package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsbryanman/DeadDrop/internal/session"
	"github.com/itsbryanman/DeadDrop/internal/storage"
)

type env struct {
	t    *testing.T
	sess *session.Session
	dir  *storage.Dir
	srv  *Server
	ts   *httptest.Server
	rep  *recorder
}

type recorder struct {
	mu     sync.Mutex
	saved  []string
	failed []string
}

func (r *recorder) UploadStarted(string, int64)         {}
func (r *recorder) UploadProgress(string, int64, int64) {}
func (r *recorder) UploadSaved(name string, _ int64, _ time.Duration) {
	r.mu.Lock()
	r.saved = append(r.saved, name)
	r.mu.Unlock()
}
func (r *recorder) UploadFailed(name, reason string) {
	r.mu.Lock()
	r.failed = append(r.failed, name+": "+reason)
	r.mu.Unlock()
}

func newEnv(t *testing.T, l session.Limits) *env {
	t.Helper()
	sess, err := session.New(context.Background(), "Test drop", l, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := storage.Create(t.TempDir(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rep := &recorder{}
	srv := New(Config{Session: sess, Storage: dir, Reporter: rep, StallTimeout: 2 * time.Second})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		sess.Close()
		ts.Close()
		dir.Close()
	})
	return &env{t: t, sess: sess, dir: dir, srv: srv, ts: ts, rep: rep}
}

var defaultLimits = session.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxUploads: 5}

func (e *env) upload(name string, body io.Reader, mod func(*http.Request)) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/upload", body)
	req.Header.Set("Authorization", "Bearer "+e.sess.Token())
	req.Header.Set(FileNameHeader, url.PathEscape(name))
	if mod != nil {
		mod(req)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func errCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var b errorBody
	json.NewDecoder(resp.Body).Decode(&b)
	return b.Error
}

func (e *env) files() map[string]string {
	e.t.Helper()
	out := map[string]string{}
	es, _ := os.ReadDir(e.dir.Path())
	for _, f := range es {
		b, _ := os.ReadFile(filepath.Join(e.dir.Path(), f.Name()))
		out[f.Name()] = string(b)
	}
	return out
}

func TestShellIsPublicAndGeneric(t *testing.T) {
	e := newEnv(t, defaultLimits)
	for _, p := range []string{"/r", "/assets/app.css", "/assets/app.js"} {
		resp, err := http.Get(e.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
		if bytes.Contains(body, []byte(e.sess.Token())) || bytes.Contains(body, []byte("Test drop")) || bytes.Contains(body, []byte(e.sess.ID)) {
			t.Errorf("%s leaks session data", p)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t, defaultLimits)
	for _, p := range []string{"/r", "/api/session", "/nope"} {
		resp := get(t, e.ts.URL + p)
		resp.Body.Close()
		h := resp.Header
		if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" ||
			h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			h.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: headers %v", p, h)
		}
	}
}

func TestUnknownRoutes404(t *testing.T) {
	e := newEnv(t, defaultLimits)
	for _, p := range []string{"/", "/r/", "/api", "/assets/", "/assets/../r", "/" + e.sess.ID} {
		resp := get(t, e.ts.URL + p)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
	}
}

func TestSessionRequiresToken(t *testing.T) {
	e := newEnv(t, defaultLimits)
	for _, auth := range []string{"", "Bearer", "Bearer nope", "Basic " + e.sess.Token(), "Bearer " + e.sess.Token() + "x"} {
		req, _ := http.NewRequest("GET", e.ts.URL+"/api/session", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp := do(t, req)
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("auth %q: status %d", auth, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/session", nil)
	req.Header.Set("Authorization", "bearer "+e.sess.Token())
	resp := do(t, req)
	defer resp.Body.Close()
	var sr sessionResponse
	json.NewDecoder(resp.Body).Decode(&sr)
	if resp.StatusCode != 200 || sr.Name != "Test drop" || sr.MaxFileBytes != 1<<20 || sr.MaxUploads != 5 {
		t.Fatalf("status %d, %+v", resp.StatusCode, sr)
	}
}

func TestUploadRequiresToken(t *testing.T) {
	e := newEnv(t, defaultLimits)
	resp := e.upload("a.txt", strings.NewReader("x"), func(r *http.Request) { r.Header.Del("Authorization") })
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(e.files()) != 0 {
		t.Fatal("file written without auth")
	}
}

func TestUploadHappyPath(t *testing.T) {
	e := newEnv(t, defaultLimits)
	resp := e.upload("résumé (1).txt", strings.NewReader("hello"), nil)
	if resp.StatusCode != 201 {
		t.Fatalf("status %d: %s", resp.StatusCode, errCode(t, resp))
	}
	var ur uploadResponse
	json.NewDecoder(resp.Body).Decode(&ur)
	if ur.Name != "résumé (1).txt" || ur.Size != 5 {
		t.Fatalf("%+v", ur)
	}
	if f := e.files(); len(f) != 1 || f["résumé (1).txt"] != "hello" {
		t.Fatalf("files %v", f)
	}
	if s := e.sess.Snapshot(); s.CompletedFiles != 1 || s.CompletedBytes != 5 {
		t.Fatalf("snapshot %+v", s)
	}
}

func TestUploadEmptyFile(t *testing.T) {
	e := newEnv(t, defaultLimits)
	if resp := e.upload("empty", http.NoBody, nil); resp.StatusCode != 201 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if f := e.files(); len(f) != 1 {
		t.Fatalf("%v", f)
	}
}

func TestUploadBadNames(t *testing.T) {
	e := newEnv(t, defaultLimits)
	for _, n := range []string{"", "../etc/passwd", "a/b", "nul", ".partial-x", "x\r\nSet-Cookie: a"} {
		resp := e.upload(n, strings.NewReader("x"), nil)
		if resp.StatusCode != 400 || errCode(t, resp) != "bad_name" {
			t.Errorf("%q: status %d", n, resp.StatusCode)
		}
	}
	resp := e.upload("x", strings.NewReader("x"), func(r *http.Request) { r.Header.Set(FileNameHeader, "%zz") })
	if resp.StatusCode != 400 {
		t.Errorf("bad escape: %d", resp.StatusCode)
	}
	resp = e.upload("x", strings.NewReader("x"), func(r *http.Request) { r.Header.Set(FileNameHeader, "%ff%fe") })
	if resp.StatusCode != 400 {
		t.Errorf("invalid utf-8: %d", resp.StatusCode)
	}
	if len(e.files()) != 0 {
		t.Fatalf("files written: %v", e.files())
	}
}

func TestUploadCollision(t *testing.T) {
	e := newEnv(t, defaultLimits)
	e.upload("a.txt", strings.NewReader("one"), nil)
	resp := e.upload("A.txt", strings.NewReader("two"), nil)
	if resp.StatusCode != 409 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if f := e.files(); len(f) != 1 || f["a.txt"] != "one" {
		t.Fatalf("%v", f)
	}
}

func TestUploadTooLargeDeclared(t *testing.T) {
	e := newEnv(t, defaultLimits)
	resp := e.upload("big", bytes.NewReader(make([]byte, 1<<20+1)), nil)
	if resp.StatusCode != 413 || errCode(t, resp) != "file_too_large" {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// chunked hides the length so the server has to enforce limits while reading.
type chunked struct{ r io.Reader }

func (c chunked) Read(p []byte) (int, error) { return c.r.Read(p) }

func TestUploadTooLargeStreamed(t *testing.T) {
	e := newEnv(t, defaultLimits)
	resp := e.upload("big", chunked{bytes.NewReader(make([]byte, 1<<20+1))}, nil)
	if resp.StatusCode != 413 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(e.files()) != 0 {
		t.Fatalf("partial left behind: %v", e.files())
	}
	if s := e.sess.Snapshot(); s.CompletedBytes != 0 || s.Busy {
		t.Fatalf("reservation leaked: %+v", s)
	}
	// exactly at the limit is fine
	resp = e.upload("exact", chunked{bytes.NewReader(make([]byte, 1<<20))}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("exact limit: %d", resp.StatusCode)
	}
}

func TestUploadSessionQuotaAcrossFiles(t *testing.T) {
	e := newEnv(t, session.Limits{MaxFileBytes: 100, MaxTotalBytes: 150, MaxUploads: 10})
	if r := e.upload("a", bytes.NewReader(make([]byte, 100)), nil); r.StatusCode != 201 {
		t.Fatal(r.StatusCode)
	}
	if r := e.upload("b", bytes.NewReader(make([]byte, 51)), nil); r.StatusCode != 413 {
		t.Fatalf("declared over quota: %d", r.StatusCode)
	}
	if r := e.upload("c", chunked{bytes.NewReader(make([]byte, 51))}, nil); r.StatusCode != 413 {
		t.Fatalf("streamed over quota: %d", r.StatusCode)
	}
	if r := e.upload("d", bytes.NewReader(make([]byte, 50)), nil); r.StatusCode != 201 {
		t.Fatalf("fits remaining: %d", r.StatusCode)
	}
	if s := e.sess.Snapshot(); s.CompletedBytes != 150 || s.CompletedFiles != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestUploadFileCount(t *testing.T) {
	e := newEnv(t, session.Limits{MaxFileBytes: 100, MaxTotalBytes: 1000, MaxUploads: 1})
	e.upload("a", strings.NewReader("x"), nil)
	if r := e.upload("b", strings.NewReader("x"), nil); r.StatusCode != 403 || errCode(t, r) != "file_limit" {
		t.Fatalf("status %d", r.StatusCode)
	}
}

func TestUploadBusy(t *testing.T) {
	e := newEnv(t, defaultLimits)
	pr, pw := io.Pipe()
	done := make(chan *http.Response)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/upload", pr)
		req.Header.Set("Authorization", "Bearer "+e.sess.Token())
		req.Header.Set(FileNameHeader, "slow")
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Error(err)
		}
		done <- resp
	}()
	pw.Write([]byte("partial data"))
	waitFor(t, func() bool { return e.sess.Snapshot().Busy })

	resp := e.upload("second", strings.NewReader("x"), nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d", resp.StatusCode)
	}
	pw.Close()
	if r := <-done; r.StatusCode != 201 {
		t.Fatalf("slow upload: %d", r.StatusCode)
	}
}

func TestUploadClientAbortCleansUp(t *testing.T) {
	e := newEnv(t, defaultLimits)
	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("POST /api/upload HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + e.sess.Token() +
		"\r\nX-File-Name: aborted.bin\r\nContent-Length: 1000\r\n\r\npartial"))
	waitFor(t, func() bool { return e.sess.Snapshot().Busy })
	conn.Close()
	waitFor(t, func() bool { return !e.sess.Snapshot().Busy })
	if f := e.files(); len(f) != 0 {
		t.Fatalf("leftover files: %v", f)
	}
	if s := e.sess.Snapshot(); s.CompletedFiles != 0 || s.CompletedBytes != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestUploadStallTimesOut(t *testing.T) {
	e := newEnv(t, defaultLimits)
	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("POST /api/upload HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + e.sess.Token() +
		"\r\nX-File-Name: stalled.bin\r\nContent-Length: 1000\r\n\r\nsome"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf, _ := io.ReadAll(conn)
	if !bytes.Contains(buf, []byte(" 408 ")) {
		t.Fatalf("got %q", buf)
	}
	if len(e.files()) != 0 {
		t.Fatal("partial left behind")
	}
}

func TestSessionCloseCancelsUpload(t *testing.T) {
	e := newEnv(t, defaultLimits)
	pr, pw := io.Pipe()
	done := make(chan int)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/upload", pr)
		req.Header.Set("Authorization", "Bearer "+e.sess.Token())
		req.Header.Set(FileNameHeader, "cut")
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	pw.Write([]byte("partial"))
	waitFor(t, func() bool { return e.sess.Snapshot().Busy })
	e.sess.Close()
	select {
	case code := <-done:
		if code != 403 && code != -1 {
			t.Fatalf("status %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload not cancelled")
	}
	pw.Close()
	if len(e.files()) != 0 {
		t.Fatal("partial left behind")
	}
	if r := e.upload("after", strings.NewReader("x"), nil); r.StatusCode != 403 {
		t.Fatalf("after close: %d", r.StatusCode)
	}
}

func TestCrossOriginRejected(t *testing.T) {
	e := newEnv(t, defaultLimits)
	cases := []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		func(r *http.Request) { r.Header.Set("Origin", "null") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	}
	for i, mod := range cases {
		if r := e.upload("x", strings.NewReader("x"), mod); r.StatusCode != 403 {
			t.Errorf("case %d: %d", i, r.StatusCode)
		}
	}
	u, _ := url.Parse(e.ts.URL)
	if r := e.upload("same", strings.NewReader("x"), func(r *http.Request) {
		r.Header.Set("Origin", "http://"+u.Host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}); r.StatusCode != 201 {
		t.Errorf("same origin: %d", r.StatusCode)
	}
	e.srv.AllowOrigin("https://abc.trycloudflare.com")
	if r := e.upload("tunnel", strings.NewReader("x"), func(r *http.Request) {
		r.Header.Set("Origin", "https://abc.trycloudflare.com")
	}); r.StatusCode != 201 {
		t.Errorf("allowed origin: %d", r.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, defaultLimits)
	resp := get(t, e.ts.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("loopback: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/healthz", nil)
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	resp = do(t, req)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("tunnelled: %d", resp.StatusCode)
	}
}

func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func get(t *testing.T, u string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return do(t, req)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
