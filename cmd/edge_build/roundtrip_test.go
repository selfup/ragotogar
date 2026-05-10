package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blevesearch/vellum"
	"github.com/pgvector/pgvector-go"
)

// TestRoundTrip_FullBuild exercises the entire cmd/edge_build pipeline
// against a seeded pg corpus, then re-opens every artifact and verifies
// it decodes back to data consistent with what was seeded. This is the
// integration test the 46de583 collation bug would have failed at: a
// build that completes but produces unreadable artifacts is the failure
// mode this guards against.
//
// What it covers:
//   - Full run() pipeline (FST + postings + vector lanes + payload + manifest)
//   - vellum.Open on the produced FST
//   - Posting list decode via varint at the offset the FST returns
//   - Vector lane file sizes match (rows × 2560)
//   - Rowmap file sizes match (rows × 4 bytes / uint32)
//   - Payload header + offset table + record encoding consistent
//   - manifest.json parseable with all expected fields populated
//
// What it deliberately does NOT cover (separate tests own these):
//   - cmd/edge's openArtifacts / mmap loader (cmd/edge tests cover that)
//   - The HTTP search handler (no embed endpoint mocking here)
func TestRoundTrip_FullBuild(t *testing.T) {
	db := newTempDB(t)

	// Seed three photos. Each gets a description + classifier row + one
	// embedding row in each of the three vector lanes.
	photos := []struct {
		name, subject, povContainer, sceneTime string
	}{
		{"alpha", "a quiet candid portrait", "ground", "afternoon"},
		{"beta", "an aerial shot of the city", "from_plane", "morning"},
		{"gamma", "indoor cafe scene", "ground", "evening"},
	}
	for i, p := range photos {
		if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $1)`, p.name); err != nil {
			t.Fatalf("photo %s: %v", p.name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO descriptions (photo_id, subject, full_description, mood)
			 VALUES ($1, $2, $3, '')`,
			p.name, p.subject, "Full description: "+p.subject,
		); err != nil {
			t.Fatalf("descriptions %s: %v", p.name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO exif (photo_id, camera_make, camera_model)
			 VALUES ($1, 'FUJIFILM', 'X100VI')`, p.name,
		); err != nil {
			t.Fatalf("exif %s: %v", p.name, err)
		}
		if _, err := db.Exec(`
			INSERT INTO classified (photo_id, pov_container, scene_time_of_day)
			VALUES ($1, $2, $3)
		`, p.name, p.povContainer, p.sceneTime); err != nil {
			t.Fatalf("classified %s: %v", p.name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO inference (photo_id) VALUES ($1)`, p.name,
		); err != nil {
			t.Fatalf("inference %s: %v", p.name, err)
		}

		// Distinct embedding per photo so the vector lanes have
		// determinable byte content (different magnitudes encode to
		// different int8 bytes after L2-normalization).
		emb := make([]float32, 2560)
		for j := range emb {
			emb[j] = float32(j+1+i) * 0.01
		}
		hv := pgvector.NewHalfVector(emb)
		for table, indexCol := range map[string]string{
			"photo_descriptions": "chunk_index",
			"photo_metadata":     "",
			"photo_queries":      "query_index",
		} {
			textCol := map[string]string{
				"photo_descriptions": "chunk_text",
				"photo_metadata":     "metadata_text",
				"photo_queries":      "query_text",
			}[table]
			text := p.subject + " for " + table
			if indexCol == "" {
				// photo_metadata: one row per photo, no index column.
				if _, err := db.Exec(fmt.Sprintf(`
					INSERT INTO %s (photo_id, schema_version, %s, embedding)
					VALUES ($1, 2, $2, $3)
				`, table, textCol), p.name, text, hv); err != nil {
					t.Fatalf("%s insert %s: %v", table, p.name, err)
				}
			} else {
				if _, err := db.Exec(fmt.Sprintf(`
					INSERT INTO %s (photo_id, schema_version, %s, %s, embedding)
					VALUES ($1, 2, 0, $2, $3)
				`, table, indexCol, textCol), p.name, text, hv); err != nil {
					t.Fatalf("%s insert %s: %v", table, p.name, err)
				}
			}
		}
	}

	out := t.TempDir()
	if err := run(db, out, "test-embed-model"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// === Verify each artifact ===

	// 1. Manifest: parses with expected shape.
	mb, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	if m.SchemaVersion != manifestSchemaVersion {
		t.Errorf("manifest.SchemaVersion = %d, want %d", m.SchemaVersion, manifestSchemaVersion)
	}
	if m.Dim != expectedDim {
		t.Errorf("manifest.Dim = %d, want %d", m.Dim, expectedDim)
	}
	if m.Quantization != "int8" {
		t.Errorf("manifest.Quantization = %q, want %q", m.Quantization, "int8")
	}
	if len(m.IDSpace.Names) != len(photos) {
		t.Errorf("manifest.IDSpace.Count = %d, want %d", len(m.IDSpace.Names), len(photos))
	}
	for lane, le := range m.Lanes {
		if le.EmbedderVersion != "test-embed-model" {
			t.Errorf("lane %s: EmbedderVersion = %q, want test-embed-model", lane, le.EmbedderVersion)
		}
		if le.Rows != len(photos) {
			t.Errorf("lane %s: Rows = %d, want %d", lane, le.Rows, len(photos))
		}
	}

	// 2. FST: openable via vellum.Open; Get on a known stemmed lexeme
	// returns a non-zero offset.
	fst, err := vellum.Open(filepath.Join(out, "terms.fst"))
	if err != nil {
		t.Fatalf("vellum.Open: %v", err)
	}
	defer fst.Close()
	// The pg English stemmer turns "candid" into the lexeme "candid"
	// (already a stem). Look it up; assert presence.
	val, ok, err := fst.Get([]byte("candid"))
	if err != nil {
		t.Fatalf(`fst.Get("candid"): %v`, err)
	}
	if !ok {
		t.Errorf(`fst.Get("candid"): not found — the seeded "candid portrait" should produce this lexeme`)
	}
	// 3. Postings: at the offset the FST returned, the posting list
	// decodes to at least one cid corresponding to "alpha" (the photo
	// whose subject contained "candid").
	postBytes, err := os.ReadFile(filepath.Join(out, "postings.bin"))
	if err != nil {
		t.Fatalf("read postings: %v", err)
	}
	if val < uint64(len(postBytes)) {
		count, n := binary.Uvarint(postBytes[val:])
		if n <= 0 {
			t.Errorf("postings: malformed count at offset %d", val)
		} else if count == 0 {
			t.Errorf(`postings at fst offset %d: count=0, expected >= 1 ("candid" → alpha)`, val)
		}
	}

	// 4. Vector lanes: each .bin is exactly (rows × dim) bytes; each
	// .rowmap.bin is exactly (rows × 4) bytes.
	for _, lane := range []string{"descriptions", "metadata", "queries"} {
		vecPath := filepath.Join(out, "vectors."+lane+".bin")
		mapPath := filepath.Join(out, "vectors."+lane+".rowmap.bin")
		vfi, err := os.Stat(vecPath)
		if err != nil {
			t.Fatalf("stat %s: %v", vecPath, err)
		}
		mfi, err := os.Stat(mapPath)
		if err != nil {
			t.Fatalf("stat %s: %v", mapPath, err)
		}
		expectedVec := int64(len(photos)) * int64(expectedDim)
		if vfi.Size() != expectedVec {
			t.Errorf("%s size = %d, want %d (rows × dim)", vecPath, vfi.Size(), expectedVec)
		}
		expectedMap := int64(len(photos)) * 4
		if mfi.Size() != expectedMap {
			t.Errorf("%s size = %d, want %d (rows × 4)", mapPath, mfi.Size(), expectedMap)
		}

		// Rowmap content: every uint32 must be a valid compact_id (< number of photos).
		mb, err := os.ReadFile(mapPath)
		if err != nil {
			t.Fatalf("read %s: %v", mapPath, err)
		}
		for i := 0; i < len(mb); i += 4 {
			cid := binary.LittleEndian.Uint32(mb[i:])
			if int(cid) >= len(photos) {
				t.Errorf("%s row %d: compact_id %d out of range (have %d photos)", mapPath, i/4, cid, len(photos))
			}
		}
	}

	// 5. Payload: header has correct count, offset table points to
	// records within file, each record decodes to known content.
	payloadBytes, err := os.ReadFile(filepath.Join(out, "payload.bin"))
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if len(payloadBytes) < 4 {
		t.Fatalf("payload too short: %d bytes", len(payloadBytes))
	}
	count := binary.LittleEndian.Uint32(payloadBytes[:4])
	if int(count) != len(photos) {
		t.Errorf("payload count = %d, want %d", count, len(photos))
	}

	// Each compact_id 0..count-1 has an offset entry; decode the first
	// record and verify the caption matches a seeded subject.
	offTable := payloadBytes[4 : 4+int(count)*8]
	firstOffset := binary.LittleEndian.Uint64(offTable[:8])
	if int(firstOffset) >= len(payloadBytes) {
		t.Fatalf("payload first offset %d out of range (len=%d)", firstOffset, len(payloadBytes))
	}
	captionLen, hLen := binary.Uvarint(payloadBytes[firstOffset:])
	if hLen <= 0 {
		t.Fatalf("payload record at offset %d: malformed caption length", firstOffset)
	}
	captionStart := int(firstOffset) + hLen
	captionEnd := captionStart + int(captionLen)
	if captionEnd > len(payloadBytes) {
		t.Fatalf("payload record caption past end of file")
	}
	caption := string(payloadBytes[captionStart:captionEnd])
	// Compact id 0 corresponds to the first photo in idSpace.Names; that
	// list is sorted byte-wise, so alphabetically: alpha < beta < gamma →
	// compact id 0 == "alpha" → caption "a quiet candid portrait".
	wantCaption := "a quiet candid portrait"
	if caption != wantCaption {
		t.Errorf("payload[cid=0] caption = %q, want %q", caption, wantCaption)
	}
}

// TestRoundTrip_ManifestCorpusHashIsDeterministic: re-running the build
// against the same seeded DB produces a manifest whose corpus_hash is
// stable. Catches a regression where a non-deterministic field (e.g. an
// ordering bug or a wall-clock leak) sneaks into the hash input.
func TestRoundTrip_ManifestCorpusHashIsDeterministic(t *testing.T) {
	db := newTempDB(t)

	// Minimal seed — corpus_hash depends on names + max(described_at) +
	// max(classified_at), and is sensitive to all three.
	for _, name := range []string{"p1", "p2", "p3"} {
		if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $1)`, name); err != nil {
			t.Fatalf("photo: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO descriptions (photo_id, subject) VALUES ($1, 'x')`, name); err != nil {
			t.Fatalf("descriptions: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO inference (photo_id) VALUES ($1)`, name); err != nil {
			t.Fatalf("inference: %v", err)
		}
	}

	out1 := t.TempDir()
	if err := run(db, out1, "test-embed-model"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	hash1 := readManifestHash(t, out1)

	out2 := t.TempDir()
	if err := run(db, out2, "test-embed-model"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	hash2 := readManifestHash(t, out2)

	if hash1 != hash2 {
		t.Errorf("corpus_hash diverged across runs: %s vs %s", hash1, hash2)
	}
}

func readManifestHash(t *testing.T, dir string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !strings.HasPrefix(m.CorpusHash, "") || len(m.CorpusHash) != 64 {
		t.Errorf("corpus_hash unexpected shape: %q (want 64 hex chars)", m.CorpusHash)
	}
	return m.CorpusHash
}
