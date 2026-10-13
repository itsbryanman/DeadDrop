package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/itsbryanman/DeadDrop/internal/cli"
	"github.com/itsbryanman/DeadDrop/internal/httpserver"
	"github.com/itsbryanman/DeadDrop/internal/network"
	"github.com/itsbryanman/DeadDrop/internal/session"
	"github.com/itsbryanman/DeadDrop/internal/storage"
	"github.com/itsbryanman/DeadDrop/internal/terminal"
	"github.com/itsbryanman/DeadDrop/internal/tunnel"
	"github.com/itsbryanman/DeadDrop/internal/units"
)

const shutdownGrace = 10 * time.Second

func receive(opts cli.ReceiveOptions, stdout, stderr *os.File) int {
	out := terminal.New(stdout, stderr, opts.JSON)

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	base, err := resolveDir(opts.Dir)
	if err != nil {
		out.Error(err.Error(), "Pass a destination with --dir.")
		return cli.ExitStorage
	}

	// Decide where to listen and what host goes in the link.
	var listenHost, linkHost string
	var warnings []string
	tunnelMode := opts.Tunnel == "cloudflare"
	switch {
	case tunnelMode:
		listenHost = "127.0.0.1"
		if opts.Bind != "" {
			listenHost = opts.Bind
		}
	case opts.Bind != "":
		listenHost = opts.Bind
		linkHost = opts.Bind
		ip := net.ParseIP(opts.Bind)
		switch {
		case ip.IsUnspecified():
			sel, err := network.SelectLAN()
			if err != nil {
				out.Error("listening on all interfaces, but could not pick an address for the link: "+err.Error(),
					"Bind a specific address instead, e.g. --bind 192.168.1.20.")
				return cli.ExitNetwork
			}
			linkHost = sel.Chosen.IP.String()
			warnings = append(warnings, "Listening on every network interface, not just "+linkHost+".")
		case ip.IsLoopback():
			warnings = append(warnings, "Bound to loopback: only this computer can open the link.")
		case !ip.IsPrivate():
			warnings = append(warnings, opts.Bind+" is not a private LAN address. Anyone who can reach it and has the link can upload.")
		}
	default:
		sel, err := network.SelectLAN()
		if err != nil {
			out.Error("couldn't find a local network address to share: "+err.Error(),
				"Connect to Wi-Fi or Ethernet, pass --bind ADDRESS, or use --tunnel cloudflare.")
			return cli.ExitNetwork
		}
		listenHost = sel.Chosen.IP.String()
		linkHost = listenHost
		warnings = append(warnings, sel.Warnings...)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(opts.Port)))
	if err != nil {
		hint := ""
		if errors.Is(err, syscall.EADDRINUSE) {
			hint = fmt.Sprintf("Port %d is already in use. Pick another with --port, or --port 0 for any free port.", opts.Port)
		}
		out.Error("could not start the server: "+err.Error(), hint)
		return cli.ExitNetwork
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if rep := storage.ScanStale(base); rep.Files > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"Found %d unfinished upload(s) (%s) from %d earlier session(s) in %s. They are safe to delete once no other deaddrop is running.",
			rep.Files, units.FormatSize(rep.Bytes), rep.Sessions, base))
	}

	limits := session.Limits{MaxFileBytes: opts.MaxFile, MaxTotalBytes: opts.MaxTotal, MaxUploads: opts.MaxUploads}
	sess, err := session.New(ctx, opts.Name, limits, opts.Expires)
	if err != nil {
		out.Error("could not create a session: "+err.Error(), "")
		return cli.ExitRuntime
	}
	defer sess.Close()

	dir, err := storage.Create(base, sess.ID)
	if err != nil {
		out.Error("could not create the destination folder: "+err.Error(), "Check that --dir is writable.")
		return cli.ExitStorage
	}
	dirClosed := false
	defer func() {
		if !dirClosed {
			dir.Close()
		}
	}()

	srv := httpserver.New(httpserver.Config{Session: sess, Storage: dir, Reporter: out})
	hs := srv.HTTPServer()
	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(ln) }()

	var tun *tunnel.Tunnel
	var tunnelDone <-chan struct{}
	var link string
	if tunnelMode {
		local := "http://" + net.JoinHostPort(listenHost, strconv.Itoa(port))
		if err := checkHealth(ctx, local); err != nil {
			out.Error("local server did not become ready: "+err.Error(), "")
			hs.Close()
			return cli.ExitNetwork
		}
		out.Status("Starting Cloudflare tunnel…")
		tun, err = tunnel.Start(ctx, tunnel.Options{LocalURL: local})
		if err != nil {
			hs.Close()
			if ctx.Err() != nil {
				return cli.ExitOK
			}
			hint := ""
			if errors.Is(err, tunnel.ErrNotInstalled) {
				hint = "Install it from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/ or run without --tunnel."
			}
			out.Error(err.Error(), hint)
			return cli.ExitTunnel
		}
		defer tun.Stop(5 * time.Second)
		tunnelDone = tun.Done()
		srv.AllowOrigin(tun.PublicURL)
		link = tun.PublicURL + "/r#" + sess.Token()
		warnings = append([]string{"Quick tunnels are meant for testing and have no uptime guarantee."}, warnings...)
	} else {
		link = "http://" + net.JoinHostPort(linkHost, strconv.Itoa(port)) + "/r#" + sess.Token()
	}

	netLabel := "Trusted LAN — HTTP, not encrypted"
	mode := "lan"
	if tunnelMode {
		netLabel = "Cloudflare quick tunnel — HTTPS to Cloudflare, not end-to-end encrypted"
		mode = "tunnel"
	}
	out.Banner(terminal.Banner{
		Network:      netLabel,
		Mode:         mode,
		Link:         link,
		ListenAddr:   ln.Addr().String(),
		SaveDir:      dir.Path(),
		DisplaySaved: displayPath(dir.Path()) + string(filepath.Separator),
		Limits: fmt.Sprintf("%s/file, %s/session, %d files, one upload at a time",
			units.FormatSize(opts.MaxFile), units.FormatSize(opts.MaxTotal), opts.MaxUploads),
		MaxFile:     opts.MaxFile,
		MaxTotal:    opts.MaxTotal,
		MaxUploads:  opts.MaxUploads,
		Expires:     opts.Expires,
		ExpiresAt:   sess.ExpiresAt,
		Name:        opts.Name,
		Warnings:    warnings,
		IncludeLink: opts.JSONIncludeSecret,
	})

	if opts.Expires > 2*time.Minute {
		t := time.AfterFunc(time.Until(sess.ExpiresAt.Add(-time.Minute)), func() {
			if sess.Context().Err() == nil {
				out.Warn("This session closes in 1 minute.")
			}
		})
		defer t.Stop()
	}

	code := cli.ExitOK
	select {
	case <-sess.Done():
	case err := <-serveErr:
		out.Error("server stopped: "+err.Error(), "")
		code = cli.ExitNetwork
	case <-tunnelDone:
		out.Error("cloudflared exited unexpectedly; the link no longer works", "")
		code = cli.ExitTunnel
	}
	// A second Ctrl+C during shutdown exits immediately.
	stopSignals()

	reason := "ended"
	if sess.Expired() {
		reason = "expired"
	}
	sess.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil {
		hs.Close()
	}
	if tun != nil {
		tun.Stop(5 * time.Second)
	}

	snap := sess.Snapshot()
	savedDir := dir.Path()
	dirClosed = true
	if dir.Close() {
		savedDir = ""
	}
	out.Summary(reason, snap.CompletedFiles, snap.CompletedBytes, displayPath(savedDir))
	return code
}

// resolveDir returns the destination base, defaulting to ~/Downloads/deaddrop.
func resolveDir(dir string) (string, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not find your home directory: %w", err)
		}
		return filepath.Join(home, "Downloads", "deaddrop"), nil
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, dir[1:])
	}
	return filepath.Abs(dir)
}

// displayPath abbreviates the home directory as ~.
func displayPath(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.Join("~", rel)
	}
	return p
}

// checkHealth confirms the listener is serving before cloudflared is
// pointed at it.
func checkHealth(ctx context.Context, base string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	var last error
	for range 20 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return last
}
