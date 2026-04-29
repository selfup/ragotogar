package library

// Character-window chunker. nomic-embed-text-v1.5 accepts up to 8192 tokens,
// so a 6KB window leaves comfortable headroom while still chopping the
// occasional long-form description into multiple chunks.
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
		end := start + ChunkChars
		if end > len(text) {
			end = len(text)
		}
		chunks = append(chunks, text[start:end])
		if end >= len(text) {
			break
		}
	}
	return chunks
}
