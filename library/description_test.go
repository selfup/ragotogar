package library

import (
	"strings"
	"testing"
)

func TestStripGeneratedQueries(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"plain tail", "Subject: cedar trees\nQueries:\nzeppelins at sunset\n", "Subject: cedar trees"},
		{"inline query", "Subject: cedar trees\nQueries: zeppelins at sunset", "Subject: cedar trees"},
		{"bold header", "Subject: cedar trees\n**Queries:**\nzeppelins", "Subject: cedar trees"},
		{"list and aside", "Subject: cedar trees\n- **QUERIES** (search phrases):\n1. zeppelins", "Subject: cedar trees"},
		{"underscores", "Subject: cedar trees\n__Queries__: zeppelins", "Subject: cedar trees"},
		{"prose after queries", "Subject: cedar trees\nQueries:\nzeppelins\nMood: peaceful\nLight: daylight", "Subject: cedar trees\nMood: peaceful\nLight: daylight"},
		{"multiple sections", "Queries: zeppelins\nSubject: cedar trees\nQueries:\nsubmarines", "Subject: cedar trees"},
		{"queries only", "Queries:\nzeppelins", ""},
		{"CRLF", "Subject: cedar trees\r\nQueries:\r\nzeppelins\r\n", "Subject: cedar trees"},
		{"legacy prose", "  A forest without formatted fields.\n", "  A forest without formatted fields.\n"},
		{"literal mention", "Subject: a sign reading Queries: ask here", "Subject: a sign reading Queries: ask here"},
		{"not a header", "Queries about trees are written on a sign.", "Queries about trees are written on a sign."},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripGeneratedQueries(tc.input)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if again := StripGeneratedQueries(got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestDescriptionAndQueryDocumentsIsolateCombinedResponse(t *testing.T) {
	p := &Photo{
		Name: "forest", FileBasename: "forest.jpg",
		FullDescription:  "Subject: cedar trees\nQueries:\nzeppelins at sunset",
		GeneratedQueries: []string{"zeppelins at sunset"},
	}
	doc := BuildDescriptionDocument(p)
	if !strings.Contains(doc, "cedar trees") || strings.Contains(doc, "zeppelins") || strings.Contains(doc, "Queries:") {
		t.Errorf("description document did not isolate prose: %q", doc)
	}
	queries := BuildQueryDocuments(p)
	if len(queries) != 1 || queries[0] != "zeppelins at sunset" {
		t.Errorf("generated query changed: %v", queries)
	}
	if !strings.Contains(p.FullDescription, "zeppelins") {
		t.Error("document construction mutated its source")
	}
}
