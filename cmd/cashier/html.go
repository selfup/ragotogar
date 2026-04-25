package main

import (
	"fmt"
	"regexp"
	"strings"
)

var reFrontmatter = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n`)
var reMetaLine = regexp.MustCompile(`^(\w+):\s*(.*)$`)

func buildHTML(md, stylesCSS string) (string, error) {
	body := md
	title := "Untitled"

	if fm := reFrontmatter.FindStringSubmatch(md); fm != nil {
		body = md[len(fm[0]):]
		for l := range strings.SplitSeq(fm[1], "\n") {
			if m := reMetaLine.FindStringSubmatch(l); m != nil && m[1] == "title" {
				title = strings.Trim(m[2], `"'`)
			}
		}
	}

	_, sections := parseSections(body)
	var htmlParts []string
	for _, sec := range sections {
		h, err := renderSection(sec)
		if err != nil {
			return "", fmt.Errorf("render section %q: %w", sec.Type, err)
		}
		htmlParts = append(htmlParts, h)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:ital,opsz,wght,SOFT@0,9..144,200..900,0..100;1,9..144,200..900,0..100&family=Newsreader:ital,opsz,wght@0,6..72,300..700;1,6..72,300..700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
%s
</style>
</head>
<body>
<main>
%s
</main>
<script>
const io = new IntersectionObserver((entries) => {
  entries.forEach(e => { if (e.isIntersecting) { e.target.classList.add('visible'); io.unobserve(e.target); } });
}, { threshold: 0.08, rootMargin: '0px 0px -50px 0px' });
document.querySelectorAll('.reveal').forEach(el => io.observe(el));
</script>
</body>
</html>`, title, stylesCSS, strings.Join(htmlParts, "\n\n")), nil
}
