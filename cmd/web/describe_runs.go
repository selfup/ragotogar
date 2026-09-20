package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const describeLogLimit = 256 << 10

var errDescribeBusy = errors.New("a describe run is already active; wait for it to finish or cancel it")

// Keep only the latest run. Runs survive browser navigation, but not a web
// server restart. Completed photos are committed by the CLI one at a time.
type describeServer struct {
	repo, dsn    string
	tmpl         *template.Template
	command      func(context.Context, describeParams) *exec.Cmd
	indexCommand func(context.Context, string) *exec.Cmd
	mu           sync.Mutex
	latest       *describeRun
	closed       bool
}

type describeRun struct {
	view    describeRunView // guarded by describeServer.mu
	params  describeParams
	output  describeLog
	cancel  context.CancelFunc
	done    chan struct{}
	tempDir string
	results describeResults // guarded by describeServer.mu
}

type describeRunView struct {
	ID           string              `json:"id"`
	Status       string              `json:"status"`
	Active       bool                `json:"active"`
	StartedAt    string              `json:"started_at"`
	FinishedAt   string              `json:"finished_at,omitempty"`
	Command      string              `json:"command"`
	Output       string              `json:"output"`
	Truncated    bool                `json:"truncated"`
	Error        string              `json:"error,omitempty"`
	Phase        string              `json:"phase"`
	Photos       []describePhotoView `json:"photos"`
	PhotoCount   int                 `json:"photo_count"`
	ResultsError string              `json:"results_error,omitempty"`
}

// Stdout and stderr share a bounded tail, even for very large libraries.
type describeLog struct {
	mu        sync.Mutex
	tail      []byte
	truncated bool
}

func (l *describeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if n >= describeLogLimit {
		l.truncated = l.truncated || len(l.tail)+n > describeLogLimit
		l.tail = append(l.tail[:0], p[n-describeLogLimit:]...)
	} else {
		if excess := len(l.tail) + n - describeLogLimit; excess > 0 {
			copy(l.tail, l.tail[excess:])
			l.tail = l.tail[:len(l.tail)-excess]
			l.truncated = true
		}
		l.tail = append(l.tail, p...)
	}
	return n, nil
}

func newDescribeServer(repo, dsn string, tmpl *template.Template) *describeServer {
	s := &describeServer{repo: repo, dsn: dsn, tmpl: tmpl}
	s.command = func(ctx context.Context, p describeParams) *exec.Cmd {
		// Same entry point as scripts/photo_describe.sh. No shell evaluates
		// form values, and the selected web DSN overrides the inherited env.
		cmd := exec.CommandContext(ctx, "go", append([]string{"run", "."}, describeArgs(p)...)...)
		cmd.Dir = filepath.Join(repo, "cmd", "describe")
		cmd.Env = append(cmd.Environ(), "LIBRARY_DSN="+dsn)
		return cmd
	}
	s.indexCommand = func(ctx context.Context, photosFile string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/index",
			"-photos-file", photosFile, "-reindex=descriptions,metadata,queries")
		cmd.Dir = repo
		cmd.Env = append(cmd.Environ(), "LIBRARY_DSN="+dsn)
		return cmd
	}
	return s
}

func resolveDescribePath(repo, input string) (string, error) {
	if input == "" {
		return "", errors.New("enter a photo directory or image file")
	}
	if input == "~" || strings.HasPrefix(input, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		input = filepath.Join(homeDir, strings.TrimPrefix(strings.TrimPrefix(input, "~"), "/"))
	}
	if !filepath.IsAbs(input) {
		input = filepath.Join(repo, input)
	}
	input = filepath.Clean(input)
	info, err := os.Stat(input)
	if err != nil {
		return "", fmt.Errorf("photo path: %w", err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", errors.New("photo path must be a directory or a regular image file")
	}
	return input, nil
}

func (s *describeServer) start(p describeParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("web server is shutting down")
	}
	if s.latest != nil && s.latest.view.Active {
		return "", errDescribeBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	tempDir, err := os.MkdirTemp("", "ragotogar-describe-")
	if err != nil {
		cancel()
		return "", err
	}
	p.resultsJSONL = filepath.Join(tempDir, "results.jsonl")
	j := &describeRun{
		view: describeRunView{ID: rand.Text(), Status: "running", Active: true,
			Phase: "describe", StartedAt: time.Now().Format(time.RFC3339), Command: buildDescribeCommand(p)},
		params: p, cancel: cancel, done: make(chan struct{}),
		tempDir: tempDir, results: describeResults{path: p.resultsJSONL},
	}
	cmd := s.command(ctx, p)
	cmd.Stdout, cmd.Stderr = &j.output, &j.output
	cmd.WaitDelay = 5 * time.Second
	if err := configureDescribeProcess(cmd); err != nil {
		cancel()
		os.RemoveAll(tempDir)
		return "", err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("start describer: %w", err)
	}
	if s.latest != nil {
		os.RemoveAll(s.latest.tempDir)
	}
	s.latest = j
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		reportErr := j.results.refresh(true)
		var names []string
		for name, ready := range j.results.ready {
			if ready {
				names = append(names, name)
			}
		}
		s.mu.Unlock()
		err = errors.Join(err, reportErr)
		if p.index && !p.dryRun && ctx.Err() == nil && reportErr == nil {
			if len(names) == 0 {
				fmt.Fprintln(&j.output, "\nIndex skipped: no photos were successfully classified in this run.")
			} else {
				err = errors.Join(err, s.indexPhotos(ctx, j, names))
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		defer close(j.done)
		j.view.Active = false
		j.view.FinishedAt = time.Now().Format(time.RFC3339)
		switch {
		case ctx.Err() != nil:
			j.view.Status = "canceled"
		case err != nil:
			j.view.Status, j.view.Error = "failed", err.Error()
		default:
			j.view.Status = "finished"
		}
		cancel()
	}()
	return j.view.ID, nil
}

func (s *describeServer) snapshot() (*describeRunView, describeParams) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		return nil, describeParams{}
	}
	j := s.latest
	v := j.view
	if err := j.results.refresh(false); err != nil {
		v.ResultsError = err.Error()
	}
	v.Photos = slices.Clone(j.results.photos)
	v.PhotoCount = len(j.results.ready)
	j.output.mu.Lock()
	v.Output, v.Truncated = string(j.output.tail), j.output.truncated
	j.output.mu.Unlock()
	return &v, j.params
}

func (s *describeServer) indexPhotos(ctx context.Context, j *describeRun, names []string) error {
	slices.Sort(names)
	data, err := json.Marshal(names)
	if err != nil {
		return err
	}
	photosFile := filepath.Join(j.tempDir, "index-photos.json")
	if err := os.WriteFile(photosFile, data, 0600); err != nil {
		return err
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return ctx.Err()
	}
	j.view.Phase = "index"
	s.mu.Unlock()
	fmt.Fprintf(&j.output, "\nIndexing %d successfully classified photo(s); refreshing descriptions, metadata, and queries.\n", len(names))
	cmd := s.indexCommand(ctx, photosFile)
	cmd.Stdout, cmd.Stderr = &j.output, &j.output
	cmd.WaitDelay = 5 * time.Second
	if err := configureDescribeProcess(cmd); err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("index: %w", err)
	}
	return nil
}

func (s *describeServer) cancelRun(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil || s.latest.view.ID != id {
		return false
	}
	if s.latest.view.Active {
		s.latest.view.Status = "canceling"
		s.latest.cancel()
	}
	return true
}

func (s *describeServer) Close() {
	s.mu.Lock()
	s.closed = true
	j := s.latest
	if j != nil && j.view.Active {
		j.view.Status = "canceling"
		j.cancel()
	}
	s.mu.Unlock()
	if j != nil {
		<-j.done
		s.mu.Lock()
		os.RemoveAll(j.tempDir)
		s.mu.Unlock()
	}
}

func (s *describeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /describe", s.page)
	mux.HandleFunc("POST /describe", s.submit)
	mux.HandleFunc("GET /describe/status", s.status)
	mux.HandleFunc("GET /describe/results", s.results)
	mux.HandleFunc("POST /describe/cancel", s.cancel)
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		protected.ServeHTTP(w, r)
	})
}

func (s *describeServer) page(w http.ResponseWriter, r *http.Request) {
	j, params := s.snapshot()
	if j == nil || r.URL.Query().Has("dir") {
		params = parseDescribeParams(r.URL.Query())
	}
	renderDescribe(w, s.tmpl, s.dsn, params, j, "", http.StatusOK)
}

func (s *describeServer) submit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid describe form", http.StatusBadRequest)
		return
	}
	p := parseDescribeParams(r.PostForm)
	path, err := resolveDescribePath(s.repo, p.dir)
	status := http.StatusBadRequest
	if err == nil {
		p.dir = path
		_, err = s.start(p)
		status = http.StatusInternalServerError
		if errors.Is(err, errDescribeBusy) {
			status = http.StatusConflict
		}
	}
	if err != nil {
		job, _ := s.snapshot()
		renderDescribe(w, s.tmpl, s.dsn, p, job, err.Error(), status)
		return
	}
	// Refreshing the resulting page must never repeat the POST.
	http.Redirect(w, r, "/describe", http.StatusSeeOther)
}

func (s *describeServer) status(w http.ResponseWriter, r *http.Request) {
	j, _ := s.snapshot()
	if j == nil || j.ID != r.URL.Query().Get("id") {
		http.Error(w, "run no longer available; reload the describe page", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j)
}

func (s *describeServer) cancel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid cancel request", http.StatusBadRequest)
		return
	}
	if !s.cancelRun(r.PostForm.Get("id")) {
		http.Error(w, "run no longer available; reload the describe page", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/describe", http.StatusSeeOther)
}
