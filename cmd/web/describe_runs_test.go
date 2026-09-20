//go:build unix

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A real process exercises output pipes, exit codes, and cancellation without
// invoking an LLM or touching the photo library.
func TestDescribeRunHelper(t *testing.T) {
	mode := os.Getenv("RAGOTOGAR_DESCRIBE_TEST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "success":
		fmt.Println("stdout <script>alert(1)</script>")
		fmt.Fprintln(os.Stderr, "stderr received")
	case "failure":
		fmt.Fprintln(os.Stderr, "synthetic preview failure")
		os.Exit(7)
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestDescribeRunHelper$")
		child.Env = append(os.Environ(), "RAGOTOGAR_DESCRIBE_TEST_HELPER=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			os.Exit(2)
		}
	case "child":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		fmt.Println("listening=" + listener.Addr().String())
		for {
			conn, err := listener.Accept()
			if err != nil {
				os.Exit(2)
			}
			conn.Close()
		}
	}
	os.Exit(0)
}

func newTestDescriber(t *testing.T, mode string) *describeServer {
	t.Helper()
	tmpl := template.Must(template.Must(template.New("describe").Parse(sidebarTmpl)).Parse(describeHTML))
	s := newDescribeServer(t.TempDir(), "postgres:///test", tmpl)
	s.command = func(ctx context.Context, p describeParams) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDescribeRunHelper$")
		cmd.Env = append(os.Environ(), "RAGOTOGAR_DESCRIBE_TEST_HELPER="+mode)
		return cmd
	}
	t.Cleanup(s.Close)
	return s
}

func describeRequest(h http.Handler, method, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func waitDescribe(t *testing.T, s *describeServer, timeout time.Duration, ready func(*describeRunView) bool) *describeRunView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v, _ := s.snapshot()
		if v != nil && ready(v) {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, _ := s.snapshot()
	t.Fatalf("timed out waiting for describer: %+v", v)
	return nil
}

func TestDescribeRunHTTP(t *testing.T) {
	s := newTestDescriber(t, "success")
	h := s.handler()
	for _, method := range []string{"GET", "HEAD"} {
		w := describeRequest(h, method, "/describe?dir="+url.QueryEscape(s.repo), nil)
		if w.Code != 200 {
			t.Fatalf("%s page: %d", method, w.Code)
		}
	}
	if v, _ := s.snapshot(); v != nil {
		t.Fatal("GET/HEAD started a run")
	}
	w := describeRequest(h, "POST", "/describe", url.Values{"dir": {s.repo}, "model": {"test/model"}, "dry": {"1"}})
	if w.Code != 303 || w.Header().Get("Location") != "/describe" {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	v := waitDescribe(t, s, 5*time.Second, func(v *describeRunView) bool { return !v.Active })
	if v.Status != "finished" || !strings.Contains(v.Output, "stderr received") || v.FinishedAt == "" {
		t.Fatalf("unexpected run: %+v", v)
	}
	w = describeRequest(h, "GET", "/describe/status?id="+v.ID, nil)
	var got describeRunView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Output != v.Output || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status: %s, %v", w.Body.String(), err)
	}
	w = describeRequest(h, "GET", "/describe", nil)
	if !strings.Contains(w.Body.String(), `value="test/model"`) || !strings.Contains(w.Body.String(), "&lt;script&gt;") || strings.Contains(w.Body.String(), "<script>alert") {
		t.Fatal("refresh lost run settings or did not escape output")
	}
	// A stale tab cannot cancel a later run.
	if w := describeRequest(h, "POST", "/describe/cancel", url.Values{"id": {"stale"}}); w.Code != 404 {
		t.Fatalf("stale cancel: %d", w.Code)
	}
}

func TestDescribeRejectsInvalidRequests(t *testing.T) {
	s := newTestDescriber(t, "success")
	h := s.handler()
	for _, input := range []string{"", filepath.Join(s.repo, "missing.jpg")} {
		w := describeRequest(h, "POST", "/describe", url.Values{"dir": {input}})
		if w.Code != 400 || !strings.Contains(w.Body.String(), `role="alert"`) {
			t.Fatalf("invalid path: %d", w.Code)
		}
	}
	for _, path := range []string{"/describe", "/describe/cancel"} {
		r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader("dir="+url.QueryEscape(s.repo)))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://untrusted.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("cross-site %s: %d", path, w.Code)
		}
	}
	if w := describeRequest(h, "GET", "/describe/cancel", nil); w.Code != 405 {
		t.Fatalf("GET cancel: %d", w.Code)
	}
	if v, _ := s.snapshot(); v != nil {
		t.Fatal("invalid request launched process")
	}
}

func TestDescribeFailureAndStartFailure(t *testing.T) {
	s := newTestDescriber(t, "failure")
	p := parseDescribeParams(url.Values{"dir": {s.repo}})
	if _, err := s.start(p); err != nil {
		t.Fatal(err)
	}
	v := waitDescribe(t, s, 5*time.Second, func(v *describeRunView) bool { return !v.Active })
	if v.Status != "failed" || v.Error != "exit status 7" || !strings.Contains(v.Output, "synthetic preview failure") {
		t.Fatalf("lost failure: %+v", v)
	}
	s.command = func(ctx context.Context, p describeParams) *exec.Cmd {
		return exec.CommandContext(ctx, filepath.Join(s.repo, "missing-executable"))
	}
	if _, err := s.start(p); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestDescribeCancelAndShutdownStopChildren(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%v", shutdown), func(t *testing.T) {
			s := newTestDescriber(t, "parent")
			h := s.handler()
			p := url.Values{"dir": {s.repo}}
			if w := describeRequest(h, "POST", "/describe", p); w.Code != 303 {
				t.Fatalf("start: %d %s", w.Code, w.Body.String())
			}
			v := waitDescribe(t, s, 5*time.Second, func(v *describeRunView) bool { return strings.Contains(v.Output, "listening=") })
			address := strings.TrimSpace(strings.Split(v.Output, "listening=")[1])
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if w := describeRequest(h, "POST", "/describe", p); w.Code != 409 {
				t.Fatalf("duplicate run: %d", w.Code)
			}
			if shutdown {
				s.Close()
			} else if w := describeRequest(h, "POST", "/describe/cancel", url.Values{"id": {v.ID}}); w.Code != 303 {
				t.Fatalf("cancel: %d", w.Code)
			}
			v = waitDescribe(t, s, 3*time.Second, func(v *describeRunView) bool { return !v.Active })
			if v.Status != "canceled" {
				t.Fatalf("cancel: %+v", v)
			}
			if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
				conn.Close()
				t.Fatal("child process survived cancellation")
			}
		})
	}
}

func TestDescribeCommandUsesLiteralArgsAndWebDSN(t *testing.T) {
	t.Setenv("LIBRARY_DSN", "postgres:///wrong")
	t.Setenv("VISION_ENDPOINT", "http://vision.test")
	p := describeParams{dir: "/photos/$(touch nope); 'single'.NEF", model: "vision model",
		classifyModel: "text model", previewWorkers: 2, inferenceWorkers: 3, retries: 4,
		force: true, dryRun: true, classify: true}
	s := newDescribeServer("/repo", "postgres:///chosen", nil)
	cmd := s.command(context.Background(), p)
	want := []string{"go", "run", ".", "-force", "-dry-run", "-model", "vision model", "-preview-workers", "2",
		"-inference-workers", "3", "-retries", "4", "-classify", "-classify-model", "text model", "--", p.dir}
	if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != "/repo/cmd/describe" {
		t.Fatalf("command: %v, dir=%s", cmd.Args, cmd.Dir)
	}
	var dsn string
	for _, env := range cmd.Environ() {
		if after, ok := strings.CutPrefix(env, "LIBRARY_DSN="); ok {
			dsn = after
		}
	}
	if dsn != s.dsn || !strings.Contains(strings.Join(cmd.Environ(), "\n"), "VISION_ENDPOINT=http://vision.test") {
		t.Fatal("DSN or endpoint inheritance lost")
	}
}

func TestDescribePathAndAttempts(t *testing.T) {
	repo := t.TempDir()
	photo := filepath.Join(repo, "single file.NEF")
	if err := os.WriteFile(photo, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]string{"single file.NEF": photo, photo: photo, repo: repo} {
		got, err := resolveDescribePath(repo, input)
		if err != nil || got != want {
			t.Fatalf("path %q: %q, %v", input, got, err)
		}
	}
	if p := parseDescribeParams(url.Values{"retries": {"0"}}); p.retries != 1 {
		t.Fatal("zero attempts would fail every image without calling the model")
	}
}

func TestDescribeLogBounded(t *testing.T) {
	var l describeLog
	_, _ = l.Write([]byte(strings.Repeat("a", describeLogLimit+10)))
	_, _ = l.Write([]byte("last line\n"))
	if len(l.tail) != describeLogLimit || !l.truncated || !strings.HasSuffix(string(l.tail), "last line\n") {
		t.Fatal("log tail is not bounded or lost recent output")
	}
}
