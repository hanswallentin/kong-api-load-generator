// Package mock is the built-in upstream: a small HTTP server that answers for
// the endpoints in config.yaml, so a demo needs no external backend.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kong/kong-loadgen/src/config"
)

// Headers the load generator sends to shape the mock's response.
const (
	ErrorHeader      = "X-Loadgen-Error"
	LatencyMinHeader = "X-Loadgen-Latency-Min"
	LatencyMaxHeader = "X-Loadgen-Latency-Max"
)

type route struct {
	keyRequired bool
	methods     map[string]bool
}

// Handler serves the configured paths. Unknown paths return 404, key-auth
// paths without a valid key return 401, and malformed JSON bodies return 400,
// so the same error variants work with or without Kong in front.
type Handler struct {
	routes    map[string]route
	keyHeader string
	keys      map[string]bool
}

// NewHandler builds the mock's routes and valid API keys from the configuration.
func NewHandler(cfg config.Config) *Handler {
	handler := &Handler{
		routes:    map[string]route{},
		keyHeader: cfg.EffectiveKeyHeader(),
		keys:      map[string]bool{},
	}
	for _, group := range cfg.Users.Groups {
		for index := 1; index <= group.Count; index++ {
			handler.keys[group.APIKey(index)] = true
		}
	}
	for _, endpoint := range cfg.Endpoints {
		methods := map[string]bool{}
		for _, method := range endpoint.Methods {
			methods[strings.ToUpper(method)] = true
		}
		handler.routes[endpoint.Path] = route{keyRequired: endpoint.Auth == "key-auth", methods: methods}
	}
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.routes[r.URL.Path]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no route for path", "path": r.URL.Path})
		return
	}
	if len(matched.methods) > 0 && !matched.methods[r.Method] {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed", "method": r.Method})
		return
	}
	if matched.keyRequired && !h.keys[r.Header.Get(h.keyHeader)] {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing or invalid API key", "header": h.keyHeader})
		return
	}

	latency := sleepFor(r)
	if status, ok := requestedError(r); ok {
		writeJSON(w, status, map[string]any{"error": "requested by X-Loadgen-Error", "status": status})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unreadable body"})
		return
	}
	if len(body) > 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && !json.Valid(body) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed JSON body"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     http.StatusOK,
		"method":     r.Method,
		"path":       r.URL.Path,
		"query":      r.URL.RawQuery,
		"headers":    flattenHeaders(r.Header),
		"latency_ms": latency.Milliseconds(),
	})
}

// sleepFor waits a random duration between the latency bounds the load
// generator sends, honouring client cancellation.
func sleepFor(r *http.Request) time.Duration {
	minMS, _ := strconv.Atoi(r.Header.Get(LatencyMinHeader))
	maxMS, err := strconv.Atoi(r.Header.Get(LatencyMaxHeader))
	if err != nil || maxMS < minMS {
		return 0
	}
	latency := time.Duration(minMS) * time.Millisecond
	if maxMS > minMS {
		latency += time.Duration(rand.IntN(maxMS-minMS+1)) * time.Millisecond
	}
	select {
	case <-time.After(latency):
	case <-r.Context().Done():
	}
	return latency
}

func requestedError(r *http.Request) (int, bool) {
	status, err := strconv.Atoi(r.Header.Get(ErrorHeader))
	if err != nil || status < 400 || status > 599 {
		return 0, false
	}
	return status, true
}

func flattenHeaders(header http.Header) map[string]string {
	result := make(map[string]string, len(header))
	for name, values := range header {
		result[name] = strings.Join(values, ", ")
	}
	return result
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Serve runs the mock upstream on addr until ctx is cancelled.
func Serve(ctx context.Context, addr string, cfg config.Config, out io.Writer) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           NewHandler(cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (is another process using the port?)", addr, err)
	}
	fmt.Fprintf(out, "Mock upstream listening on %s with %d endpoint(s). Press Ctrl+C to stop.\n", listener.Addr(), len(cfg.Endpoints))
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// ListenAddress returns the address the mock binds to for a upstream URL. It
// listens on all interfaces so a containerised data plane can reach it.
func ListenAddress(upstreamURL string) (string, error) {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return "", fmt.Errorf("upstream.default_url: %w", err)
	}
	if parsed.Port() == "" {
		return "", fmt.Errorf("upstream.default_url %q needs an explicit port, such as http://localhost:9000", upstreamURL)
	}
	return ":" + parsed.Port(), nil
}

// IsBuiltin reports whether an upstream URL points at this machine, where the
// built-in mock would normally run. Only the built-in mock honours
// X-Loadgen-Error, so upstream-mode errors depend on this check.
func IsBuiltin(upstreamURL string) bool {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return false
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1", "host.docker.internal":
		return true
	}
	return false
}
