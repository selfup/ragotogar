// Package prompts holds the LLM prompt templates used elsewhere in the tree.
//
// Each template is embedded at compile time via //go:embed so the binaries
// have a single source of truth and no runtime file dependency. The .md
// files alongside this Go source are the canonical, human-editable copy —
// edit them directly; the next build picks up the change.
package prompts

import _ "embed"

// Query is the rewrite prompt for translating natural-language searches
// into Postgres websearch_to_tsquery boolean form. Substitute {{query}}
// at call time. See library.RewriteQuery for the caller.
//
//go:embed query.md
var Query string

// ClassifyFilter is the post-retrieval prompt that asks an LLM to drop
// candidate photos whose classifier verdicts (typed enums from cmd/classify)
// contradict the user's natural-language request. Substitute {{query}}
// with the original NL query and {{candidates}} with the formatted
// "id: key=val, key=val, …" lines. See library.FilterByClassification.
//
//go:embed classify_filter.md
var ClassifyFilter string
