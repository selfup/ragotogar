package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ── types ─────────────────────────────────────────────────────────────────

type Block struct {
	Type  string   // "p", "h", "code", "ul", "ol", "quote"
	Level int      // headings
	Text  string   // p, h, quote
	Items []string // ul, ol
	Lang  string   // code
	File  string   // code
	Body  string   // code text
}

type Section struct {
	Type  string
	Props map[string]any
	Body  string
}

// ── inline formatting ─────────────────────────────────────────────────────

var (
	reBacktick  = regexp.MustCompile("`([^`]+)`")
	reBold      = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic    = regexp.MustCompile(`(^|[^*])\*([^*\n]+)\*`)
	reLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHardBreak = regexp.MustCompile(`\\\n`)
)

func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func inline(src string) string {
	s := esc(src)
	s = reBacktick.ReplaceAllString(s, `<code class="code-inline">$1</code>`)
	s = reBold.ReplaceAllString(s, `<strong>$1</strong>`)
	s = reItalic.ReplaceAllStringFunc(s, func(m string) string {
		sub := reItalic.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		return sub[1] + "<em>" + sub[2] + "</em>"
	})
	s = reLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = reHardBreak.ReplaceAllString(s, "<br>")
	return s
}

// ── block parser ──────────────────────────────────────────────────────────

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reUL      = regexp.MustCompile(`^[-*]\s`)
	reOL      = regexp.MustCompile(`^\d+\.\s`)
	reQuote   = regexp.MustCompile(`^>\s?`)
)

func parseBlocks(md string) []Block {
	lines := strings.Split(md, "\n")
	var blocks []Block
	i := 0
	for i < len(lines) {
		line := lines[i]

		// blank line
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}

		// code fence
		if strings.HasPrefix(line, "```") {
			header := strings.TrimPrefix(line, "```")
			parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
			lang, file := "", ""
			if len(parts) > 0 {
				lang = strings.TrimSpace(parts[0])
			}
			if len(parts) > 1 {
				file = strings.TrimSpace(parts[1])
			}
			i++
			var body []string
			for i < len(lines) && !strings.HasPrefix(lines[i], "```") {
				body = append(body, lines[i])
				i++
			}
			i++ // closing fence
			blocks = append(blocks, Block{Type: "code", Lang: lang, File: file, Body: strings.Join(body, "\n")})
			continue
		}

		// heading
		if m := reHeading.FindStringSubmatch(line); m != nil {
			blocks = append(blocks, Block{Type: "h", Level: len(m[1]), Text: m[2]})
			i++
			continue
		}

		// blockquote
		if reQuote.MatchString(line) {
			var parts []string
			for i < len(lines) && reQuote.MatchString(lines[i]) {
				parts = append(parts, reQuote.ReplaceAllString(lines[i], ""))
				i++
			}
			blocks = append(blocks, Block{Type: "quote", Text: strings.Join(parts, " ")})
			continue
		}

		// unordered list
		if reUL.MatchString(line) {
			var items []string
			for i < len(lines) && reUL.MatchString(lines[i]) {
				items = append(items, reUL.ReplaceAllString(lines[i], ""))
				i++
			}
			blocks = append(blocks, Block{Type: "ul", Items: items})
			continue
		}

		// ordered list
		if reOL.MatchString(line) {
			var items []string
			for i < len(lines) && reOL.MatchString(lines[i]) {
				items = append(items, reOL.ReplaceAllString(lines[i], ""))
				i++
			}
			blocks = append(blocks, Block{Type: "ol", Items: items})
			continue
		}

		// paragraph — consume consecutive non-block lines
		var paraLines []string
		for i < len(lines) {
			l := lines[i]
			if strings.TrimSpace(l) == "" {
				break
			}
			if strings.HasPrefix(l, "```") || reHeading.MatchString(l) ||
				reQuote.MatchString(l) || reUL.MatchString(l) || reOL.MatchString(l) {
				break
			}
			paraLines = append(paraLines, l)
			i++
		}
		if len(paraLines) > 0 {
			blocks = append(blocks, Block{Type: "p", Text: strings.Join(paraLines, " ")})
		}
	}
	return blocks
}

// ── section parser ────────────────────────────────────────────────────────

var reSectionFence = regexp.MustCompile(`(?m)^:::([\w-]+)(?:\s+(\{[^\n]*\}))?\s*\n([\s\S]*?)\n:::[ \t]*(?:\n|$)`)

func parseSections(md string) (string, []Section) {
	var sections []Section
	pre := ""
	firstIdx := reSectionFence.FindStringIndex(md)
	if firstIdx != nil {
		pre = strings.TrimSpace(md[:firstIdx[0]])
	}
	matches := reSectionFence.FindAllStringSubmatchIndex(md, -1)
	allMatches := reSectionFence.FindAllStringSubmatch(md, -1)
	for idx, m := range allMatches {
		_ = matches[idx]
		sType := m[1]
		propsJSON := m[2]
		body := strings.TrimSpace(m[3])

		props := map[string]any{}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &props)
		}
		sections = append(sections, Section{Type: sType, Props: props, Body: body})
	}
	return pre, sections
}
