package main

import (
	"bufio"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/pgvector/pgvector-go"
)

// laneCounts records per-lane row counts for the manifest.
type laneCounts struct {
	Descriptions int
	Metadata     int
	Queries      int
}

// expectedDim is the locked vector dimension. halfvec(2560) → int8(2560).
// Mismatched rows are an immediate hard error — manifest claims dim=2560
// at runtime, drift here would surface as silent corruption.
const expectedDim = 2560

// buildVectorLanes writes three lanes of L2-normalized int8 vectors plus
// per-lane row→compact-id sidecar files. Uniform sidecar handling across
// lanes — even photo_metadata (one row per photo) gets a rowmap so the
// edge runtime never relies on the implicit "row index = compact id"
// assumption.
//
// Files written into outDir:
//
//	vectors.descriptions.bin      flat int8, [N_d × 2560]
//	vectors.descriptions.rowmap.bin   uint32 LE, length N_d
//	vectors.metadata.bin          flat int8, [N_m × 2560]
//	vectors.metadata.rowmap.bin   uint32 LE, length N_m
//	vectors.queries.bin           flat int8, [N_q × 2560]
//	vectors.queries.rowmap.bin    uint32 LE, length N_q
func buildVectorLanes(db *sql.DB, ids *idSpace, outDir string) (laneCounts, error) {
	var counts laneCounts

	// COLLATE "C" forces byte-wise lex on photo_id sorts so artifacts
	// stay byte-identical across hosts with different lc_collate
	// settings (corpus_hash determinism). Within-row ordering doesn't
	// affect runtime correctness since the rowmap sidecar carries the
	// compact_id per row, but byte-stable artifacts are a contract.
	d, err := writeLane(db, ids, outDir, "descriptions",
		`SELECT photo_id, embedding FROM photo_descriptions
		   ORDER BY photo_id COLLATE "C", chunk_index`)
	if err != nil {
		return counts, fmt.Errorf("descriptions lane: %w", err)
	}
	counts.Descriptions = d

	m, err := writeLane(db, ids, outDir, "metadata",
		`SELECT photo_id, embedding FROM photo_metadata
		   ORDER BY photo_id COLLATE "C"`)
	if err != nil {
		return counts, fmt.Errorf("metadata lane: %w", err)
	}
	counts.Metadata = m

	q, err := writeLane(db, ids, outDir, "queries",
		`SELECT photo_id, embedding FROM photo_queries
		   ORDER BY photo_id COLLATE "C", query_index`)
	if err != nil {
		return counts, fmt.Errorf("queries lane: %w", err)
	}
	counts.Queries = q

	return counts, nil
}

func writeLane(db *sql.DB, ids *idSpace, outDir, lane, query string) (int, error) {
	vecPath := filepath.Join(outDir, "vectors."+lane+".bin")
	mapPath := filepath.Join(outDir, "vectors."+lane+".rowmap.bin")

	vecFile, err := os.Create(vecPath)
	if err != nil {
		return 0, err
	}
	defer vecFile.Close()
	mapFile, err := os.Create(mapPath)
	if err != nil {
		return 0, err
	}
	defer mapFile.Close()

	vecBW := bufio.NewWriter(vecFile)
	mapBW := bufio.NewWriter(mapFile)

	rows, err := db.Query(query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	rowBuf := make([]byte, expectedDim) // one int8 per dim
	idScratch := make([]byte, 4)
	count := 0

	for rows.Next() {
		var name string
		var hv pgvector.HalfVector
		if err := rows.Scan(&name, &hv); err != nil {
			return 0, err
		}
		cid, ok := ids.CompactID(name)
		if !ok {
			// Vector row for a photo not in id_space — skip rather than
			// poison the lane with an unmapped row. Logged at the caller
			// via final count diff if it ever happens.
			continue
		}
		fp32 := hv.Slice()
		if len(fp32) != expectedDim {
			return 0, fmt.Errorf("%s lane: photo %s has dim=%d, expected %d", lane, name, len(fp32), expectedDim)
		}
		quantizeInt8(fp32, rowBuf)
		if _, err := vecBW.Write(rowBuf); err != nil {
			return 0, err
		}
		binary.LittleEndian.PutUint32(idScratch, cid)
		if _, err := mapBW.Write(idScratch); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := vecBW.Flush(); err != nil {
		return 0, err
	}
	if err := mapBW.Flush(); err != nil {
		return 0, err
	}
	return count, nil
}

// quantizeInt8 L2-normalizes v in place (conceptually) and writes the
// rounded int8 components to dst. dst must be len(v) bytes long. The
// stored values are int8, but Go's []byte is the natural mmap-friendly
// container — readers reinterpret each byte as int8 via a signed cast.
//
// Approach: norm = sqrt(sum(x²)); scale = 127 / norm; round; clamp to
// [-127, 127]. The asymmetric saturating clamp avoids producing -128
// which has no positive counterpart and would skew the dot product on
// vectors with extreme components. Pure-zero vectors (norm == 0) write
// zeros — a degenerate case that shouldn't arise from a real embedder
// but is handled rather than panicking on /0.
func quantizeInt8(v []float32, dst []byte) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		for i := range dst {
			dst[i] = 0
		}
		return
	}
	scale := 127.0 / math.Sqrt(sum)
	for i, x := range v {
		q := math.Round(float64(x) * scale)
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		dst[i] = byte(int8(q))
	}
}
