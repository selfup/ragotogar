package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// manifestSchemaVersion is the artifact-set version. Bump on any
// breaking change to artifact file layout, manifest field shape, or
// the payload tag contract. Edge runtime must compare and refuse to
// load a higher version.
const manifestSchemaVersion = 1

type manifest struct {
	SchemaVersion int                  `json:"schema_version"`
	CorpusHash    string               `json:"corpus_hash"`
	BuiltAt       string               `json:"built_at"`
	Dim           int                  `json:"dim"`
	Quantization  string               `json:"quantization"`
	Lanes         map[string]laneEntry `json:"lanes"`
	IDSpace       idSpaceEntry         `json:"id_space"`
	Payload       payloadEntry         `json:"payload"`
}

type laneEntry struct {
	EmbedderVersion string `json:"embedder_version"`
	Rows            int    `json:"rows"`
}

type idSpaceEntry struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

type payloadEntry struct {
	Tags []string `json:"tags"`
}

func writeManifest(db *sql.DB, ids *idSpace, lanes laneCounts, embedModel, path string) error {
	var maxDescribed, maxClassified sql.NullTime
	if err := db.QueryRow(`SELECT MAX(described_at) FROM inference`).Scan(&maxDescribed); err != nil {
		return fmt.Errorf("max described_at: %w", err)
	}
	if err := db.QueryRow(`SELECT MAX(classified_at) FROM classified`).Scan(&maxClassified); err != nil {
		return fmt.Errorf("max classified_at: %w", err)
	}
	hash := corpusHash(ids.Names, maxDescribed, maxClassified)

	m := manifest{
		SchemaVersion: manifestSchemaVersion,
		CorpusHash:    hash,
		BuiltAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Dim:           expectedDim,
		Quantization:  "int8",
		Lanes: map[string]laneEntry{
			"descriptions": {EmbedderVersion: embedModel, Rows: lanes.Descriptions},
			"metadata":     {EmbedderVersion: embedModel, Rows: lanes.Metadata},
			"queries":      {EmbedderVersion: embedModel, Rows: lanes.Queries},
		},
		IDSpace: idSpaceEntry{
			Count: len(ids.Names),
			Names: ids.Names,
		},
		Payload: payloadEntry{
			Tags: payloadTagFields,
		},
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(&m)
}

// corpusHash digests sorted(names) ‖ max(described_at) ‖
// max(classified_at). Pure function; the DB-querying caller in
// writeManifest reads the timestamps and feeds them in. Names must
// already be sorted (idSpace.Names is by construction).
//
// Separator byte 0 between names prevents "ab"+"c" colliding with
// "a"+"bc". `D:` and `C:` prefixes prevent a swapped timestamp from
// producing the same digest.
func corpusHash(names []string, maxDescribed, maxClassified sql.NullTime) string {
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	if maxDescribed.Valid {
		h.Write([]byte("D:" + maxDescribed.Time.UTC().Format(time.RFC3339Nano)))
	} else {
		h.Write([]byte("D:none"))
	}
	if maxClassified.Valid {
		h.Write([]byte("C:" + maxClassified.Time.UTC().Format(time.RFC3339Nano)))
	} else {
		h.Write([]byte("C:none"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
