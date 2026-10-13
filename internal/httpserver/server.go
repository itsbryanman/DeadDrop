// Package httpserver serves the upload page and API for one session.
package httpserver

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/itsbryanman/DeadDrop/internal/session"
	"github.com/itsbryanman/DeadDrop/internal/storage"
	"github.com/itsbryanman/DeadDrop/internal/web"
)

// Reporter receives upload lifecycle events, e.g. for terminal output.
// Names passed to it have already been validated.
type Reporter interface {
	UploadStarted(name string, size int64)
	UploadProgress(name string, written, size int64)
	UploadSaved(name string, size int64, elapsed time.Duration)
	UploadFailed(name string, reason string)
}

// Config configures a Server.
type Config struct {
	Session  *session.Session
	Storage  *storage.Dir
	Reporter Reporter
	// StallTimeout aborts an upload when no body bytes arrive for this long.
	// It bounds stuck connections without capping slow but live transfers.
	StallTimeout time.Duration
}

// Server is an http.Handler for the receive session.
type Server struct {
	cfg Config

	mu      sync.RWMutex
	origins map[string]bool
}

func New(cfg Config) *Server {
	if cfg.StallTimeout <= 0 {
		cfg.StallTimeout = 60 * time.Second
	}
	if cfg.Reporter == nil {
		cfg.Reporter = nopReporter{}
	}
	return &Server{cfg: cfg, origins: make(map[string]bool)}
}

// AllowOrigin accepts browser uploads from origin (scheme://host[:port]) in
// addition to the request's own Host, e.g. the public tunnel hostname.
func (s *Server) AllowOrigin(origin string) {
	s.mu.Lock()
	s.origins[origin] = true
	s.mu.Unlock()
}

// HTTPServer wraps s with conservative timeouts. There is deliberately no
// ReadTimeout: large uploads on slow links are legitimate, and stalled
// bodies are handled per-read in the upload handler instead.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", csp)
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

	switch r.URL.Path {
	case "/r":
		s.static(w, r, "text/html; charset=utf-8", web.IndexHTML)
	case "/assets/app.css":
		s.static(w, r, "text/css; charset=utf-8", web.AppCSS)
	case "/assets/app.js":
		s.static(w, r, "text/javascript; charset=utf-8", web.AppJS)
	case "/api/session":
		s.handleSession(w, r)
	case "/api/upload":
		s.handleUpload(w, r)
	case "/healthz":
		s.handleHealth(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	}
}

func (s *Server) static(w http.ResponseWriter, r *http.Request, ctype string, body []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write(body)
	}
}

type sessionResponse struct {
	Name           string    `json:"name,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ServerTime     time.Time `json:"serverTime"`
	MaxFileBytes   int64     `json:"maxFileBytes"`
	MaxTotalBytes  int64     `json:"maxTotalBytes"`
	MaxUploads     int       `json:"maxUploads"`
	ReceivedFiles  int       `json:"receivedFiles"`
	ReceivedBytes  int64     `json:"receivedBytes"`
	UploadInFlight bool      `json:"uploadInFlight"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	if !s.authorized(r) {
		unauthorized(w)
		return
	}
	snap := s.cfg.Session.Snapshot()
	if snap.Closed {
		writeError(w, http.StatusForbidden, "closed", "This drop has closed.")
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		Name:           snap.Name,
		ExpiresAt:      snap.ExpiresAt.UTC(),
		ServerTime:     time.Now().UTC(),
		MaxFileBytes:   snap.Limits.MaxFileBytes,
		MaxTotalBytes:  snap.Limits.MaxTotalBytes,
		MaxUploads:     snap.Limits.MaxUploads,
		ReceivedFiles:  snap.CompletedFiles,
		ReceivedBytes:  snap.CompletedBytes,
		UploadInFlight: snap.Busy,
	})
}

// handleHealth answers readiness probes from this machine only. Requests
// relayed by cloudflared also arrive from loopback, so forwarded ones are
// refused by their proxy headers.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() ||
		r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Cf-Ray") != "" {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="deaddrop"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", "This link's access key is missing or wrong.")
}

type nopReporter struct{}

func (nopReporter) UploadStarted(string, int64)              {}
func (nopReporter) UploadProgress(string, int64, int64)      {}
func (nopReporter) UploadSaved(string, int64, time.Duration) {}
func (nopReporter) UploadFailed(string, string)              {}
