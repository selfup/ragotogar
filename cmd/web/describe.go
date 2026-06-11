package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"ragotogar/library"
)

// Defaults mirror cmd/describe's config literals so the form's initial
// state matches what a bare CLI invocation would do.
const (
	defaultPreviewWorkers   = 4
	defaultInferenceWorkers = 1
	defaultRetries          = 3
)

// describePageData feeds the describe-section template. Numeric fields
// are pre-formatted strings for the input value attributes, same
// convention as pageData's threshold/weight fields.
type describePageData struct {
	Active string // sidebar highlight — always "describe" here

	Dir              string
	Model            string
	ClassifyModel    string
	PreviewWorkers   string
	InferenceWorkers string
	Retries          string
	Force            bool
	DryRun           bool
	Classify         bool

	// DSN is the masked library DSN, display-only — the describer
	// inherits the same library cmd/web is serving.
	DSN string
	// Command is the composed scripts/photo_describe.sh invocation.
	// Empty until a directory is submitted. While the section is a
	// scaffold, this preview IS the output — execution isn't wired.
	Command string
	// DirMissing is true when the form was submitted without a
	// directory (the `required` attribute blocks that in the browser,
	// but URL fiddling / old bookmarks can still arrive with dir=).
	// Renders an explicit note instead of silently doing nothing.
	DirMissing bool
}

// describeParams is the resolved form state buildDescribeCommand
// consumes. Worker counts arrive already clamped by parseCount.
type describeParams struct {
	dir              string
	model            string
	classifyModel    string
	previewWorkers   int
	inferenceWorkers int
	retries          int
	force            bool
	dryRun           bool
	classify         bool
}

// defaultVisionModel mirrors cmd/describe's -model default.
func defaultVisionModel() string {
	return envOr("LM_MODEL", "qwen/qwen3-vl-8b")
}

// parseCount reads an integer URL param and clamps it into [min, max].
// Missing or malformed input falls back (same posture as parseThreshold:
// URL fiddling can't push worker counts into nonsense territory).
func parseCount(raw string, fallback, min, max int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// shellQuote single-quotes s when it contains anything the shell would
// interpret, so the previewed command is copy-paste safe for paths with
// spaces (e.g. "/Volumes/My Drive/JPEG").
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// A leading ~/ must stay outside the quotes — '~/x' is passed
	// literally (no home expansion) and the describer would see a
	// nonexistent path. ~/'x' expands fine.
	if rest, ok := strings.CutPrefix(s, "~/"); ok && rest != "" {
		return "~/" + shellQuote(rest)
	}
	if !strings.ContainsAny(s, " \t'\"\\$`!*?[](){}<>;&|~#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildDescribeCommand composes the scripts/photo_describe.sh invocation
// the form state describes. Flags are always explicit (even at their
// defaults) so the preview is unambiguous about what would run.
func buildDescribeCommand(p describeParams) string {
	parts := []string{"./scripts/photo_describe.sh"}
	if p.force {
		parts = append(parts, "-force")
	}
	if p.dryRun {
		parts = append(parts, "-dry-run")
	}
	parts = append(parts,
		"-model", shellQuote(p.model),
		"-preview-workers", strconv.Itoa(p.previewWorkers),
		"-inference-workers", strconv.Itoa(p.inferenceWorkers),
		"-retries", strconv.Itoa(p.retries),
	)
	if p.classify {
		parts = append(parts, "-classify", "-classify-model", shellQuote(p.classifyModel))
	}
	parts = append(parts, shellQuote(p.dir))
	return strings.Join(parts, " ")
}

// serveDescribe renders the describe section. GET-only state in URL
// params (like the search page) so a tuned form is shareable/bookmarkable.
func serveDescribe(w http.ResponseWriter, r *http.Request, tmpl *template.Template, dsn string) {
	q := r.URL.Query()
	dir := strings.TrimSpace(q.Get("dir"))
	model := strings.TrimSpace(q.Get("model"))
	if model == "" {
		model = defaultVisionModel()
	}
	classifyModel := strings.TrimSpace(q.Get("classify_model"))
	if classifyModel == "" {
		classifyModel = library.ClassifyModel()
	}
	p := describeParams{
		dir:              dir,
		model:            model,
		classifyModel:    classifyModel,
		previewWorkers:   parseCount(q.Get("pw"), defaultPreviewWorkers, 1, 64),
		inferenceWorkers: parseCount(q.Get("iw"), defaultInferenceWorkers, 1, 32),
		retries:          parseCount(q.Get("retries"), defaultRetries, 0, 10),
		force:            q.Get("force") == "1",
		dryRun:           q.Get("dry") == "1",
		classify:         q.Get("class") == "1",
	}

	var command string
	if dir != "" {
		command = buildDescribeCommand(p)
	}
	// First page load has no query params at all; a submit always
	// carries them. Submitted-but-empty gets called out explicitly.
	dirMissing := dir == "" && len(q) > 0

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, describePageData{
		Active:           "describe",
		Dir:              dir,
		Model:            model,
		ClassifyModel:    classifyModel,
		PreviewWorkers:   strconv.Itoa(p.previewWorkers),
		InferenceWorkers: strconv.Itoa(p.inferenceWorkers),
		Retries:          strconv.Itoa(p.retries),
		Force:            p.force,
		DryRun:           p.dryRun,
		Classify:         p.classify,
		DSN:              library.MaskDSN(dsn),
		Command:          command,
		DirMissing:       dirMissing,
	}); err != nil {
		log.Printf("template: %v", err)
	}
}
