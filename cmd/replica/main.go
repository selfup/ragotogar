// replica round-robins HTTP requests across llama.cpp servers and optionally
// owns their lifetime. Run with go run ./cmd/replica -h.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	listen, executable, model string
	backends                  []*url.URL
	spawn                     bool
	args                      []string
	startup, shutdown         time.Duration
}

func parseConfig(args []string, output io.Writer) (config, error) {
	var c config
	f := flag.NewFlagSet("replica", flag.ContinueOnError)
	f.SetOutput(output)
	f.StringVar(&c.listen, "listen", "127.0.0.1:1234", "proxy listen address")
	b := f.String("backends", "http://127.0.0.1:1235,http://127.0.0.1:1236", "comma-separated backend base URLs")
	f.BoolVar(&c.spawn, "spawn", false, "start and supervise local llama-server processes (Unix)")
	f.StringVar(&c.executable, "llama-server", "llama-server", "llama-server executable")
	f.StringVar(&c.model, "model", "", "GGUF model path (required with -spawn)")
	f.DurationVar(&c.startup, "startup-timeout", 5*time.Minute, "total time allowed for managed replicas to become ready")
	f.DurationVar(&c.shutdown, "shutdown-timeout", 30*time.Second, "request drain timeout, then child SIGTERM grace period")
	f.Usage = func() {
		fmt.Fprintln(output, "Usage: replica [flags] [-- shared llama-server arguments]\nManaged example: -spawn -model /path/model.gguf -- --embedding --parallel 10 -ngl 99")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		return c, err
	}
	c.args = f.Args()
	if c.startup <= 0 || c.shutdown <= 0 {
		return c, errors.New("timeouts must be positive")
	}
	if !c.spawn && (c.model != "" || len(c.args) > 0) {
		return c, errors.New("-model and llama-server arguments require -spawn")
	}
	if c.spawn && c.model == "" {
		return c, errors.New("-spawn requires -model")
	}
	for _, arg := range c.args {
		name, _, _ := strings.Cut(arg, "=")
		switch name {
		case "--host", "--port", "-m", "--model", "--":
			return c, fmt.Errorf("%s is managed by replica; use -backends or -model", name)
		}
	}
	seen := map[string]bool{}
	for raw := range strings.SplitSeq(*b, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return c, fmt.Errorf("invalid backend URL %q (use http(s) without credentials, query, or fragment)", raw)
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return c, fmt.Errorf("invalid backend port in %q", raw)
			}
		}
		if c.spawn {
			ip := net.ParseIP(u.Hostname())
			if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.Port() == "" || (u.Path != "" && u.Path != "/") {
				return c, fmt.Errorf("managed backend %q must be http://loopback-IP:port without a path", raw)
			}
			if seen[u.Host] {
				return c, fmt.Errorf("duplicate managed backend %q", raw)
			}
			seen[u.Host] = true
		}
		c.backends = append(c.backends, u)
	}
	if len(c.backends) == 0 {
		return c, errors.New("no backends configured")
	}
	return c, nil
}

func newProxy(backends []*url.URL, transport http.RoundTripper) http.Handler {
	proxies := make([]*httputil.ReverseProxy, 0, len(backends))
	for _, u := range backends {
		p := httputil.NewSingleHostReverseProxy(u)
		p.Transport = transport
		p.FlushInterval = -1 // Forward streaming responses immediately.
		p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("backend %s: %v", u.Host, err)
			http.Error(w, "backend unavailable", http.StatusBadGateway)
		}
		proxies = append(proxies, p)
	}
	var next atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxies[(next.Add(1)-1)%uint64(len(proxies))].ServeHTTP(w, r)
	})
}

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// Each child has exactly one Wait caller; events is buffered for all children
// so startup failures and shutdown never strand a waiter.
func startChild(executable string, args []string, events chan<- error) (*child, error) {
	cmd := exec.Command(executable, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := configureProcess(cmd); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &child{cmd: cmd, done: make(chan struct{})}
	log.Printf("started replica pid=%d", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		events <- fmt.Errorf("replica pid=%d exited: %v", cmd.Process.Pid, err)
		close(p.done)
	}()
	return p, nil
}

func stopChildren(children []*child, grace time.Duration) {
	if len(children) == 0 {
		return
	}
	for _, p := range children {
		signalProcess(p.cmd, false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	for _, p := range children {
		select {
		case <-p.done:
		case <-ctx.Done():
		}
	}
	// Also remove descendants when the group leader exited first.
	for _, p := range children {
		signalProcess(p.cmd, true)
	}
	for _, p := range children {
		<-p.done
	}
}

func waitReady(ctx context.Context, backends []*url.URL, transport http.RoundTripper) error {
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, u := range backends {
		health := *u
		health.Path = "/health"
		for {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, health.String(), nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting for %s: %w", u.Host, ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		log.Printf("replica ready: %s", u.Host)
	}
	return nil
}

func run(ctx context.Context, c config) error {
	log.Printf("replica supervisor pid=%d", os.Getpid())
	// Bind before spawning: a proxy-port conflict must not leave model processes.
	listener, err := net.Listen("tcp", c.listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Backend URLs are direct, independent of HTTP_PROXY.
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost = 256, 64
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 0
	defer transport.CloseIdleConnections()
	events := make(chan error, len(c.backends))
	var children []*child
	defer func() { stopChildren(children, c.shutdown) }()
	if c.spawn {
		// Reserve all backend ports before starting any process, catching occupied
		// ports instead of mistaking somebody else's /health for our replica.
		var reservations []net.Listener
		defer func() {
			for _, l := range reservations {
				l.Close()
			}
		}()
		for _, u := range c.backends {
			l, err := net.Listen("tcp", u.Host)
			if err != nil {
				return fmt.Errorf("reserve backend %s: %w", u.Host, err)
			}
			reservations = append(reservations, l)
		}
		for i, u := range c.backends {
			if err := ctx.Err(); err != nil {
				return nil
			}
			args := append([]string{}, c.args...)
			args = append(args, "--model", c.model, "--host", u.Hostname(), "--port", u.Port())
			reservations[i].Close()
			p, err := startChild(c.executable, args, events)
			if err != nil {
				return fmt.Errorf("start %s: %w", u.Host, err)
			}
			children = append(children, p)
		}
		readyCtx, cancel := context.WithTimeout(ctx, c.startup)
		ready := make(chan error, 1)
		go func() { ready <- waitReady(readyCtx, c.backends, transport) }()
		select {
		case err = <-ready:
			cancel()
		case err = <-events:
			cancel()
			<-ready
		case <-ctx.Done():
			cancel()
			<-ready
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
	}
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	srv := &http.Server{Handler: newProxy(c.backends, transport), ReadHeaderTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return requestCtx }}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	log.Printf("listening on %s -> %d backend(s)", listener.Addr(), len(c.backends))
	select {
	case <-ctx.Done():
	case err = <-events:
	case err = <-served:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	log.Printf("draining proxy requests")
	drainCtx, cancel := context.WithTimeout(context.Background(), c.shutdown)
	defer cancel()
	if shutdownErr := srv.Shutdown(drainCtx); shutdownErr != nil {
		log.Printf("request drain: %v; closing active connections", shutdownErr)
		cancelRequests()
		srv.Close()
	}
	return err
}

func main() {
	c, err := parseConfig(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = run(ctx, c)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
