package main

import (
	"bytes"
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
	"time"
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
	inputDir    string
	outputDir   string
	dryRun      bool
	lmBase      string
	model       string
	resizePx    int
	jpegQuality int
	maxRetries  int
	retryDelay  time.Duration
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
		lmBase:      envOr("LM_STUDIO_BASE", "http://localhost:1234"),
		model:       envOr("LM_MODEL", "mistralai/devstral-small-2-2512"),
		resizePx:    envOrInt("RESIZE_PX", 1024),
		jpegQuality: envOrInt("JPEG_QUALITY", 85),
		maxRetries:  3,
		retryDelay:  5 * time.Second,
	}

	flag.StringVar(&cfg.outputDir, "output", "", "Output directory for .txt files (default: <input_dir>/descriptions)")
	flag.StringVar(&cfg.model, "model", cfg.model, "LM Studio model name")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "List files that would be processed without calling the LLM")
	flag.IntVar(&cfg.maxRetries, "retries", cfg.maxRetries, "Max retry attempts per image on API failure")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <input_dir>\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}
	cfg.inputDir = flag.Arg(0)

	info, err := os.Stat(cfg.inputDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: '%s' is not a directory\n", cfg.inputDir)
		os.Exit(1)
	}

	if cfg.outputDir == "" {
		cfg.outputDir = filepath.Join(cfg.inputDir, "descriptions")
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

func run(cfg config) error {
	files, err := collectFiles(cfg.inputDir, 3)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Printf("No image files found in '%s'\n", cfg.inputDir)
		return nil
	}

	fmt.Printf("Found %d image(s) in '%s'\n", len(files), cfg.inputDir)
	fmt.Printf("Output: %s\n", cfg.outputDir)
	fmt.Printf("Model:  %s @ %s\n", cfg.model, cfg.lmBase)
	fmt.Printf("Retries: %d (delay %s)\n\n", cfg.maxRetries, cfg.retryDelay)

	if cfg.dryRun {
		for _, f := range files {
			fmt.Printf("  [dry-run] %s\n", f)
		}
		fmt.Printf("\n%d files would be processed.\n", len(files))
		return nil
	}

	if err := os.MkdirAll(cfg.outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	magickCmd := findMagick()

	var processed, errors, skipped int
	for i, file := range files {
		exif := extractEXIF(file)
		safeName := safeOutputName(cfg.inputDir, file, exif)
		txtOut := filepath.Join(cfg.outputDir, safeName+".json")

		if _, err := os.Stat(txtOut); err == nil {
			fmt.Printf("  [skip] %s (already exists)\n", safeName)
			skipped++
			continue
		}

		fmt.Printf("  [%d/%d] %s", i+1, len(files), safeName)

		start := time.Now()

		b64, err := makePreviewBase64(magickCmd, file, cfg.resizePx, cfg.jpegQuality)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n    !! Preview failed: %v, skipping\n", err)
			errors++
			continue
		}

		description, err := describeWithRetry(cfg, b64, exifToPromptString(exif))
		elapsed := time.Since(start)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n    !! Vision API failed after %d attempts: %v\n", cfg.maxRetries, err)
			errors++
		}

		fmt.Printf(" (%s)\n", elapsed.Round(time.Millisecond))

		if err := writeOutput(txtOut, safeName, file, exif, description, elapsed); err != nil {
			fmt.Fprintf(os.Stderr, "    !! Write failed: %v\n", err)
			errors++
			continue
		}

		processed++
	}

	fmt.Printf("\nDone. Processed: %d, Errors: %d, Skipped: %d\n", processed, errors, skipped)
	fmt.Printf("Output: %s\n", cfg.outputDir)
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
	prompt := "Describe exactly what is visible in this photograph. Be concrete and specific — name objects, colors, materials, positions, and spatial relationships. Do NOT use subjective or interpretive language like 'intimate', 'captures', 'suggests', or 'evokes'. Just state what you see.\n\nFormat:\n- Subject: what/who is in the frame\n- Setting: specific location type, surfaces, objects\n- Light: direction, color, source (window/lamp/sun/etc)\n- Colors: dominant palette\n- Composition: framing, angle, depth of field\n\nCamera metadata for context:\n" + exif

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
	httpReq.Header.Set("Authorization", "Bearer lm-studio")

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

	return content, nil
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
	FileName              string `json:"file_name"`
	DateTimeOriginal      string `json:"date_time_original"`
	Make                  string `json:"make"`
	Model                 string `json:"model"`
	LensModel             string `json:"lens_model"`
	LensInfo              string `json:"lens_info"`
	FocalLength           string `json:"focal_length"`
	FocalLengthIn35mm     string `json:"focal_length_in_35mm"`
	FNumber               string `json:"f_number"`
	ExposureTime          string `json:"exposure_time"`
	ISO                   string `json:"iso"`
	ExposureCompensation  string `json:"exposure_compensation"`
	WhiteBalance          string `json:"white_balance"`
	MeteringMode          string `json:"metering_mode"`
	ExposureMode          string `json:"exposure_mode"`
	Flash                 string `json:"flash"`
	ImageWidth            string `json:"image_width"`
	ImageHeight           string `json:"image_height"`
	GPSLatitude           string `json:"gps_latitude,omitempty"`
	GPSLongitude          string `json:"gps_longitude,omitempty"`
	Artist                string `json:"artist,omitempty"`
	Copyright             string `json:"copyright,omitempty"`
	Software              string `json:"software,omitempty"`
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

func makePreviewBase64(magickCmd, file string, resizePx, quality int) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))

	// For RAW files, try extracting the embedded JPEG preview first.
	// Most cameras embed a full-size JPEG — this avoids needing darktable/rawtherapee.
	if rawExts[ext] {
		if b64, err := extractEmbeddedPreview(magickCmd, file, resizePx, quality); err == nil {
			return b64, nil
		}
	}

	return magickConvert(magickCmd, file, resizePx, quality)
}

// extractEmbeddedPreview pulls the embedded JPEG from a RAW file via exiftool,
// then resizes it with ImageMagick.
func extractEmbeddedPreview(magickCmd, file string, resizePx, quality int) (string, error) {
	// Extract embedded preview to a temp file
	tmp, err := os.CreateTemp("", "describe-embedded-*.jpg")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command("exiftool", "-b", "-PreviewImage", file)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("no embedded preview found")
	}

	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return "", err
	}

	// Resize the extracted JPEG
	return magickConvert(magickCmd, tmpPath, resizePx, quality)
}

func magickConvert(magickCmd, file string, resizePx, quality int) (string, error) {
	tmp, err := os.CreateTemp("", "describe-preview-*.jpg")
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("%s: %s", err, out)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

type descriptionFields struct {
	Subject     string `json:"subject"`
	Setting     string `json:"setting"`
	Light       string `json:"light"`
	Colors      string `json:"colors"`
	Composition string `json:"composition"`
}

type photoDescription struct {
	Name        string            `json:"name"`
	File        string            `json:"file"`
	Path        string            `json:"path"`
	DurationMs  int64             `json:"duration_ms"`
	Duration    string            `json:"duration"`
	Metadata    exifData          `json:"metadata"`
	Fields      descriptionFields `json:"fields"`
	Description string            `json:"description"`
}

// parseDescriptionFields extracts structured sections from the model output.
// The model is prompted to use "Subject:", "Setting:", etc. headers.
func parseDescriptionFields(description string) descriptionFields {
	fields := descriptionFields{}
	sections := map[string]*string{
		"subject":     &fields.Subject,
		"setting":     &fields.Setting,
		"light":       &fields.Light,
		"colors":      &fields.Colors,
		"composition": &fields.Composition,
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
			// Check that the key is followed by a colon (possibly with trailing bold markers)
			after := cleaned[len(key):]
			after = strings.TrimLeft(after, "*_ ")
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

func writeOutput(path, safeName, srcFile string, exif exifData, description string, elapsed time.Duration) error {
	out := photoDescription{
		Name:        safeName,
		File:        filepath.Base(srcFile),
		Path:        srcFile,
		DurationMs:  elapsed.Milliseconds(),
		Duration:    elapsed.Round(time.Millisecond).String(),
		Metadata:    exif,
		Fields:      parseDescriptionFields(description),
		Description: description,
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	return os.WriteFile(path, data, 0644)
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
