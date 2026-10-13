//go:build unix

package tunnel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeCloudflared writes a shell script standing in for cloudflared.
func fakeCloudflared(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func okProbe(context.Context, string) error { return nil }

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestStartFindsURLAndStopsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	bin := fakeCloudflared(t, `
case "$*" in *"--url http://127.0.0.1:1234"*) ;; *) echo "bad args: $*" >&2; exit 3;; esac
sleep 300 &
echo $! > `+pidFile+`
echo "INF Requesting new quick Tunnel on trycloudflare.com..." >&2
echo "INF |  https://sunny-river-1234.trycloudflare.com   |" >&2
wait
`)
	tun, err := Start(context.Background(), Options{LocalURL: "http://127.0.0.1:1234", Binary: bin, Timeout: 5 * time.Second, Probe: okProbe})
	if err != nil {
		t.Fatal(err)
	}
	if tun.PublicURL != "https://sunny-river-1234.trycloudflare.com" {
		t.Fatalf("url %q", tun.PublicURL)
	}
	b, _ := os.ReadFile(pidFile)
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if child == 0 || !alive(child) {
		t.Fatal("helper child not running")
	}
	tun.Stop(2 * time.Second)
	select {
	case <-tun.Done():
	default:
		t.Fatal("not stopped")
	}
	deadline := time.Now().Add(2 * time.Second)
	for alive(child) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(child) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Fatal("child of cloudflared survived Stop")
	}
}

func TestStartKillsIfTermIgnored(t *testing.T) {
	bin := fakeCloudflared(t, `
trap '' TERM
echo "https://stubborn.trycloudflare.com" >&2
while :; do sleep 1; done
`)
	tun, err := Start(context.Background(), Options{LocalURL: "http://127.0.0.1:1", Binary: bin, Timeout: 5 * time.Second, Probe: okProbe})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	tun.Stop(300 * time.Millisecond)
	if time.Since(start) > 3*time.Second {
		t.Fatal("Stop took too long")
	}
}

func TestStartReportsEarlyExit(t *testing.T) {
	bin := fakeCloudflared(t, `echo "ERR failed to request quick Tunnel: 429 Too Many Requests" >&2; exit 1`)
	_, err := Start(context.Background(), Options{LocalURL: "http://127.0.0.1:1", Binary: bin, Timeout: 5 * time.Second, Probe: okProbe})
	if err == nil || !strings.Contains(err.Error(), "429 Too Many Requests") {
		t.Fatalf("got %v", err)
	}
}

func TestStartTimesOut(t *testing.T) {
	bin := fakeCloudflared(t, `exec sleep 300`)
	start := time.Now()
	_, err := Start(context.Background(), Options{LocalURL: "http://127.0.0.1:1", Binary: bin, Timeout: 300 * time.Millisecond, Probe: okProbe})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 7*time.Second {
		t.Fatal("timeout not honoured")
	}
}

func TestStartProbeFailureTimesOut(t *testing.T) {
	bin := fakeCloudflared(t, `echo "https://never-ready.trycloudflare.com" >&2; exec sleep 300`)
	_, err := Start(context.Background(), Options{LocalURL: "http://127.0.0.1:1", Binary: bin, Timeout: 1500 * time.Millisecond,
		Probe: func(context.Context, string) error { return errors.New("dns not ready") }})
	if err == nil || !strings.Contains(err.Error(), "did not become reachable") {
		t.Fatalf("got %v", err)
	}
}

func TestStartNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Start(context.Background(), Options{LocalURL: "http://127.0.0.1:1"})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("got %v", err)
	}
}
