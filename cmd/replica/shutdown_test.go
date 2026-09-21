package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestShutdownDrainsOrCancelsActiveRequest(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "timeout"}[timeout], func(t *testing.T) {
			started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					return
				}
				close(started)
				select {
				case <-release:
					w.Write([]byte("complete"))
				case <-r.Context().Done():
					close(canceled)
				}
			}))
			defer backend.Close()
			u, _ := url.Parse(backend.URL)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := l.Addr().String()
			l.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			grace := 2 * time.Second
			if timeout {
				grace = 100 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() { done <- run(ctx, config{listen: addr, backends: []*url.URL{u}, shutdown: grace}) }()
			client := &http.Client{Timeout: 3 * time.Second}
			// Wait for the listener without forwarding a test inference request.
			deadline := time.Now().Add(3 * time.Second)
			for {
				resp, err := client.Get("http://" + addr + "/health")
				if err == nil {
					resp.Body.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			result := make(chan string, 1)
			go func() {
				resp, err := client.Get("http://" + addr + "/work")
				if err != nil {
					result <- ""
					return
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				result <- string(b)
			}()
			<-started
			cancel()
			if !timeout {
				select {
				case <-done:
					t.Fatal("shutdown did not wait for request")
				case <-time.After(30 * time.Millisecond):
				}
				close(release)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("shutdown hung")
			}
			if got := <-result; !timeout && got != "complete" {
				t.Fatalf("drained response=%q", got)
			}
			if timeout {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("backend was not canceled")
				}
			}
			// Proxy-only shutdown leaves the external server running.
			resp, err := client.Get(backend.URL + "/health")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}
