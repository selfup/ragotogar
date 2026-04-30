package library

// Character-window chunker. Qwen3-Embedding-4B accepts up to 32K tokens, so
// a 6KB window leaves enormous headroom while still chopping the occasional
// long-form description into multiple chunks. The window size is held over
// from the nomic-embed-text-v1.5 era (8192-token limit) — revisit if recall
// suggests longer chunks would help under the stronger embedder.
//
// Matches tools/rag_common.chunk_text from the Phase 2 Python era so the
// embedded chunks stay byte-comparable across the rewrite.
const (
	ChunkChars   = 6000
	ChunkOverlap = 400
)

// Chunk splits text into overlapping character windows. Empty input yields
// an empty slice; documents shorter than ChunkChars yield exactly one chunk.
func Chunk(text string) []string {
	if text == "" {
		return nil
	}
	if len(text) <= ChunkChars {
		return []string{text}
	}
	step := ChunkChars - ChunkOverlap
	var chunks []string
	for start := 0; start < len(text); start += step {
		end := min(start+ChunkChars, len(text))
		chunks = append(chunks, text[start:end])
		if end >= len(text) {
			break
		}
	}
	return chunks
}
