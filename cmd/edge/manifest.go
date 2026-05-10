package main

// Manifest mirrors the on-disk shape produced by cmd/edge_build. The
// edge runtime parses it at startup, drift-checks per-lane
// embedder_version, and uses id_space.names[i] to translate compact
// uint32 ids back to photos.name for hydration calls.
//
// Schema-version mismatch is fatal — manifest.SchemaVersion below the
// minimum we know how to read means the artifacts predate this binary
// (refuse to load); above the maximum means the artifacts are newer
// (refuse rather than silently misread).
type Manifest struct {
	SchemaVersion int                  `json:"schema_version"`
	CorpusHash    string               `json:"corpus_hash"`
	BuiltAt       string               `json:"built_at"`
	Dim           int                  `json:"dim"`
	Quantization  string               `json:"quantization"`
	Lanes         map[string]LaneEntry `json:"lanes"`
	IDSpace       IDSpaceEntry         `json:"id_space"`
	Payload       PayloadEntry         `json:"payload"`
}

type LaneEntry struct {
	EmbedderVersion string `json:"embedder_version"`
	Rows            int    `json:"rows"`
}

type IDSpaceEntry struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

type PayloadEntry struct {
	Tags []string `json:"tags"`
}

// supportedManifestVersion is the only schema version this binary
// reads. Bumping this is intentional — a release that knows multiple
// versions should accept a range, not silently misread a future shape.
const supportedManifestVersion = 1
