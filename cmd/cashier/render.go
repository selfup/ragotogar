package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ── shared helpers ────────────────────────────────────────────────────────

func sectionMarker(num, label string) string {
	return fmt.Sprintf(`<div class="section-marker"><span class="numeral">%s</span><span class="rule"></span><span class="label">%s</span></div>`,
		num, inline(label))
}

func propStr(props map[string]any, key, fallback string) string {
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fallback
}

var reSplitRule = regexp.MustCompile(`(?m)^\s*---\s*$`)

func splitByRule(body string) []string {
	var out []string
	for _, chunk := range reSplitRule.Split(body, -1) {
		chunk = strings.TrimSpace(chunk)
		if chunk != "" {
			out = append(out, chunk)
		}
	}
	return out
}

var reHeadingLine = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

func splitHeading(body string) (heading, rest string) {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if m := reHeadingLine.FindStringSubmatch(l); m != nil {
			remaining := append(lines[:i:i], lines[i+1:]...)
			return m[2], strings.TrimSpace(strings.Join(remaining, "\n"))
		}
		if strings.TrimSpace(l) != "" {
			// non-heading content first — no split
			break
		}
	}
	return "", body
}

func renderBody(md string, pClass string) string {
	blocks := parseBlocks(md)
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "p":
			if pClass != "" {
				fmt.Fprintf(&sb, "<p class=%q>%s</p>\n", pClass, inline(b.Text))
			} else {
				fmt.Fprintf(&sb, "<p>%s</p>\n", inline(b.Text))
			}
		case "h":
			fmt.Fprintf(&sb, "<h%d>%s</h%d>\n", b.Level, inline(b.Text), b.Level)
		case "ul":
			sb.WriteString("<ul>")
			for _, it := range b.Items {
				fmt.Fprintf(&sb, "<li>%s</li>", inline(it))
			}
			sb.WriteString("</ul>\n")
		case "ol":
			sb.WriteString("<ol>")
			for _, it := range b.Items {
				fmt.Fprintf(&sb, "<li>%s</li>", inline(it))
			}
			sb.WriteString("</ol>\n")
		case "quote":
			fmt.Fprintf(&sb, "<blockquote>%s</blockquote>\n", inline(b.Text))
		case "code":
			sb.WriteString(renderCode(b))
			sb.WriteByte('\n')
		}
	}
	return strings.TrimSpace(sb.String())
}

func renderCode(b Block) string {
	head := ""
	if b.Lang != "" || b.File != "" {
		langPart, filePart := "", ""
		if b.Lang != "" {
			langPart = fmt.Sprintf(`<div class="code-lang">%s</div>`, esc(b.Lang))
		}
		if b.File != "" {
			filePart = fmt.Sprintf(`<div class="code-file">%s</div>`, esc(b.File))
		}
		head = fmt.Sprintf(`<div class="code-head">%s%s</div>`, langPart, filePart)
	}
	body := esc(b.Body)
	if strings.ToLower(b.Lang) == "rust" {
		body = highlightRust(b.Body)
	}
	return fmt.Sprintf(`<div class="code-block">%s<pre><code>%s</code></pre></div>`, head, body)
}

func embedImage(src string, width int) (string, error) {
	path := strings.TrimPrefix(src, "file://")
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("cashier_%d_%d.jpg", os.Getpid(), time.Now().UnixNano()))
	defer os.Remove(tmp)
	resize := fmt.Sprintf("%dx>", width)
	cmd := exec.Command("magick", path, "-resize", resize, "-quality", "85", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("magick: %s: %w", out, err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}

func proseRender(cls, screenLabel, num, label string, props map[string]any, body string) string {
	heading, rest := splitHeading(body)
	sl := propStr(props, "label", screenLabel)
	n := propStr(props, "num", num)
	lbl := propStr(props, "label", label)
	blocks := parseBlocks(rest)
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "p":
			fmt.Fprintf(&sb, `<p class="body">%s</p>`+"\n", inline(b.Text))
		case "code":
			sb.WriteString(renderCode(b))
			sb.WriteByte('\n')
		case "ul":
			sb.WriteString(`<ul class="body-list">`)
			for _, it := range b.Items {
				fmt.Fprintf(&sb, "<li>%s</li>", inline(it))
			}
			sb.WriteString("</ul>\n")
		case "ol":
			sb.WriteString(`<ol class="body-list">`)
			for _, it := range b.Items {
				fmt.Fprintf(&sb, "<li>%s</li>", inline(it))
			}
			sb.WriteString("</ol>\n")
		case "h":
			fmt.Fprintf(&sb, `<h%d class="body-h">%s</h%d>`+"\n", b.Level, inline(b.Text), b.Level)
		}
	}
	return fmt.Sprintf(`<section class="%s" data-screen-label="%s">
  %s
  <h2 class="section-h2">%s</h2>
  %s
</section>`, cls, sl, sectionMarker(n, lbl), inline(heading), strings.TrimSpace(sb.String()))
}

// ── Rust syntax highlighter ───────────────────────────────────────────────

var rustKeywords = map[string]bool{
	"as": true, "async": true, "await": true, "break": true, "const": true,
	"continue": true, "crate": true, "dyn": true, "else": true, "enum": true,
	"extern": true, "false": true, "fn": true, "for": true, "if": true,
	"impl": true, "in": true, "let": true, "loop": true, "match": true,
	"mod": true, "move": true, "mut": true, "pub": true, "ref": true,
	"return": true, "self": true, "Self": true, "static": true, "struct": true,
	"super": true, "trait": true, "true": true, "type": true, "unsafe": true,
	"use": true, "where": true, "while": true, "box": true,
}
var rustDefKeywords = map[string]bool{
	"impl": true, "enum": true, "struct": true, "trait": true,
	"fn": true, "type": true, "mod": true, "union": true,
}

func isIdent(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}
func isIdentCont(c byte) bool {
	return isIdent(c) || (c >= '0' && c <= '9')
}
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func highlightRust(src string) string {
	var sb strings.Builder
	i := 0
	n := len(src)
	prevSig := ""

	for i < n {
		c := src[i]

		// whitespace
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			j := i
			for j < n && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			sb.WriteString(esc(src[i:j]))
			i = j
			continue
		}

		// line comment
		if c == '/' && i+1 < n && src[i+1] == '/' {
			j := i
			for j < n && src[j] != '\n' {
				j++
			}
			fmt.Fprintf(&sb, `<span class="tok-com">%s</span>`, esc(src[i:j]))
			i = j
			prevSig = ""
			continue
		}

		// block comment
		if c == '/' && i+1 < n && src[i+1] == '*' {
			j := i + 2
			for j < n-1 && !(src[j] == '*' && src[j+1] == '/') {
				j++
			}
			if j < n-1 {
				j += 2
			}
			fmt.Fprintf(&sb, `<span class="tok-com">%s</span>`, esc(src[i:j]))
			i = j
			prevSig = ""
			continue
		}

		// string
		if c == '"' {
			j := i + 1
			for j < n && src[j] != '"' {
				if src[j] == '\\' && j+1 < n {
					j += 2
				} else {
					j++
				}
			}
			if j < n {
				j++
			}
			fmt.Fprintf(&sb, `<span class="tok-str">%s</span>`, esc(src[i:j]))
			i = j
			prevSig = "str"
			continue
		}

		// attribute #[...]
		if c == '#' && i+1 < n && src[i+1] == '[' {
			j := i + 2
			depth := 1
			for j < n && depth > 0 {
				switch src[j] {
				case '[':
					depth++
				case ']':
					depth--
				}
				j++
			}
			fmt.Fprintf(&sb, `<span class="tok-attr">%s</span>`, esc(src[i:j]))
			i = j
			prevSig = "attr"
			continue
		}

		// number
		if isDigit(c) {
			j := i
			for j < n && (isHexDigit(src[j]) || src[j] == 'x' || src[j] == 'X' || src[j] == '_' || src[j] == '.') {
				j++
			}
			fmt.Fprintf(&sb, `<span class="tok-num">%s</span>`, esc(src[i:j]))
			i = j
			prevSig = "num"
			continue
		}

		// path separator ::
		if c == ':' && i+1 < n && src[i+1] == ':' {
			sb.WriteString("::")
			i += 2
			prevSig = "::"
			continue
		}

		// identifier
		if isIdent(c) {
			j := i
			for j < n && isIdentCont(src[j]) {
				j++
			}
			word := src[i:j]
			followedByCall := j < n && (src[j] == '(' || (j+1 < n && src[j] == '!' && src[j+1] == '('))
			if rustKeywords[word] {
				fmt.Fprintf(&sb, `<span class="tok-key">%s</span>`, esc(word))
			} else if prevSig == "::" || rustDefKeywords[prevSig] || followedByCall {
				fmt.Fprintf(&sb, `<span class="tok-fn">%s</span>`, esc(word))
			} else {
				sb.WriteString(esc(word))
			}
			i = j
			prevSig = word
			continue
		}

		sb.WriteString(esc(src[i : i+1]))
		i++
		prevSig = src[i-1 : i]
	}
	return sb.String()
}

// ── section renderers ─────────────────────────────────────────────────────

var romans = []string{"i.", "ii.", "iii.", "iv.", "v.", "vi.", "vii.", "viii.", "ix.", "x.", "xi.", "xii."}

func romanOrN(idx int) string {
	if idx < len(romans) {
		return romans[idx]
	}
	return fmt.Sprintf("%d.", idx+1)
}

func renderHero(sec Section) (string, error) {
	chunks := splitByRule(sec.Body)
	titleBlock, sub, tag := "", "", ""
	if len(chunks) > 0 {
		titleBlock = chunks[0]
	}
	if len(chunks) > 1 {
		sub = chunks[1]
	}
	if len(chunks) > 2 {
		tag = chunks[2]
	}

	lines := strings.Split(titleBlock, "\n")
	heading, overline := "", ""
	hIdx := -1
	for i, l := range lines {
		if reHeadingLine.MatchString(l) {
			hIdx = i
			break
		}
	}
	if hIdx >= 0 {
		m := reHeadingLine.FindStringSubmatch(lines[hIdx])
		heading = m[2]
		var rest []string
		rest = append(rest, lines[:hIdx]...)
		rest = append(rest, lines[hIdx+1:]...)
		overline = strings.TrimSpace(strings.Join(rest, "\n"))
	} else {
		overline = strings.TrimSpace(titleBlock)
	}

	// split heading on "/" → <br>
	titleParts := strings.Split(heading, "/")
	var titleHTMLParts []string
	for _, part := range titleParts {
		h := inline(strings.TrimSpace(part))
		h = strings.ReplaceAll(h, "<em>", `<span class="italic">`)
		h = strings.ReplaceAll(h, "</em>", "</span>")
		titleHTMLParts = append(titleHTMLParts, h)
	}
	titleHTML := strings.Join(titleHTMLParts, "<br>")

	imgHTML := ""
	if imgSrc := propStr(sec.Props, "image", ""); imgSrc != "" {
		b64, err := embedImage(imgSrc, 2048)
		if err != nil {
			return "", fmt.Errorf("embedImage: %w", err)
		}
		imgHTML = fmt.Sprintf("<figure style=\"margin:0;padding:2rem;background:#fff;text-align:center;\"><img src=\"%s\" style=\"max-width:100%%;max-height:75vh;width:auto;height:auto;display:inline-block;\" alt=\"\"></figure>\n", b64)
	}

	overlineHTML := ""
	if overline != "" {
		overlineHTML = fmt.Sprintf(`<div class="hero-overline">%s</div>`, inline(overline))
	}
	subHTML := ""
	if sub != "" {
		subHTML = fmt.Sprintf(`<p class="hero-sub">%s</p>`, inline(sub))
	}

	return fmt.Sprintf(`%s<section class="hero" data-screen-label="00 Hero">
  <div class="hero-header">
    <div class="mark">%s</div>
    <div class="meta">%s</div>
  </div>
  <div class="hero-center">
    %s
    <h1 class="hero-title">%s</h1>
    %s
  </div>
  <div class="hero-footer">
    <div class="tagline">%s</div>
    <div class="centered">§</div>
    <div class="scroll">Continue</div>
  </div>
</section>`,
		imgHTML,
		inline(propStr(sec.Props, "masthead", "")),
		inline(propStr(sec.Props, "meta", "")),
		overlineHTML,
		titleHTML,
		subHTML,
		inline(tag),
	), nil
}

func renderBuilt(sec Section) string {
	heading, rest := splitHeading(sec.Body)
	chunks := splitByRule(rest)
	listMd, kicker := "", ""
	if len(chunks) > 0 {
		listMd = chunks[0]
	}
	if len(chunks) > 1 {
		kicker = chunks[1]
	}
	var items []string
	for _, b := range parseBlocks(listMd) {
		if b.Type == "ol" || b.Type == "ul" {
			items = b.Items
			break
		}
	}
	var rows strings.Builder
	for i, t := range items {
		fmt.Fprintf(&rows, `<div class="requirement"><div class="num">%s</div><div class="text">%s</div></div>`+"\n    ",
			romanOrN(i), inline(t))
	}
	kickerHTML := ""
	if kicker != "" {
		kickerHTML = fmt.Sprintf(`<p class="kicker">%s</p>`, inline(kicker))
	}
	return fmt.Sprintf(`<section class="built" data-screen-label="%s">
  %s
  <h2 class="section-h2">%s</h2>
  <div class="requirement-list">
    %s
  </div>
  %s
</section>`,
		propStr(sec.Props, "label", "03 Requirements"),
		sectionMarker(propStr(sec.Props, "num", "III."), propStr(sec.Props, "label", "Requirements")),
		inline(heading),
		strings.TrimSpace(rows.String()),
		kickerHTML,
	)
}

func renderPhotoMeta(sec Section) string {
	heading, rest := splitHeading(sec.Body)
	chunks := splitByRule(rest)
	listMd, kicker := "", ""
	if len(chunks) > 0 {
		listMd = chunks[0]
	}
	if len(chunks) > 1 {
		kicker = chunks[1]
	}
	var items []string
	for _, b := range parseBlocks(listMd) {
		if b.Type == "ol" || b.Type == "ul" {
			items = b.Items
			break
		}
	}
	var rows strings.Builder
	for _, t := range items {
		before, after, ok := strings.Cut(t, ": ")
		key, val := t, ""
		if ok {
			key = before
			val = after
		}
		fmt.Fprintf(&rows, `<div class="requirement"><div class="num">%s</div><div class="text">%s</div></div>`+"\n    ",
			esc(key), inline(val))
	}
	kickerHTML := ""
	if kicker != "" {
		kickerHTML = fmt.Sprintf(`<p class="kicker">%s</p>`, inline(kicker))
	}
	return fmt.Sprintf(`<section class="built photo-meta" data-screen-label="%s">
  %s
  <h2 class="section-h2">%s</h2>
  <div class="requirement-list">
    %s
  </div>
  %s
</section>`,
		propStr(sec.Props, "label", "Metadata"),
		sectionMarker(propStr(sec.Props, "num", "III."), propStr(sec.Props, "label", "Metadata")),
		inline(heading),
		strings.TrimSpace(rows.String()),
		kickerHTML,
	)
}

func renderDualPillars(sec Section) string {
	chunks := splitByRule(sec.Body)
	intro, left, right := "", "", ""
	if len(chunks) > 0 {
		intro = chunks[0]
	}
	if len(chunks) > 1 {
		left = chunks[1]
	}
	if len(chunks) > 2 {
		right = chunks[2]
	}
	heading, _ := splitHeading(intro)

	renderGroup := func(md string) (label string, items []string) {
		h, rest := splitHeading(md)
		for _, b := range parseBlocks(rest) {
			if b.Type == "ul" || b.Type == "ol" {
				items = b.Items
				break
			}
		}
		return h, items
	}
	lLabel, lItems := renderGroup(left)
	rLabel, rItems := renderGroup(right)

	pillar := func(label string, items []string) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "<h3>%s</h3>", inline(label))
		for _, it := range items {
			parts := strings.SplitN(it, " — ", 2)
			head, body := parts[0], ""
			if len(parts) > 1 {
				body = parts[1]
			}
			fmt.Fprintf(&sb, `<div class="pillar"><div class="head">%s</div><div class="body">%s</div></div>`,
				inline(head), inline(body))
		}
		return fmt.Sprintf(`<div class="pillar-group">%s</div>`, sb.String())
	}

	return fmt.Sprintf(`<section class="dual-pillars" data-screen-label="%s">
  %s
  <h2 class="section-h2" style="margin-bottom:4rem;">%s</h2>
  <div class="pillar-grid">%s%s</div>
</section>`,
		propStr(sec.Props, "label", "07 Pillars"),
		sectionMarker(propStr(sec.Props, "num", "VII."), propStr(sec.Props, "label", "Pillars")),
		inline(heading),
		pillar(lLabel, lItems),
		pillar(rLabel, rItems),
	)
}

func renderLandscape(sec Section) string {
	chunks := splitByRule(sec.Body)
	main, statBlock := "", ""
	if len(chunks) > 0 {
		main = chunks[0]
	}
	if len(chunks) > 1 {
		statBlock = chunks[1]
	}
	heading, rest := splitHeading(main)
	blocks := parseBlocks(rest)
	lead := ""
	var chips []string
	for _, b := range blocks {
		if b.Type == "p" && lead == "" {
			lead = b.Text
		}
		if b.Type == "ul" {
			chips = b.Items
		}
	}
	sm, _ := splitHeading(statBlock)
	caption := ""
	for l := range strings.SplitSeq(statBlock, "\n") {
		if strings.TrimSpace(l) != "" && !reHeadingLine.MatchString(l) {
			caption = strings.TrimSpace(l)
			break
		}
	}
	chipsHTML := ""
	if len(chips) > 0 {
		var sb strings.Builder
		sb.WriteString(`<div class="state-list">`)
		for _, c := range chips {
			fmt.Fprintf(&sb, `<span class="state-chip">%s</span>`, inline(c))
		}
		sb.WriteString("</div>")
		chipsHTML = sb.String()
	}
	sourceHTML := ""
	if src := propStr(sec.Props, "source", ""); src != "" {
		sourceHTML = fmt.Sprintf(`<div class="source">Source: %s</div>`, inline(src))
	}
	statNum := sm
	if override := propStr(sec.Props, "stat", ""); override != "" {
		statNum = override
	}
	return fmt.Sprintf(`<section class="landscape" data-screen-label="%s">
  %s
  <div class="body-grid">
    <div>
      <h2 class="section-h2">%s</h2>
      <p class="lead">%s</p>
      %s
    </div>
    <div class="big-stat">
      <div class="number">%s</div>
      <p class="caption">%s</p>
      %s
    </div>
  </div>
</section>`,
		propStr(sec.Props, "label", "02 Landscape"),
		sectionMarker(propStr(sec.Props, "num", "II."), propStr(sec.Props, "label", "Landscape")),
		inline(heading), inline(lead), chipsHTML,
		inline(statNum), inline(caption), sourceHTML,
	)
}

func renderHow(sec Section) string {
	heading, rest := splitHeading(sec.Body)
	steps := splitByRule(rest)
	romansHow := []string{"i", "ii", "iii", "iv", "v", "vi", "vii", "viii"}
	var stepHTMLs []string
	for idx, s := range steps {
		blocks := parseBlocks(s)
		label, stepH, desc := "", "", ""
		firstP := true
		for _, b := range blocks {
			if b.Type == "p" && firstP {
				label = b.Text
				firstP = false
			} else if b.Type == "h" {
				stepH = b.Text
			} else if b.Type == "p" {
				if desc != "" {
					desc += " "
				}
				desc += b.Text
			}
		}
		num := fmt.Sprintf("%d", idx+1)
		if idx < len(romansHow) {
			num = romansHow[idx]
		}
		stepHTMLs = append(stepHTMLs, fmt.Sprintf(
			`<div class="step"><div class="num">%s</div><div class="label">%s</div><h3>%s</h3><p>%s</p></div>`,
			num, inline(label), inline(stepH), inline(desc),
		))
	}
	return fmt.Sprintf(`<section class="how" data-screen-label="%s">
  %s
  <h2 class="section-h2" style="max-width:18ch;margin-bottom:4rem;">%s</h2>
  <div class="steps">
    %s
  </div>
</section>`,
		propStr(sec.Props, "label", "06 Journey"),
		sectionMarker(propStr(sec.Props, "num", "VI."), propStr(sec.Props, "label", "Journey")),
		inline(heading),
		strings.Join(stepHTMLs, "\n    "),
	)
}

func renderAnalogy(sec Section) string {
	chunks := splitByRule(sec.Body)
	main, scene := "", ""
	if len(chunks) > 0 {
		main = chunks[0]
	}
	if len(chunks) > 1 {
		scene = chunks[1]
	}
	heading, rest := splitHeading(main)
	var iconItems []string
	for _, b := range parseBlocks(rest) {
		if b.Type == "ul" {
			iconItems = b.Items
			break
		}
	}
	icons := []string{
		`<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.3"><rect x="12" y="8" width="24" height="32" rx="2"/></svg>`,
		`<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.3"><circle cx="24" cy="24" r="14"/></svg>`,
		`<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.3"><path d="M8 12 L40 12 L40 36 L8 36 Z"/></svg>`,
	}
	var iconRowParts []string
	for i, it := range iconItems {
		if i >= 3 {
			break
		}
		parts := strings.SplitN(it, " — ", 2)
		label, sub := parts[0], ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		iconRowParts = append(iconRowParts, fmt.Sprintf(
			`<div class="icon-item"><div class="icon">%s</div><div class="label">%s</div><div class="sub">%s</div></div>`,
			icons[i], inline(label), inline(sub),
		))
	}
	sceneBlocks := parseBlocks(scene)
	var sceneHTMLParts []string
	for _, b := range sceneBlocks {
		if b.Type == "p" {
			sceneHTMLParts = append(sceneHTMLParts, fmt.Sprintf(`<p class="scene">%s</p>`, inline(b.Text)))
		} else if b.Type == "code" {
			sceneHTMLParts = append(sceneHTMLParts, renderCode(b))
		}
	}
	return fmt.Sprintf(`<section class="analogy" data-screen-label="%s">
  %s
  <div class="body-grid">
    <div>
      <h2 class="section-h2">%s</h2>
      <div class="icon-row">
        %s
      </div>
    </div>
    <div>%s</div>
  </div>
</section>`,
		propStr(sec.Props, "label", "04 Analogy"),
		sectionMarker(propStr(sec.Props, "num", "IV."), propStr(sec.Props, "label", "Analogy")),
		inline(heading),
		strings.Join(iconRowParts, "\n        "),
		strings.Join(sceneHTMLParts, "\n    "),
	)
}

func renderObjections(sec Section) string {
	heading, rest := splitHeading(sec.Body)
	pairs := splitByRule(rest)
	var qaHTMLs []string
	for _, p := range pairs {
		blocks := parseBlocks(p)
		q, a := "", ""
		if len(blocks) > 0 {
			q = blocks[0].Text
		}
		var aParts []string
		for _, b := range blocks[1:] {
			aParts = append(aParts, b.Text)
		}
		a = strings.Join(aParts, " ")
		qaHTMLs = append(qaHTMLs, fmt.Sprintf(
			`<div class="qa"><div class="q">&ldquo;%s&rdquo;</div><div class="a">%s</div></div>`,
			inline(q), inline(a),
		))
	}
	return fmt.Sprintf(`<section class="objections" data-screen-label="%s">
  %s
  <h2 class="section-h2" style="max-width:20ch;margin-bottom:4rem;">%s</h2>
  <div class="qa-grid">
    %s
  </div>
</section>`,
		propStr(sec.Props, "label", "09 Objections"),
		sectionMarker(propStr(sec.Props, "num", "IX."), propStr(sec.Props, "label", "Objections")),
		inline(heading),
		strings.Join(qaHTMLs, "\n    "),
	)
}

func renderAction(sec Section) string {
	heading, rest := splitHeading(sec.Body)
	steps := splitByRule(rest)
	markers := []string{"Step One", "Step Two", "Step Three", "Step Four"}
	var stepHTMLs []string
	for idx, s := range steps {
		blocks := parseBlocks(s)
		stepH, desc := "", ""
		for _, b := range blocks {
			if b.Type == "h" {
				stepH = b.Text
			} else if b.Type == "p" {
				if desc != "" {
					desc += " "
				}
				desc += b.Text
			}
		}
		marker := fmt.Sprintf("Step %d", idx+1)
		if idx < len(markers) {
			marker = markers[idx]
		}
		stepHTMLs = append(stepHTMLs, fmt.Sprintf(
			`<div class="action-step"><div class="marker">%s</div><h3>%s</h3><p>%s</p></div>`,
			marker, inline(stepH), inline(desc),
		))
	}
	return fmt.Sprintf(`<section class="action" data-screen-label="%s">
  %s
  <h2 class="section-h2" style="margin-bottom:4rem;">%s</h2>
  <div class="action-steps">
    %s
  </div>
</section>`,
		propStr(sec.Props, "label", "11 Action"),
		sectionMarker(propStr(sec.Props, "num", "XI."), propStr(sec.Props, "label", "Action")),
		inline(heading),
		strings.Join(stepHTMLs, "\n    "),
	)
}

func renderFalseChoice(sec Section) string {
	chunks := splitByRule(sec.Body)
	intro, left, right, conclusion := "", "", "", ""
	if len(chunks) > 0 {
		intro = chunks[0]
	}
	if len(chunks) > 1 {
		left = chunks[1]
	}
	if len(chunks) > 2 {
		right = chunks[2]
	}
	if len(chunks) > 3 {
		conclusion = chunks[3]
	}
	h, _ := splitHeading(intro)

	parseSide := func(md string) (label, sub, content string) {
		a, rest1 := splitHeading(md)
		b, rest2 := splitHeading(rest1)
		var parts []string
		for _, bl := range parseBlocks(rest2) {
			if bl.Type == "p" {
				parts = append(parts, fmt.Sprintf("<p>%s</p>", inline(bl.Text)))
			} else if bl.Type == "code" {
				parts = append(parts, renderCode(bl))
			}
		}
		return a, b, strings.Join(parts, "\n")
	}
	lLabel, lSub, lContent := parseSide(left)
	rLabel, rSub, rContent := parseSide(right)

	conclusionHTML := ""
	if conclusion != "" {
		conclusionHTML = fmt.Sprintf(`<p class="conclusion">%s</p>`, inline(conclusion))
	}
	return fmt.Sprintf(`<section class="false-choice" data-screen-label="%s">
  %s
  <h2 class="section-h2" style="max-width:15ch;margin-bottom:4rem;">%s</h2>
  <div class="two-column">
    <div class="column"><div class="column-header">%s</div><h3>%s</h3>%s</div>
    <div class="vs-divider"><div class="line"></div><div class="mark">versus</div><div class="line"></div></div>
    <div class="column"><div class="column-header">%s</div><h3>%s</h3>%s</div>
  </div>
  %s
</section>`,
		propStr(sec.Props, "label", "01 Premise"),
		sectionMarker(propStr(sec.Props, "num", "I."), propStr(sec.Props, "label", "The Premise")),
		inline(h),
		inline(lLabel), inline(lSub), lContent,
		inline(rLabel), inline(rSub), rContent,
		conclusionHTML,
	)
}

func renderComparison(sec Section) string {
	heading, rest := splitHeading(sec.Body)
	var rows [][]string
	for l := range strings.SplitSeq(rest, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "|") {
			continue
		}
		if regexp.MustCompile(`^\|\s*-+`).MatchString(l) {
			continue
		}
		l = strings.TrimPrefix(strings.TrimSuffix(l, "|"), "|")
		var cells []string
		for c := range strings.SplitSeq(l, "|") {
			cells = append(cells, strings.TrimSpace(c))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	header := rows[0]
	dataRows := rows[1:]
	oursIdx := len(header) - 1

	renderCell := func(i int, val, headLabel string) string {
		switch i {
		case 0:
			return fmt.Sprintf(`<div class="feature">%s</div>`, inline(val))
		case oursIdx:
			return fmt.Sprintf(`<div class="val good" data-label="%s">%s</div>`, inline(headLabel), inline(val))
		default:
			return fmt.Sprintf(`<div class="val bad" data-label="%s">%s</div>`, inline(headLabel), inline(val))
		}
	}

	var headerCells []string
	for i, h := range header {
		if i == 0 {
			headerCells = append(headerCells, fmt.Sprintf(`<div class="feature">%s</div>`, inline(h)))
		} else if i == oursIdx {
			headerCells = append(headerCells, fmt.Sprintf(`<div class="ours">%s</div>`, inline(h)))
		} else {
			headerCells = append(headerCells, fmt.Sprintf(`<div>%s</div>`, inline(h)))
		}
	}

	var dataHTMLs []string
	for _, row := range dataRows {
		var cells []string
		for i, v := range row {
			hl := ""
			if i < len(header) {
				hl = header[i]
			}
			cells = append(cells, renderCell(i, v, hl))
		}
		dataHTMLs = append(dataHTMLs, fmt.Sprintf(`<div class="comp-row">
      %s
    </div>`, strings.Join(cells, "")))
	}

	return fmt.Sprintf(`<section class="comparison" data-screen-label="%s">
  %s
  <h2 class="section-h2" style="margin-bottom:4rem;max-width:24ch;">%s</h2>
  <div class="comparison-table">
    <div class="comp-row header">
      %s
    </div>
    %s
  </div>
</section>`,
		propStr(sec.Props, "label", "08 Comparison"),
		sectionMarker(propStr(sec.Props, "num", "VIII."), propStr(sec.Props, "label", "Against the Alternatives")),
		inline(heading),
		strings.Join(headerCells, ""),
		strings.Join(dataHTMLs, "\n    "),
	)
}

func renderClose(sec Section) string {
	chunks := splitByRule(sec.Body)
	quote, post := "", ""
	if len(chunks) > 0 {
		quote = chunks[0]
	}
	if len(chunks) > 1 {
		post = chunks[1]
	}
	postHTML := ""
	if post != "" {
		postHTML = fmt.Sprintf(`<p class="post">%s</p>`, inline(post))
	}
	return fmt.Sprintf(`<section class="close" data-screen-label="%s">
  %s
  <div class="close-content">
    <blockquote>%s</blockquote>
    %s
  </div>
  <div class="close-footer"><div class="mark">%s</div><div class="meta">%s</div></div>
</section>`,
		propStr(sec.Props, "label", "12 Close"),
		sectionMarker(propStr(sec.Props, "num", "XII."), propStr(sec.Props, "label", "In Closing")),
		inline(quote),
		postHTML,
		inline(propStr(sec.Props, "mark", "")),
		inline(propStr(sec.Props, "meta", "")),
	)
}

func renderProposal(sec Section) string {
	chunks := splitByRule(sec.Body)
	intro, cardBlock, ann := "", "", ""
	if len(chunks) > 0 {
		intro = chunks[0]
	}
	if len(chunks) > 1 {
		cardBlock = chunks[1]
	}
	if len(chunks) > 2 {
		ann = chunks[2]
	}
	heading, rest := splitHeading(intro)
	lede := ""
	for _, b := range parseBlocks(rest) {
		if b.Type == "p" {
			lede = b.Text
			break
		}
	}

	cbLines := strings.Split(cardBlock, "\n")
	headerLine := strings.Split(cbLines[0], "|")
	cardMark, cardTier := "", ""
	if len(headerLine) > 0 {
		cardMark = strings.TrimSpace(headerLine[0])
	}
	if len(headerLine) > 1 {
		cardTier = strings.TrimSpace(headerLine[1])
	}
	cbBlocks := parseBlocks(strings.Join(cbLines[1:], "\n"))
	cardTitle, cardSubtitle := "", ""
	var panelItems []string
	for _, b := range cbBlocks {
		if b.Type == "h" && b.Level == 3 && cardTitle == "" {
			cardTitle = b.Text
		} else if b.Type == "p" && cardSubtitle == "" {
			cardSubtitle = b.Text
		} else if b.Type == "ol" || b.Type == "ul" {
			panelItems = b.Items
		}
	}
	var panels []string
	for i, it := range panelItems {
		parts := strings.SplitN(it, "|", 2)
		code := "████ · ████ · ████"
		status := "Sealed"
		if len(parts) > 0 {
			code = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			status = strings.TrimSpace(parts[1])
		}
		hiddenClass := "hidden"
		if regexp.MustCompile(`(?i)spent|used`).MatchString(status) {
			hiddenClass = "used"
		}
		panels = append(panels, fmt.Sprintf(
			`<div class="scratch-panel %s"><span class="scratch-num">%02d</span><span class="scratch-code">%s</span><span class="scratch-status">%s</span></div>`,
			hiddenClass, i+1, inline(code), inline(status),
		))
	}
	var annotations []string
	for _, b := range parseBlocks(ann) {
		if b.Type == "ul" || b.Type == "ol" {
			for _, it := range b.Items {
				parts := strings.SplitN(it, " — ", 2)
				label, content := parts[0], ""
				if len(parts) > 1 {
					content = parts[1]
				}
				annotations = append(annotations, fmt.Sprintf(
					`<div class="annotation"><div class="label">%s</div><div class="content">%s</div></div>`,
					inline(label), inline(content),
				))
			}
		}
	}
	return fmt.Sprintf(`<section class="proposal" data-screen-label="%s">
  %s
  <div class="title-block">
    <h2 class="section-h2" style="font-size:clamp(3rem,8vw,6.5rem);line-height:0.95;letter-spacing:-0.04em;margin-bottom:2.5rem;">%s</h2>
    <p class="lede">%s</p>
  </div>
  <div class="card-display">
    <div class="verification-card">
      <div class="card-header"><div class="card-mark">%s</div><div class="card-tier">%s</div></div>
      <h3 class="card-title">%s</h3>
      <p class="card-subtitle">%s</p>
      <div class="scratch-stack">
          %s
      </div>
      <div class="card-footer"><div class="card-activation">%s<br>%s</div><div class="card-specimen">SPECIMEN</div></div>
    </div>
    <div class="card-annotations">
      %s
    </div>
  </div>
</section>`,
		propStr(sec.Props, "label", "05 The Proposal"),
		sectionMarker(propStr(sec.Props, "num", "V."), propStr(sec.Props, "label", "The Proposal")),
		inline(heading), inline(lede),
		inline(cardMark), inline(cardTier),
		inline(cardTitle), inline(cardSubtitle),
		strings.Join(panels, "\n          "),
		inline(propStr(sec.Props, "footer1", "")),
		inline(propStr(sec.Props, "footer2", "")),
		strings.Join(annotations, "\n      "),
	)
}

// ── main router ───────────────────────────────────────────────────────────

func renderSection(sec Section) (string, error) {
	switch sec.Type {
	case "hero":
		return renderHero(sec)
	case "built":
		return renderBuilt(sec), nil
	case "photo-meta":
		return renderPhotoMeta(sec), nil
	case "dual-pillars":
		return renderDualPillars(sec), nil
	case "landscape":
		return renderLandscape(sec), nil
	case "how":
		return renderHow(sec), nil
	case "analogy":
		return renderAnalogy(sec), nil
	case "objections":
		return renderObjections(sec), nil
	case "why-not":
		return proseRender("why-not", "10 Obstacle", "X.", "Obstacle", sec.Props, sec.Body), nil
	case "prose":
		return proseRender("prose", "Notes", "V.", "Notes", sec.Props, sec.Body), nil
	case "action":
		return renderAction(sec), nil
	case "false-choice":
		return renderFalseChoice(sec), nil
	case "comparison":
		return renderComparison(sec), nil
	case "close":
		return renderClose(sec), nil
	case "proposal":
		return renderProposal(sec), nil
	default:
		return fmt.Sprintf("<section class=%q>\n%s\n</section>", sec.Type, renderBody(sec.Body, "")), nil
	}
}
