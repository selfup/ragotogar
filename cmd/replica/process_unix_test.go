//go:build unix

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Reuse the test executable as a fake llama-server; no model or GPU needed.
func TestReplicaProcess(t *testing.T) {
	if os.Getenv("REPLICA_TEST_CHILD") != "1" {
		return
	}
	var host, port, mode string
	for i := 0; i+1 < len(os.Args); i++ {
		switch os.Args[i] {
		case "--host":
			host = os.Args[i+1]
		case "--port":
			port = os.Args[i+1]
		case "--model":
			mode = os.Args[i+1]
		}
	}
	if mode == "stubborn" {
		signal.Ignore(syscall.SIGTERM)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("REPLICA_TEST_DIR"), port), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(2)
	}
	if mode == "exit" {
		os.Exit(7)
	}
	signals := []os.Signal{os.Interrupt}
	if mode != "stubborn" {
		signals = append(signals, syscall.SIGTERM)
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if mode == "loading" {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	srv := &http.Server{Addr: net.JoinHostPort(host, port), Handler: mux}
	go func() {
		if srv.ListenAndServe() != http.ErrServerClosed {
			os.Exit(3)
		}
	}()
	<-ctx.Done()
	srv.Close()
	os.Exit(0)
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func managedConfig(t *testing.T, mode string) config {
	t.Helper()
	t.Setenv("REPLICA_TEST_CHILD", "1")
	t.Setenv("REPLICA_TEST_DIR", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config{listen: freeAddress(t), executable: exe, model: mode, spawn: true,
		args: []string{"-test.run=^TestReplicaProcess$", "--"}, startup: 5 * time.Second, shutdown: 100 * time.Millisecond}
	for range 2 {
		u, _ := url.Parse("http://" + freeAddress(t))
		c.backends = append(c.backends, u)
	}
	return c
}

func assertChildrenGone(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(os.Getenv("REPLICA_TEST_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		b, _ := os.ReadFile(filepath.Join(os.Getenv("REPLICA_TEST_DIR"), entry.Name()))
		pid, _ := strconv.Atoi(string(b))
		if pid <= 0 {
			t.Fatal("missing PID")
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("child %d still exists: %v", pid, err)
		}
	}
}

func awaitProxy(t *testing.T, address string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + address + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("proxy did not become ready")
}

func TestManagedShutdown(t *testing.T) {
	for _, mode := range []string{"ready", "stubborn"} {
		t.Run(mode, func(t *testing.T) {
			c := managedConfig(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- run(ctx, c) }()
			awaitProxy(t, c.listen)
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown hung")
			}
			assertChildrenGone(t)
		})
	}
}

func TestManagedStartupFailureCleansUp(t *testing.T) {
	for _, mode := range []string{"loading", "exit"} {
		t.Run(mode, func(t *testing.T) {
			c := managedConfig(t, mode)
			c.startup = 500 * time.Millisecond
			if err := run(context.Background(), c); err == nil {
				t.Fatal("expected startup failure")
			}
			assertChildrenGone(t)
		})
	}
}

func TestManagedRuntimeExitStopsSibling(t *testing.T) {
	c := managedConfig(t, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, c) }()
	awaitProxy(t, c.listen)
	b, err := os.ReadFile(filepath.Join(os.Getenv("REPLICA_TEST_DIR"), c.backends[0].Port()))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "exited") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit")
	}
	assertChildrenGone(t)
}

func TestOccupiedBackendNotAdopted(t *testing.T) {
	c := managedConfig(t, "ready")
	l, err := net.Listen("tcp", c.backends[1].Host)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := run(context.Background(), c); err == nil {
		t.Fatal("accepted occupied backend")
	}
	entries, _ := os.ReadDir(os.Getenv("REPLICA_TEST_DIR"))
	if len(entries) != 0 {
		t.Fatal("started a child before reserving all ports")
	}
}
