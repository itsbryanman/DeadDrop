package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/itsbryanman/DeadDrop/internal/units"
)

func TestParseReceiveDefaults(t *testing.T) {
	inv, err := Parse([]string{"receive"})
	if err != nil {
		t.Fatal(err)
	}
	o := inv.Receive
	if inv.Command != CommandReceive || o.Expires != 30*time.Minute || o.MaxFile != 2*units.GiB ||
		o.MaxTotal != 5*units.GiB || o.MaxUploads != 20 || o.Port != 8080 || o.Tunnel != "" {
		t.Fatalf("unexpected defaults: %+v", o)
	}
}

func TestParseReceiveTunnelDefaultsToRandomPort(t *testing.T) {
	inv, err := Parse([]string{"receive", "--tunnel", "cloudflare"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Receive.Port != 0 {
		t.Fatalf("port = %d, want 0", inv.Receive.Port)
	}
}

func TestParseReceiveFlags(t *testing.T) {
	inv, err := Parse([]string{"receive", "--dir", "/tmp/x", "--expires", "2h", "--max-file", "10MiB",
		"--max-total=1GiB", "--max-uploads", "3", "--port", "9000", "--bind", "192.168.1.5",
		"--name", "  Taxes 2026 ", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	o := inv.Receive
	if o.Dir != "/tmp/x" || o.Expires != 2*time.Hour || o.MaxFile != 10*units.MiB || o.MaxTotal != units.GiB ||
		o.MaxUploads != 3 || o.Port != 9000 || o.Bind != "192.168.1.5" || o.Name != "Taxes 2026" || !o.JSON {
		t.Fatalf("unexpected: %+v", o)
	}
}

func TestParseErrors(t *testing.T) {
	bad := [][]string{
		{},
		{"bogus"},
		{"--port", "1"},
		{"send", "./folder"},
		{"receive", "--nope"},
		{"receive", "extra"},
		{"receive", "--expires", "0s"},
		{"receive", "--expires", "25h"},
		{"receive", "--expires", "soon"},
		{"receive", "--max-file", "2GB"},
		{"receive", "--max-total", "0"},
		{"receive", "--max-uploads", "0"},
		{"receive", "--port", "70000"},
		{"receive", "--bind", "localhost"},
		{"receive", "--tunnel", "ngrok"},
		{"receive", "--tunnel", "cloudflare", "--bind", "192.168.1.5"},
		{"receive", "--name", "bad\x07name"},
		{"receive", "--name", "rtl‮txt"},
		{"receive", "--json-include-secret"},
	}
	for _, args := range bad {
		_, err := Parse(args)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("Parse(%q) error = %v, want UsageError", args, err)
		}
	}
}

func TestParseHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		inv, err := Parse(args)
		if err != nil || inv.Command != CommandHelp || inv.HelpTopic != "" {
			t.Errorf("Parse(%q) = %+v, %v", args, inv, err)
		}
	}
	inv, err := Parse([]string{"receive", "--help"})
	if err != nil || inv.Command != CommandHelp || inv.HelpTopic != "receive" {
		t.Errorf("receive --help = %+v, %v", inv, err)
	}
	inv, err = Parse([]string{"--version"})
	if err != nil || inv.Command != CommandVersion {
		t.Errorf("--version = %+v, %v", inv, err)
	}
}

func TestFlagErrorsUseDoubleDash(t *testing.T) {
	_, err := Parse([]string{"receive", "--nope"})
	if err == nil || err.Error() != "unknown flag --nope" {
		t.Fatalf("got %v", err)
	}
}
