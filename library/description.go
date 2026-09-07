package library

import "strings"

// ParseDescriptionSection recognizes the describer's section headers, including
// Markdown emphasis, list markers, and parenthetical asides. Both field parsing
// and query removal use this boundary so they agree about what belongs to prose.
func ParseDescriptionSection(line string) (key, value string, ok bool) {
	cleaned := strings.TrimLeft(strings.TrimSpace(line), "-*_ ")
	cleaned = strings.TrimRight(cleaned, "*_ ")
	lower := strings.ToLower(cleaned)
	for _, key := range []string{
		"subject", "setting", "light", "colors", "mood", "composition",
		"vantage", "ground truth", "condition", "queries",
	} {
		if !strings.HasPrefix(lower, key) {
			continue
		}
		after := cleaned[len(key):]
		for {
			trimmed := strings.TrimLeft(after, "*_ ")
			inside, hasAside := strings.CutPrefix(trimmed, "(")
			if !hasAside {
				after = trimmed
				break
			}
			_, rest, closed := strings.Cut(inside, ")")
			if !closed {
				after = trimmed
				break
			}
			after = rest
		}
		if rest, found := strings.CutPrefix(after, ":"); found {
			return key, strings.TrimLeft(rest, "* "), true
		}
	}
	return "", "", false
}

// StripGeneratedQueries removes Queries sections from combined vision output.
// Prose before and after each section is retained; ordinary mentions of queries
// are left alone. Responses without a Queries header are returned unchanged.
// Raw output belongs in inference.raw_response, not in a search document.
func StripGeneratedQueries(description string) string {
	var prose strings.Builder
	inQueries, removed := false, false
	for line := range strings.SplitAfterSeq(description, "\n") {
		if key, _, ok := ParseDescriptionSection(line); ok {
			inQueries = key == "queries"
			removed = removed || inQueries
		}
		if !inQueries {
			prose.WriteString(line)
		}
	}
	if !removed {
		return description
	}
	return strings.TrimSpace(prose.String())
}
