package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	for _, args := range [][]string{
		{"-backends", ", ,"}, {"-backends", "localhost:1235"},
		{"-backends", "ftp://localhost:1235"}, {"-backends", "http://user:pass@localhost"},
		{"-backends", "http://localhost?token=x"}, {"-backends", "http://localhost:99999"},
		{"-spawn"}, {"-model", "model.gguf"}, {"-shutdown-timeout", "0s"},
		{"-spawn", "-model", "m", "-backends", "http://example.com:1235"},
		{"-spawn", "-model", "m", "-backends", "http://127.0.0.1:1235/api"},
		{"-spawn", "-model", "m", "-backends", "http://127.0.0.1:1235,http://127.0.0.1:1235/"},
		{"-spawn", "-model", "m", "--", "--port=9999"},
	} {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	c, err := parseConfig([]string{"-spawn", "-model", "/path with spaces/m.gguf", "--", "--embedding", "--parallel", "10"}, io.Discard)
	if err != nil || len(c.backends) != 2 || len(c.args) != 3 {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	if _, err := parseConfig([]string{"-backends", "https://example.com/api, http://[::1]:1236"}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestRoundRobinForwardsRequests(t *testing.T) {
	var counts [2]atomic.Int64
	var backends []*url.URL
	for i := range counts {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[i].Add(1)
			body, _ := io.ReadAll(r.Body)
			if r.Method != "POST" || r.URL.RequestURI() != "/api/v1/embeddings?q=x" || string(body) != `{"input":"hello"}` || r.Header.Get("Authorization") != "Bearer test" {
				t.Errorf("request changed: %s %s body=%s auth=%q", r.Method, r.URL, body, r.Header.Get("Authorization"))
			}
			w.Header().Set("X-Replica", fmt.Sprint(i))
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, i)
		}))
		t.Cleanup(s.Close)
		u, _ := url.Parse(s.URL + "/api")
		backends = append(backends, u)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	s := httptest.NewServer(newProxy(backends, transport))
	defer s.Close()
	request := func() string {
		r, _ := http.NewRequest("POST", s.URL+"/v1/embeddings?q=x", strings.NewReader(`{"input":"hello"}`))
		r.Header.Set("Authorization", "Bearer test")
		resp, err := s.Client().Do(r)
		if err != nil {
			t.Error(err)
			return ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-Replica") != string(body) {
			t.Error("response changed")
		}
		return string(body)
	}
	for _, want := range []string{"0", "1", "0", "1"} {
		if got := request(); got != want {
			t.Fatalf("got %s want %s", got, want)
		}
	}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() { request() })
	}
	wg.Wait()
	if counts[0].Load() != 22 || counts[1].Load() != 22 {
		t.Fatalf("unbalanced requests: %d %d", counts[0].Load(), counts[1].Load())
	}
}

func TestProxyStreamsAndPropagatesCancellation(t *testing.T) {
	canceled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	proxy := httptest.NewServer(newProxy([]*url.URL{u}, backend.Client().Transport))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", proxy.URL, nil)
	resp, err := proxy.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("backend request not canceled")
	}
}

func TestBackendFailureReturns502WithoutRetry(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	u, _ := url.Parse(dead.URL)
	w := httptest.NewRecorder()
	newProxy([]*url.URL{u}, http.DefaultTransport).ServeHTTP(w, httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader("input")))
	if w.Code != 502 {
		t.Fatalf("status=%d", w.Code)
	}
}
