package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ragotogar/library"
)

var supportedExts = map[string]bool{
	"jpg": true, "jpeg": true, "png": true,
	"tif": true, "tiff": true,
	"nef": true, "raf": true, "arw": true,
	"cr2": true, "cr3": true, "dng": true,
	"orf": true, "rw2": true,
}

var thinkBlockRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

type config struct {
	inputDir         string
	inputFile        string // set when a single file is passed instead of a directory
	dsn              string
	force            bool
	dryRun           bool
	initOnly         bool
	classify         bool // pipeline mode: classify each photo right after it's described
	lmBase           string
	model            string
	classifyModel    string
	resizePx         int
	jpegQuality      int
	maxRetries       int
	retryDelay       time.Duration
	previewWorkers   int
	inferenceWorkers int
}

// LM Studio chat completion request/response types.
type chatRequest struct {
	Model    string        `json:"model"`
	User     string        `json:"user"`
	Messages []chatMessage `json:"messages"`
	MaxToks  int           `json:"max_tokens"`
	Temp     float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	cfg := config{
		lmBase:           library.VisionEndpoint(), // VISION_ENDPOINT > LM_STUDIO_BASE > localhost
		model:            envOr("LM_MODEL", "qwen/qwen3-vl-8b"),
		resizePx:         envOrInt("RESIZE_PX", 1024),
		jpegQuality:      envOrInt("JPEG_QUALITY", 85),
		maxRetries:       3,
		retryDelay:       5 * time.Second,
		previewWorkers:   4,
		inferenceWorkers: 1,
	}

	flag.StringVar(&cfg.dsn, "dsn", defaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
	flag.BoolVar(&cfg.force, "force", false, "Re-describe photos already in the DB (UPSERT on the photos.name conflict)")
	flag.BoolVar(&cfg.initOnly, "init-only", false, "Open the DB, apply the schema, and exit (used by scripts/bootstrap.sh)")
	flag.StringVar(&cfg.model, "model", cfg.model, "LM Studio model name")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "List files that would be processed without calling the LLM")
	flag.IntVar(&cfg.maxRetries, "retries", cfg.maxRetries, "Max retry attempts per image on API failure")
	flag.IntVar(&cfg.previewWorkers, "preview-workers", cfg.previewWorkers, "Parallel preview (resize/extract) workers")
	flag.IntVar(&cfg.inferenceWorkers, "inference-workers", cfg.inferenceWorkers, "Parallel LLM inference workers (default 1; bump to N to use LM Studio's --parallel N batching)")
	flag.BoolVar(&cfg.classify, "classify", false, "Pipeline mode: classify each photo right after describing it. Failures are logged, not fatal — re-run cmd/classify to retry.")
	flag.StringVar(&cfg.classifyModel, "classify-model", library.ClassifyModel(), "LM Studio model for the inline classifier (only used with -classify)")
	flag.Parse()

	if cfg.initOnly {
		if err := initOnly(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <input_dir|image_file>\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}
	input := flag.Arg(0)
	info, err := os.Stat(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: '%s': %v\n", input, err)
		os.Exit(1)
	}
	if info.IsDir() {
		cfg.inputDir = input
	} else {
		cfg.inputFile = input
		cfg.inputDir = filepath.Dir(input)
	}

	if err := checkDeps(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// initOnly opens the DB to apply the schema (CREATE TABLE IF NOT EXISTS),
// then closes it. Used by scripts/bootstrap.sh so a fresh checkout has all
// tables ready before the Python tools or cmd/web touch the library.
func initOnly(cfg config) error {
	db, err := openDB(cfg.dsn)
	if err != nil {
		return fmt.Errorf("open library: %w", err)
	}
	defer db.Close()
	fmt.Printf("Schema applied to %s\n", library.MaskDSN(cfg.dsn))
	return nil
}

func run(cfg config) error {
	var files []string
	if cfg.inputFile != "" {
		files = []string{cfg.inputFile}
	} else {
		var err error
		files, err = collectFiles(cfg.inputDir, 3)
		if err != nil {
			return err
		}
	}
	if len(files) == 0 {
		fmt.Printf("No image files found in '%s'\n", cfg.inputDir)
		return nil
	}

	fmt.Printf("Found %d image(s) in '%s'\n", len(files), cfg.inputDir)
	fmt.Printf("Library: %s\n", library.MaskDSN(cfg.dsn))
	fmt.Printf("Model:   %s @ %s\n", cfg.model, cfg.lmBase)
	fmt.Printf("Retries: %d (delay %s)\n", cfg.maxRetries, cfg.retryDelay)
	fmt.Printf("Preview workers: %d\n", cfg.previewWorkers)
	fmt.Printf("Inference workers: %d\n\n", cfg.inferenceWorkers)

	if cfg.dryRun {
		for _, f := range files {
			fmt.Printf("  [dry-run] %s\n", f)
		}
		fmt.Printf("\n%d files would be processed.\n", len(files))
		return nil
	}

	db, err := openDB(cfg.dsn)
	if err != nil {
		return fmt.Errorf("open library: %w", err)
	}
	defer db.Close()

	existing, err := listExistingNames(db)
	if err != nil {
		return fmt.Errorf("list existing: %w", err)
	}

	magickCmd := findMagick()

	type job struct {
		file     string
		exif     exifData
		safeName string
	}
	type previewResult struct {
		bytes    []byte
		duration time.Duration
		err      error
	}

	// Build job list up front, applying skip-exists serially so we don't
	// spawn preview workers for photos we won't process.
	var jobs []job
	var skipped int
	for _, file := range files {
		exif := extractEXIF(file)
		safeName := safeOutputName(cfg.inputDir, file, exif)
		if !cfg.force && existing[safeName] {
			fmt.Printf("  [skip] %s (already in DB)\n", safeName)
			skipped++
			continue
		}
		jobs = append(jobs, job{file: file, exif: exif, safeName: safeName})
	}

	if len(jobs) == 0 {
		fmt.Printf("\nDone. Processed: 0, Errors: 0, Skipped: %d\n", skipped)
		fmt.Printf("Library: %s\n", library.MaskDSN(cfg.dsn))
		return nil
	}

	// Per-job result slot; buffered so a finished worker can drop its
	// result and move on without waiting for the (serial) consumer.
	previews := make([]chan previewResult, len(jobs))
	for i := range previews {
		previews[i] = make(chan previewResult, 1)
	}

	workers := cfg.previewWorkers
	if workers < 1 {
		workers = 1
	}
	jobCh := make(chan int, len(jobs))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Go(func() {
			for idx := range jobCh {
				start := time.Now()
				bytes, err := makePreviewBytes(magickCmd, jobs[idx].file, cfg.resizePx, cfg.jpegQuality)
				previews[idx] <- previewResult{
					bytes:    bytes,
					duration: time.Since(start),
					err:      err,
				}
			}
		})
	}
	for i := range jobs {
		jobCh <- i
	}
	close(jobCh)

	// Inference worker pool. Workers consume job indices, wait for the
	// preview channel for that index, then call the LLM and write output.
	// Logs include the completion ordinal (atomic) since workers finish out
	// of order at inferenceWorkers > 1.
	inferWorkers := max(cfg.inferenceWorkers, 1)
	inferCh := make(chan int, len(jobs))
	var processed, errors atomic.Int64
	var done atomic.Int64
	var inferWg sync.WaitGroup
	for w := 0; w < inferWorkers; w++ {
		inferWg.Add(1)
		go func() {
			defer inferWg.Done()
			for idx := range inferCh {
				j := jobs[idx]
				res := <-previews[idx]
				ord := done.Add(1)
				if res.err != nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n    !! Preview failed: %v, skipping\n",
						ord, len(jobs), j.safeName, res.err)
					errors.Add(1)
					continue
				}

				b64 := base64.StdEncoding.EncodeToString(res.bytes)
				inferenceStart := time.Now()
				description, err := describeWithRetry(cfg, b64, exifToPromptString(j.exif))
				inferenceElapsed := time.Since(inferenceStart)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n    !! Vision API failed after %d attempts: %v\n",
						ord, len(jobs), j.safeName, cfg.maxRetries, err)
					errors.Add(1)
					continue
				}

				fmt.Printf("  [%d/%d] %s (preview %s, inference %s)\n",
					ord, len(jobs), j.safeName,
					res.duration.Round(time.Millisecond),
					inferenceElapsed.Round(time.Millisecond))

				fields := parseDescriptionFields(description)
				if err := insertPhoto(
					db, j.safeName, j.file, j.exif, description, fields,
					res.bytes, cfg.model,
					res.duration.Milliseconds(), inferenceElapsed.Milliseconds(),
				); err != nil {
					fmt.Fprintf(os.Stderr, "    !! DB write failed: %v\n", err)
					errors.Add(1)
					continue
				}

				// Pipeline-mode classification: run the small text classifier
				// against the prose we just wrote, in this same worker. This
				// fills the describer's tail-end idle window with classify
				// work for the next photo, no LM Studio model contention
				// because vision and text inference happen sequentially within
				// the worker. Failures are logged and don't roll back the row
				// (re-run cmd/classify standalone to retry).
				if cfg.classify {
					classifyStart := time.Now()
					if err := library.ClassifyOne(context.Background(), db, j.safeName, cfg.classifyModel); err != nil {
						fmt.Fprintf(os.Stderr, "    !! classify failed for %s: %v\n", j.safeName, err)
					} else {
						fmt.Printf("    classify %s\n", time.Since(classifyStart).Round(time.Millisecond))
					}
				}

				processed.Add(1)
			}
		}()
	}
	for i := range jobs {
		inferCh <- i
	}
	close(inferCh)
	inferWg.Wait()
	wg.Wait()

	fmt.Printf("\nDone. Processed: %d, Errors: %d, Skipped: %d\n", processed.Load(), errors.Load(), skipped)
	fmt.Printf("Library: %s\n", library.MaskDSN(cfg.dsn))
	return nil
}

func describeWithRetry(cfg config, b64, exif string) (string, error) {
	var lastErr error
	for attempt := range cfg.maxRetries {
		desc, err := describeImage(cfg, b64, exif)
		if err == nil && desc != "" {
			return desc, nil
		}
		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("empty response")
		}
		if attempt < cfg.maxRetries-1 {
			// Exponential backoff with jitter: base * 2^attempt + random jitter
			backoff := cfg.retryDelay * time.Duration(1<<uint(attempt))
			jitter := time.Duration(rand.Int64N(int64(cfg.retryDelay)))
			wait := backoff + jitter
			fmt.Fprintf(os.Stderr, "    !! Attempt %d failed (%v), retrying in %s...\n", attempt+1, lastErr, wait.Round(time.Second))
			time.Sleep(wait)
		}
	}
	return "", fmt.Errorf("all %d attempts failed: %w", cfg.maxRetries, lastErr)
}

func describeImage(cfg config, b64, exif string) (string, error) {
	prompt := "Before describing, check if the scene contains many similar or repeating elements (rows of people, identical chairs, parked cars, etc). If so, keep it simple — state the count and describe the group once. Never repeat the same sentence or description for each individual item.\n\nDescribe exactly what is visible in this photograph. Be concrete and specific — name objects, colors, materials, positions, and spatial relationships. Do NOT use subjective or interpretive language like 'intimate', 'captures', 'suggests', or 'evokes'. Just state what you see.\n\nFormat:\n- Subject: what/who is in the frame\n- Setting: specific location type, surfaces, objects; explicitly note indoor or outdoor\n- Light: direction, color, source (window/lamp/sun/etc); time of day suggested; weather (clear, overcast, rain, snow, fog)\n- Colors: dominant palette\n- Composition: framing, camera angle (eye level, looking up, looking down), depth of field, distance to the main subject\n- Vantage: where the camera is physically located. Is the photographer on the ground, elevated (balcony, rooftop, hill), inside a vehicle, inside a plane, on a drone, or shooting through a window/foliage/fence? If from a plane, is it on the ground or in flight? If from a vehicle, moving or stopped? Use 'unclear' if there is no evidence either way.\n- Ground truth: how many people are visible (none / one / two / a few / a group / a crowd); how many animals; is anything in motion (subject moving, camera moving, both, or static).\n- Condition: physical state visible in the frame — under construction (scaffolding, exposed framing, partial walls, work in progress), worn (visible damage, rust, peeling paint, weathering), aged or old, new or recent, pristine or well-maintained, clean, dirty, cluttered, abandoned, freshly renovated. State what you actually see; use 'unclear' if condition isn't legible from the photo.\n\nCamera metadata for context:\n" + exif

	sessionID := fmt.Sprintf("photo-describe-%d-%d", time.Now().UnixNano(), rand.Int64())

	req := chatRequest{
		Model: cfg.model,
		User:  sessionID,
		Messages: []chatMessage{
			{
				Role: "user",
				Content: []contentPart{
					{
						Type:     "image_url",
						ImageURL: &imageURL{URL: "data:image/jpeg;base64," + b64},
					},
					{
						Type: "text",
						Text: prompt,
					},
				},
			},
		},
		MaxToks: 16384,
		Temp:    0.3,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", cfg.lmBase+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+library.LLMAPIKey())

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	choice := chatResp.Choices[0]
	content := choice.Message.Content
	// Strip <think> blocks from reasoning models
	content = thinkBlockRe.ReplaceAllString(content, "")
	content = strings.TrimSpace(content)

	if content == "" && choice.Message.ReasoningContent != "" {
		return "", fmt.Errorf("model exhausted tokens on reasoning (%d chars), no content produced (finish_reason=%s)",
			len(choice.Message.ReasoningContent), choice.FinishReason)
	}

	if sentence, count := detectRepetitionLoop(content, 20, 5); count > 0 {
		return "", fmt.Errorf("repetition loop detected: %q repeated %d times", truncate(sentence, 80), count)
	}

	return content, nil
}

// detectRepetitionLoop checks whether any sentence (split on ". ") of at least
// minLen characters appears more than maxRepeats times. Returns the offending
// sentence and its count, or ("", 0) if no loop is detected.
func detectRepetitionLoop(text string, minLen, maxRepeats int) (string, int) {
	sentences := strings.Split(text, ". ")
	counts := make(map[string]int)
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < minLen {
			continue
		}
		counts[s]++
	}
	var worst string
	var worstCount int
	for s, c := range counts {
		if c > maxRepeats && c > worstCount {
			worst = s
			worstCount = c
		}
	}
	return worst, worstCount
}

func collectFiles(dir string, maxDepth int) ([]string, error) {
	var files []string
	root := filepath.Clean(dir)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			depth := 0
			if rel != "." {
				depth = strings.Count(rel, string(filepath.Separator)) + 1
			}
			if depth > maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, "._") || name == ".DS_Store" {
			return nil
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		if supportedExts[ext] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// safeOutputName builds a globally unique name from EXIF: date_model_filename.
// e.g. "20250928_X100VI_DSCF1516". Falls back to path-based naming if EXIF is missing.
func safeOutputName(inputDir, filePath string, exif exifData) string {
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	// Parse date from "2025:09:28 16:38:17" -> "20250928"
	date := ""
	if exif.DateTimeOriginal != "" {
		parts := strings.Fields(exif.DateTimeOriginal)
		if len(parts) >= 1 {
			date = strings.ReplaceAll(parts[0], ":", "")
		}
	}

	model := strings.ReplaceAll(exif.Model, " ", "")

	if date != "" && model != "" {
		return date + "_" + model + "_" + base
	}

	// Fallback: use relative path
	rel, err := filepath.Rel(inputDir, filePath)
	if err != nil {
		return base
	}
	noExt := strings.TrimSuffix(rel, filepath.Ext(rel))
	return strings.ReplaceAll(noExt, string(filepath.Separator), "__")
}

type exifData struct {
	FileName             string `json:"file_name"`
	DateTimeOriginal     string `json:"date_time_original"`
	Make                 string `json:"make"`
	Model                string `json:"model"`
	LensModel            string `json:"lens_model"`
	LensInfo             string `json:"lens_info"`
	FocalLength          string `json:"focal_length"`
	FocalLengthIn35mm    string `json:"focal_length_in_35mm"`
	FNumber              string `json:"f_number"`
	ExposureTime         string `json:"exposure_time"`
	ISO                  string `json:"iso"`
	ExposureCompensation string `json:"exposure_compensation"`
	WhiteBalance         string `json:"white_balance"`
	MeteringMode         string `json:"metering_mode"`
	ExposureMode         string `json:"exposure_mode"`
	Flash                string `json:"flash"`
	ImageWidth           string `json:"image_width"`
	ImageHeight          string `json:"image_height"`
	GPSLatitude          string `json:"gps_latitude,omitempty"`
	GPSLongitude         string `json:"gps_longitude,omitempty"`
	Artist               string `json:"artist,omitempty"`
	Copyright            string `json:"copyright,omitempty"`
	Software             string `json:"software,omitempty"`
}

func extractEXIF(file string) exifData {
	cmd := exec.Command("exiftool", "-json", "-f",
		"-FileName",
		"-DateTimeOriginal",
		"-Make", "-Model",
		"-LensModel", "-LensInfo",
		"-FocalLength", "-FocalLengthIn35mmFormat",
		"-FNumber", "-ExposureTime", "-ISO",
		"-ExposureCompensation",
		"-WhiteBalance",
		"-MeteringMode", "-ExposureMode",
		"-Flash",
		"-ImageWidth", "-ImageHeight",
		"-GPSLatitude", "-GPSLongitude",
		"-Artist", "-Copyright",
		"-Software",
		file,
	)
	out, err := cmd.Output()
	if err != nil {
		return exifData{}
	}
	// exiftool -json returns an array of objects
	var raw []map[string]any
	if err := json.Unmarshal(out, &raw); err != nil || len(raw) == 0 {
		return exifData{}
	}
	m := raw[0]
	str := func(key string) string {
		v, ok := m[key]
		if !ok {
			return ""
		}
		s := fmt.Sprintf("%v", v)
		if s == "-" {
			return ""
		}
		return s
	}
	return exifData{
		FileName:             str("FileName"),
		DateTimeOriginal:     str("DateTimeOriginal"),
		Make:                 str("Make"),
		Model:                str("Model"),
		LensModel:            str("LensModel"),
		LensInfo:             str("LensInfo"),
		FocalLength:          str("FocalLength"),
		FocalLengthIn35mm:    str("FocalLengthIn35mmFormat"),
		FNumber:              str("FNumber"),
		ExposureTime:         str("ExposureTime"),
		ISO:                  str("ISO"),
		ExposureCompensation: str("ExposureCompensation"),
		WhiteBalance:         str("WhiteBalance"),
		MeteringMode:         str("MeteringMode"),
		ExposureMode:         str("ExposureMode"),
		Flash:                str("Flash"),
		ImageWidth:           str("ImageWidth"),
		ImageHeight:          str("ImageHeight"),
		GPSLatitude:          str("GPSLatitude"),
		GPSLongitude:         str("GPSLongitude"),
		Artist:               str("Artist"),
		Copyright:            str("Copyright"),
		Software:             str("Software"),
	}
}

func exifToPromptString(e exifData) string {
	fields := []struct{ k, v string }{
		{"FileName", e.FileName}, {"DateTimeOriginal", e.DateTimeOriginal},
		{"Make", e.Make}, {"Model", e.Model},
		{"LensModel", e.LensModel}, {"LensInfo", e.LensInfo},
		{"FocalLength", e.FocalLength}, {"FocalLengthIn35mmFormat", e.FocalLengthIn35mm},
		{"FNumber", e.FNumber}, {"ExposureTime", e.ExposureTime}, {"ISO", e.ISO},
		{"ExposureCompensation", e.ExposureCompensation},
		{"WhiteBalance", e.WhiteBalance},
		{"MeteringMode", e.MeteringMode}, {"ExposureMode", e.ExposureMode},
		{"Flash", e.Flash},
		{"ImageWidth", e.ImageWidth}, {"ImageHeight", e.ImageHeight},
		{"GPSLatitude", e.GPSLatitude}, {"GPSLongitude", e.GPSLongitude},
		{"Artist", e.Artist}, {"Copyright", e.Copyright}, {"Software", e.Software},
	}
	var lines []string
	for _, f := range fields {
		if f.v != "" {
			lines = append(lines, f.k+": "+f.v)
		}
	}
	return strings.Join(lines, "\n")
}

func findMagick() string {
	if _, err := exec.LookPath("magick"); err == nil {
		return "magick"
	}
	return "convert"
}

// rawExts lists extensions where we should try extracting the embedded JPEG
// preview via exiftool before falling back to ImageMagick.
var rawExts = map[string]bool{
	"raf": true, "arw": true, "nef": true,
	"cr2": true, "cr3": true, "dng": true,
	"orf": true, "rw2": true, "pef": true,
}

// makePreviewBytes returns the resized JPEG bytes used both for the LLM
// (base64-encoded by the caller) and as the thumbnail BLOB.
func makePreviewBytes(magickCmd, file string, resizePx, quality int) ([]byte, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))

	// For RAW files, try extracting the embedded JPEG preview first.
	// Most cameras embed a full-size JPEG — this avoids needing darktable/rawtherapee.
	if rawExts[ext] {
		if b, err := extractEmbeddedPreviewBytes(magickCmd, file, resizePx, quality); err == nil {
			return b, nil
		}
	}

	return magickConvertBytes(magickCmd, file, resizePx, quality)
}

// extractEmbeddedPreviewBytes pulls the embedded JPEG from a RAW file via
// exiftool, then resizes it with ImageMagick. Returns the resized JPEG bytes.
func extractEmbeddedPreviewBytes(magickCmd, file string, resizePx, quality int) ([]byte, error) {
	tmp, err := os.CreateTemp("", "describe-embedded-*.jpg")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command("exiftool", "-b", "-PreviewImage", file)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("no embedded preview found")
	}

	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return nil, err
	}

	return magickConvertBytes(magickCmd, tmpPath, resizePx, quality)
}

func magickConvertBytes(magickCmd, file string, resizePx, quality int) ([]byte, error) {
	tmp, err := os.CreateTemp("", "describe-preview-*.jpg")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	resize := fmt.Sprintf("%dx%d>", resizePx, resizePx)
	cmd := exec.Command(magickCmd, file,
		"-resize", resize,
		"-quality", fmt.Sprintf("%d", quality),
		"-strip",
		tmpPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s: %s", err, out)
	}

	return os.ReadFile(tmpPath)
}

type descriptionFields struct {
	Subject     string `json:"subject"`
	Setting     string `json:"setting"`
	Light       string `json:"light"`
	Colors      string `json:"colors"`
	Composition string `json:"composition"`
	Vantage     string `json:"vantage"`
	GroundTruth string `json:"ground_truth"`
	Condition   string `json:"condition"`
}

// parseDescriptionFields extracts structured sections from the model output.
// The model is prompted to use "Subject:", "Setting:", etc. headers.
//
// "ground truth" must precede "ground" (no overlap currently, but keep this
// in mind if more keys are added) — the parser walks the map in unspecified
// order, so any prefix collision needs to be resolved by avoiding overlap.
func parseDescriptionFields(description string) descriptionFields {
	fields := descriptionFields{}
	sections := map[string]*string{
		"subject":      &fields.Subject,
		"setting":      &fields.Setting,
		"light":        &fields.Light,
		"colors":       &fields.Colors,
		"composition":  &fields.Composition,
		"vantage":      &fields.Vantage,
		"ground truth": &fields.GroundTruth,
		"condition":    &fields.Condition,
	}

	lines := strings.Split(description, "\n")
	var currentField *string
	var currentLines []string

	flush := func() {
		if currentField != nil && len(currentLines) > 0 {
			*currentField = strings.TrimSpace(strings.Join(currentLines, "\n"))
		}
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Strip markdown formatting and list markers to normalize headers like:
		// "**Subject:**", "- **Subject**:", "**Subject**:", "Subject:", etc.
		cleaned := strings.TrimLeft(trimmed, "-*_ ")
		cleaned = strings.TrimRight(cleaned, "*_ ")
		matched := false
		for key, ptr := range sections {
			lower := strings.ToLower(cleaned)
			if !strings.HasPrefix(lower, key) {
				continue
			}
			// After the key, skip over any combination of markdown markers and
			// parenthetical asides before requiring a colon. This handles headers like:
			//   "Colors:", "**Colors:**", "- **Colors**:",
			//   "**Colors** (from metadata):", "- **Colors** (in B&W):"
			after := cleaned[len(key):]
			for {
				trimmed := strings.TrimLeft(after, "*_ ")
				inside, ok := strings.CutPrefix(trimmed, "(")
				if !ok {
					after = trimmed
					break
				}
				_, rest, found := strings.Cut(inside, ")")
				if !found {
					// unmatched paren; bail and let the colon check fail
					after = trimmed
					break
				}
				after = rest
			}
			if len(after) == 0 || after[0] != ':' {
				continue
			}
			flush()
			currentField = ptr
			rest := strings.TrimLeft(after[1:], "* ")
			if rest != "" {
				currentLines = append(currentLines, rest)
			}
			matched = true
			break
		}
		if !matched && currentField != nil {
			currentLines = append(currentLines, trimmed)
		}
	}
	flush()

	return fields
}

func checkDeps() error {
	var missing []string
	for _, dep := range []string{"exiftool", "curl"} {
		if _, err := exec.LookPath(dep); err != nil {
			missing = append(missing, dep)
		}
	}
	if _, err := exec.LookPath("magick"); err != nil {
		if _, err := exec.LookPath("convert"); err != nil {
			missing = append(missing, "imagemagick")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing dependencies: %s", strings.Join(missing, ", "))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}
