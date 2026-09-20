//go:build unix

package main

import (
	"encoding/json"
	"html/template"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ragotogar/library/testdb"
)

// Exercise the browser POST through go run, real image tools, schema migrations,
// and Postgres writes. Only model responses are mocked; no personal photos or
// production database are used. This also checks the CLI's partial-failure exit.
func TestDescribeCLIIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("real describer integration")
	}
	for _, dependency := range []string{"go", "exiftool", "curl"} {
		if _, err := exec.LookPath(dependency); err != nil {
			t.Skipf("requires %s", dependency)
		}
	}
	if _, err := exec.LookPath("magick"); err != nil {
		if _, err := exec.LookPath("convert"); err != nil {
			t.Skip("requires ImageMagick")
		}
	}
	db, dsn := testdb.NewWithDSN(t, "web_describe", nil)
	var visionCalls, textCalls, embedCalls atomic.Int64
	var failEmbed, failClassify atomic.Bool
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string
			Input []string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if r.URL.Path == "/v1/embeddings" {
			embedCalls.Add(1)
			if failEmbed.Load() {
				http.Error(w, "synthetic embedding failure", http.StatusBadRequest)
				return
			}
			if body.Model != "mock-embed" {
				t.Errorf("unexpected embedding model %q", body.Model)
			}
			data := make([]map[string]any, len(body.Input))
			for i := range data {
				data[i] = map[string]any{"index": i, "embedding": []float32{1, 0, 0, 0}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		content := "Subject: a red square\nSetting: indoors\nQueries: red square"
		switch body.Model {
		case "mock-vision":
			if visionCalls.Add(1) > 1 {
				content = "Subject: a blue square\nSetting: indoors" // force refresh removes old query vectors
			}
		case "mock-classifier":
			textCalls.Add(1)
			content = `{"subject_category":["object"],"scene_indoor_outdoor":"indoor"}`
			if failClassify.Load() {
				content = "invalid classification"
			}
		default:
			t.Errorf("unexpected model %q", body.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer llm.Close()
	t.Setenv("VISION_ENDPOINT", llm.URL)
	t.Setenv("TEXT_ENDPOINT", llm.URL)
	t.Setenv("EMBED_ENDPOINT", llm.URL)
	t.Setenv("EMBED_MODEL", "mock-embed")
	t.Setenv("EMBED_DIM", "4")
	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("LIBRARY_DSN", "postgres:///deliberately_wrong_database")
	photos := t.TempDir()
	photo := filepath.Join(photos, "single image.jpg")
	f, err := os.Create(photo)
	if err != nil {
		t.Fatal(err)
	}
	err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("exiftool", "-overwrite_original", "-Make=Test", "-Model=TestCamera", photo).CombinedOutput(); err != nil {
		t.Fatalf("write fixture EXIF: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(photos, "broken.jpg"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.Must(template.New("describe").Parse(sidebarTmpl)).Parse(describeHTML))
	s := newDescribeServer(repo, dsn, tmpl)
	defer s.Close()
	h := s.handler()
	values := url.Values{"dir": {photo}, "model": {"mock-vision"}, "retries": {"1"},
		"index": {"1"}, "classify_model": {"mock-classifier"}, "dry": {"1"}}
	run := func(wantStatus string) *describeRunView {
		t.Helper()
		w := describeRequest(h, "POST", "/describe", values)
		if w.Code != 303 {
			t.Fatalf("submit: %d %s", w.Code, w.Body.String())
		}
		v := waitDescribe(t, s, 45*time.Second, func(v *describeRunView) bool { return !v.Active })
		if v.Status != wantStatus {
			t.Fatalf("status=%s want=%s, error=%s\n%s", v.Status, wantStatus, v.Error, v.Output)
		}
		return v
	}
	if v := run("finished"); !strings.Contains(v.Output, "1 files would be processed") || strings.Contains(v.Output, "broken.jpg") {
		t.Fatalf("single-file dry run selected siblings: %s", v.Output)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='photos'").Scan(&tables); err != nil || tables != 0 || visionCalls.Load() != 0 || textCalls.Load() != 0 || embedCalls.Load() != 0 {
		t.Fatalf("dry run touched DB/models: tables=%d err=%v", tables, err)
	}
	values.Del("dry")
	v := run("finished")
	if v.Phase != "index" || len(v.Photos) != 1 || !strings.Contains(v.Photos[0].Description, `"subject": "a red square"`) || !strings.Contains(v.Photos[0].Classification, `"scene_indoor_outdoor": "indoor"`) {
		t.Fatalf("index stage or JSON results missing: %+v", v)
	}
	page := describeRequest(h, "GET", "/describe", nil)
	for _, text := range []string{"description JSON", "classification JSON", "a red square", "scene_indoor_outdoor", "download all result snapshots"} {
		if page.Code != 200 || !strings.Contains(page.Body.String(), text) {
			t.Fatalf("results page missing %q: status=%d", text, page.Code)
		}
	}
	for _, store := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + store).Scan(&count); err != nil || count == 0 {
			t.Fatalf("store %s missing: %d %v", store, count, err)
		}
	}
	w := describeRequest(h, "GET", "/describe/results?id="+v.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"index_ready":true`) {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	var path, subject, classifier string
	var thumbBytes int
	if err := db.QueryRow(`SELECT p.file_path, d.subject, length(t.bytes), c.classifier_model
		FROM photos p JOIN descriptions d ON d.photo_id=p.id JOIN thumbnails t ON t.photo_id=p.id
		JOIN classified c ON c.photo_id=p.id`).Scan(&path, &subject, &thumbBytes, &classifier); err != nil {
		t.Fatal(err)
	}
	if path != photo || subject != "a red square" || thumbBytes == 0 || classifier != "mock-classifier" {
		t.Fatalf("unexpected persisted photo: %q %q %d %q", path, subject, thumbBytes, classifier)
	}
	// A different photo already has vectors. A scoped force refresh must leave
	// its values and row timestamps untouched.
	if _, err := db.Exec(`INSERT INTO photos (id,name) VALUES ('unrelated','unrelated');
		INSERT INTO photo_descriptions (photo_id,schema_version,chunk_index,chunk_text,embedding)
		SELECT 'unrelated',schema_version,0,'untouched',embedding FROM photo_descriptions LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	var unrelatedBefore, unrelatedAfter string
	if err := db.QueryRow(`SELECT to_jsonb(d)::text FROM photo_descriptions d WHERE photo_id='unrelated'`).Scan(&unrelatedBefore); err != nil {
		t.Fatal(err)
	}
	embedsBeforeSkip := embedCalls.Load()
	if v := run("finished"); !strings.Contains(v.Output, "Skipped: 1") {
		t.Fatalf("existing photo wasn't skipped: %s", v.Output)
	}
	if embedCalls.Load() != embedsBeforeSkip {
		t.Fatal("skipped photo triggered indexing")
	}
	values.Set("force", "1")
	run("finished")
	if visionCalls.Load() != 2 || textCalls.Load() != 2 {
		t.Fatalf("force/skip calls: vision=%d classifier=%d", visionCalls.Load(), textCalls.Load())
	}
	if err := db.QueryRow(`SELECT to_jsonb(d)::text FROM photo_descriptions d WHERE photo_id='unrelated'`).Scan(&unrelatedAfter); err != nil || unrelatedAfter != unrelatedBefore {
		t.Fatalf("force refreshed an unrelated photo: %v", err)
	}
	var queries int
	if err := db.QueryRow(`SELECT count(*) FROM photo_queries`).Scan(&queries); err != nil || queries != 0 {
		t.Fatalf("force retained obsolete queries: %d %v", queries, err)
	}
	var indexedText string
	if err := db.QueryRow(`SELECT chunk_text FROM photo_descriptions WHERE photo_id <> 'unrelated' LIMIT 1`).Scan(&indexedText); err != nil || !strings.Contains(indexedText, "blue square") || strings.Contains(indexedText, "red square") {
		t.Fatalf("description embedding source not refreshed: %s %v", indexedText, err)
	}
	failEmbed.Store(true)
	if v := run("failed"); v.Phase != "index" || !strings.Contains(v.Output, "synthetic embedding failure") || len(v.Photos) != 1 {
		t.Fatalf("index failure lost JSON output: %+v", v)
	}
	failEmbed.Store(false)
	failClassify.Store(true)
	embedsBeforeFailure := embedCalls.Load()
	if v := run("finished"); len(v.Photos) != 1 || v.Photos[0].Error == "" || v.Photos[0].Classification != "null" {
		t.Fatalf("failed classification showed a stale value: %+v", v)
	}
	if embedCalls.Load() != embedsBeforeFailure {
		t.Fatal("indexed a failed classification")
	}
	failClassify.Store(false)
	values.Del("force")
	values.Set("dir", photos)
	if v := run("failed"); !strings.Contains(v.Output, "Errors: 1, Skipped: 1") || !strings.Contains(v.Output, "Preview failed") {
		t.Fatalf("directory failure not reported: %s", v.Output)
	}
}
