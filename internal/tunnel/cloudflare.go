// Package tunnel runs an optional Cloudflare quick tunnel through an
// already installed cloudflared binary.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrNotInstalled means cloudflared is not on PATH.
var ErrNotInstalled = errors.New("cloudflared is not installed")

var hostPattern = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Options configure Start.
type Options struct {
	// LocalURL is the loopback origin to expose, e.g. http://127.0.0.1:41234.
	LocalURL string
	// Binary overrides the cloudflared path (tests).
	Binary string
	// Timeout bounds the wait for a public URL that answers.
	Timeout time.Duration
	// Probe checks that the public URL serves the app. Defaults to a GET
	// of PublicURL+"/r" expecting 200.
	Probe func(ctx context.Context, publicURL string) error
}

// Tunnel is a running cloudflared child process.
type Tunnel struct {
	PublicURL string

	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	logDone chan struct{}

	logMu sync.Mutex
	log   []string
}

// Start launches cloudflared and returns once its public URL serves the
// app. On any failure the child is stopped before returning.
func Start(ctx context.Context, opt Options) (*Tunnel, error) {
	bin := opt.Binary
	if bin == "" {
		p, err := exec.LookPath("cloudflared")
		if err != nil {
			return nil, ErrNotInstalled
		}
		bin = p
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 45 * time.Second
	}
	if opt.Probe == nil {
		opt.Probe = probe
	}

	// --config pointing at an empty file stops a user's named-tunnel config
	// from being picked up by the quick tunnel.
	cmd := exec.Command(bin, "tunnel", "--no-autoupdate", "--config", os.DevNull, "--url", opt.LocalURL)
	configureProcess(cmd)
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("starting cloudflared: %w", err)
	}
	pw.Close()

	t := &Tunnel{cmd: cmd, done: make(chan struct{}), logDone: make(chan struct{})}
	go func() {
		t.err = cmd.Wait()
		close(t.done)
	}()

	found := make(chan string, 1)
	go t.readLog(pr, found)

	deadline := time.Now().Add(opt.Timeout)
	tctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	select {
	case u := <-found:
		t.PublicURL = u
	case <-t.done:
		return nil, fmt.Errorf("cloudflared exited before creating a tunnel%s", t.tail())
	case <-tctx.Done():
		t.Stop(5 * time.Second)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("timed out after %s waiting for cloudflared to create a tunnel%s", opt.Timeout, t.tail())
	}

	// The hostname is printed before Cloudflare routes traffic to it, so
	// poll until the app actually answers through the tunnel.
	var lastErr error
	for {
		pctx, pcancel := context.WithTimeout(tctx, 10*time.Second)
		lastErr = opt.Probe(pctx, t.PublicURL)
		pcancel()
		if lastErr == nil {
			return t, nil
		}
		select {
		case <-t.done:
			return nil, fmt.Errorf("cloudflared exited while the tunnel was starting%s", t.tail())
		case <-tctx.Done():
			t.Stop(5 * time.Second)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("tunnel %s did not become reachable within %s: %v", t.PublicURL, opt.Timeout, lastErr)
		case <-time.After(time.Second):
		}
	}
}

func (t *Tunnel) readLog(r io.ReadCloser, found chan<- string) {
	defer close(t.logDone)
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	sent := false
	for sc.Scan() {
		line := sc.Text()
		t.logMu.Lock()
		t.log = append(t.log, line)
		if len(t.log) > 15 {
			t.log = t.log[1:]
		}
		t.logMu.Unlock()
		if !sent {
			if u := hostPattern.FindString(line); u != "" {
				found <- u
				sent = true
			}
		}
	}
}

// tail formats the last cloudflared log lines for an error message. It
// gives the reader a moment to drain output written just before exit.
func (t *Tunnel) tail() string {
	select {
	case <-t.logDone:
	case <-time.After(time.Second):
	}
	t.logMu.Lock()
	defer t.logMu.Unlock()
	if len(t.log) == 0 {
		return ""
	}
	return "\ncloudflared output:\n  " + strings.Join(t.log, "\n  ")
}

// Done is closed if cloudflared exits.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Stop asks cloudflared (and anything it spawned) to exit, then kills it
// if it has not exited within grace. Only the process this package started
// is signalled.
func (t *Tunnel) Stop(grace time.Duration) {
	select {
	case <-t.done:
		return
	default:
	}
	terminate(t.cmd)
	select {
	case <-t.done:
	case <-time.After(grace):
		kill(t.cmd)
		<-t.done
	}
}

func probe(ctx context.Context, publicURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+"/r", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
