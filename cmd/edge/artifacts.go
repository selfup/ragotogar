package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/blevesearch/mmap-go"
	"github.com/blevesearch/vellum"
)

// Artifacts is the runtime view of the seven on-disk files cmd/edge_build
// produced. mmap-backed where the file is large or the access pattern
// is random (vector blobs, postings, payload bytes). Read into memory
// where the file is small and indexed densely (rowmaps, payload offset
// table) — saves a syscall per access at negligible memory cost.
type Artifacts struct {
	Manifest *Manifest

	FST *vellum.FST

	Postings     []byte // mmap; indexed by uint64 offset stored in FST values
	postingsMmap mmap.MMap
	postingsFile *os.File

	Lanes map[string]*VectorLane

	// PayloadBytes is the full payload.bin mmap. PayloadOffsets[cid]
	// is the file-relative start of cid's record (varint(len)+bytes
	// for caption then 5×varint+bytes for tags). The whole file is
	// mapped, so PayloadBytes[offset:] accesses the record directly
	// without arithmetic on the header.
	PayloadBytes   []byte
	PayloadOffsets []uint64
	payloadMmap    mmap.MMap
	payloadFile    *os.File
}

// VectorLane is one lane of int8-quantized vectors plus the row→compact-id
// sidecar. Vectors are mmap'd as raw bytes; reinterpret each as int8 via
// signed cast. RowMap is in-memory because the dense uint32 access pattern
// during a scan benefits from no syscall overhead.
type VectorLane struct {
	Name    string
	Vectors []byte   // mmap'd, len = rows × dim
	RowMap  []uint32 // in-memory, len = rows
	Rows    int

	vMmap mmap.MMap
	vFile *os.File
}

// openArtifacts opens every artifact file referenced by mf in dir and
// returns a fully-loaded Artifacts. On any failure, partial state is
// cleaned up before returning the error — caller does not need to call
// Close on a non-nil error path.
func openArtifacts(dir string, mf *Manifest) (a *Artifacts, err error) {
	a = &Artifacts{
		Manifest: mf,
		Lanes:    map[string]*VectorLane{},
	}
	defer func() {
		if err != nil {
			a.Close()
			a = nil
		}
	}()

	// FST — vellum.Open mmaps internally.
	fstPath := filepath.Join(dir, "terms.fst")
	if a.FST, err = vellum.Open(fstPath); err != nil {
		return nil, fmt.Errorf("open %s: %w", fstPath, err)
	}

	// Postings — random-access by uint64 offset, mmap.
	if a.Postings, a.postingsMmap, a.postingsFile, err = mmapFile(filepath.Join(dir, "postings.bin")); err != nil {
		return nil, err
	}

	// Three vector lanes.
	for _, name := range []string{"descriptions", "metadata", "queries"} {
		entry, ok := mf.Lanes[name]
		if !ok {
			return nil, fmt.Errorf("manifest missing lane %q", name)
		}
		lane, err := openVectorLane(dir, name, entry.Rows, mf.Dim)
		if err != nil {
			return nil, err
		}
		a.Lanes[name] = lane
	}

	// Payload — mmap full file, parse offset table into memory.
	a.PayloadBytes, a.payloadMmap, a.payloadFile, err = mmapFile(filepath.Join(dir, "payload.bin"))
	if err != nil {
		return nil, err
	}
	if len(a.PayloadBytes) < 4 {
		return nil, fmt.Errorf("payload.bin too short: %d bytes", len(a.PayloadBytes))
	}
	count := binary.LittleEndian.Uint32(a.PayloadBytes[:4])
	if int(count) != mf.IDSpace.Count {
		return nil, fmt.Errorf("payload count=%d, manifest id_space=%d", count, mf.IDSpace.Count)
	}
	tableStart := 4
	tableEnd := tableStart + int(count)*8
	if len(a.PayloadBytes) < tableEnd {
		return nil, fmt.Errorf("payload.bin truncated: header expects %d bytes, file has %d", tableEnd, len(a.PayloadBytes))
	}
	a.PayloadOffsets = make([]uint64, count)
	for i := range a.PayloadOffsets {
		a.PayloadOffsets[i] = binary.LittleEndian.Uint64(a.PayloadBytes[tableStart+i*8 : tableStart+(i+1)*8])
	}

	return a, nil
}

func openVectorLane(dir, name string, rows, dim int) (*VectorLane, error) {
	lane := &VectorLane{Name: name, Rows: rows}

	vBytes, vM, vF, err := mmapFile(filepath.Join(dir, "vectors."+name+".bin"))
	if err != nil {
		return nil, err
	}
	expectVecLen := rows * dim
	if len(vBytes) != expectVecLen {
		_ = vM.Unmap()
		_ = vF.Close()
		return nil, fmt.Errorf("vectors.%s.bin size=%d, expected %d (rows=%d, dim=%d)", name, len(vBytes), expectVecLen, rows, dim)
	}
	lane.Vectors, lane.vMmap, lane.vFile = vBytes, vM, vF

	// Rowmap is small — read into memory rather than mmap. uint32 LE
	// per row.
	rmPath := filepath.Join(dir, "vectors."+name+".rowmap.bin")
	rmBytes, err := os.ReadFile(rmPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rmPath, err)
	}
	if len(rmBytes) != rows*4 {
		return nil, fmt.Errorf("rowmap.%s size=%d, expected %d (rows=%d)", name, len(rmBytes), rows*4, rows)
	}
	lane.RowMap = make([]uint32, rows)
	for i := range lane.RowMap {
		lane.RowMap[i] = binary.LittleEndian.Uint32(rmBytes[i*4 : (i+1)*4])
	}

	return lane, nil
}

// mmapFile opens path read-only and maps the entire file. Returns the
// byte slice (the mmap), the MMap handle (for Unmap), and the *os.File
// (for Close). All three are needed at Close time.
func mmapFile(path string) ([]byte, mmap.MMap, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	m, err := mmap.Map(f, mmap.RDONLY, 0)
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	return []byte(m), m, f, nil
}

// Close releases all mmap'd regions and closes file handles. Safe to
// call on partially-loaded Artifacts (each step nils its handle if the
// caller already cleaned up).
func (a *Artifacts) Close() error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if a.FST != nil {
		record(a.FST.Close())
	}
	for _, lane := range a.Lanes {
		if lane.vMmap != nil {
			record(lane.vMmap.Unmap())
		}
		if lane.vFile != nil {
			record(lane.vFile.Close())
		}
	}
	if a.postingsMmap != nil {
		record(a.postingsMmap.Unmap())
	}
	if a.postingsFile != nil {
		record(a.postingsFile.Close())
	}
	if a.payloadMmap != nil {
		record(a.payloadMmap.Unmap())
	}
	if a.payloadFile != nil {
		record(a.payloadFile.Close())
	}
	return firstErr
}

// Compile-time guard that VectorLane satisfies io.Closer for tests
// that want a uniform handle.
var _ io.Closer = (*Artifacts)(nil)
