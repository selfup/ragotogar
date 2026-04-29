package library

import (
	"strings"
	"testing"
)

func TestChunkEmpty(t *testing.T) {
	if got := Chunk(""); len(got) != 0 {
		t.Errorf("empty input → %v, want nil", got)
	}
}

func TestChunkSingle(t *testing.T) {
	short := "A small photo description that fits in one chunk."
	got := Chunk(short)
	if len(got) != 1 || got[0] != short {
		t.Errorf("short doc → %v chunks, want exactly 1 unchanged", len(got))
	}
}

func TestChunkOverlap(t *testing.T) {
	long := strings.Repeat("a", ChunkChars*2+500)
	got := Chunk(long)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks for long doc, got %d", len(got))
	}
	for i, c := range got {
		if len(c) > ChunkChars {
			t.Errorf("chunk %d exceeds ChunkChars: %d > %d", i, len(c), ChunkChars)
		}
	}
	// Adjacent chunks must overlap by ChunkOverlap chars.
	for i := 1; i < len(got); i++ {
		prev := got[i-1]
		cur := got[i]
		// Last `ChunkOverlap` of prev must appear at the start of cur.
		tail := prev[len(prev)-ChunkOverlap:]
		if !strings.HasPrefix(cur, tail) {
			t.Errorf("chunk %d does not begin with the trailing overlap of chunk %d", i, i-1)
		}
	}
}

func TestChunkExactlyChunkChars(t *testing.T) {
	doc := strings.Repeat("a", ChunkChars)
	got := Chunk(doc)
	if len(got) != 1 {
		t.Errorf("doc of length ChunkChars → %d chunks, want 1", len(got))
	}
}
