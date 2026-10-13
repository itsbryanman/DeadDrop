package terminal

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rsc.io/qr"
)

func TestJSONBannerOmitsSecretByDefault(t *testing.T) {
	var buf bytes.Buffer
	p := NewWriter(&buf, true)
	p.Banner(Banner{Mode: "lan", Link: "http://192.168.1.2:8080/r#SECRETSECRETSECRET1234", ExpiresAt: time.Now(),
		Warnings: []string{"careful"}})
	if strings.Contains(buf.String(), "SECRET") {
		t.Fatalf("secret leaked: %s", buf.String())
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines: %q", lines)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev["event"] != "session_started" {
		t.Fatalf("%v %v", ev, err)
	}

	buf.Reset()
	p.Banner(Banner{Mode: "lan", Link: "http://x/r#SECRET", IncludeLink: true, ExpiresAt: time.Now()})
	if !strings.Contains(buf.String(), "SECRET") {
		t.Fatal("opt-in link missing")
	}
}

func TestTextEvents(t *testing.T) {
	var buf bytes.Buffer
	p := NewWriter(&buf, false)
	p.UploadStarted("photo.jpg", 100)
	p.UploadSaved("photo.jpg", 8*1024*1024+300*1024, time.Second)
	p.UploadFailed("big.iso", "file too large")
	p.Summary("expired", 1, 8*1024*1024+300*1024, "/tmp/x")
	out := buf.String()
	for _, want := range []string{"✓ photo.jpg", "8.3 MiB", "saved", "✗ big.iso", "file too large", "Session expired. Received 1 file (8.3 MiB)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Error("escape codes written to a non-terminal")
	}
}

func TestRenderQR(t *testing.T) {
	q, err := RenderQR("http://192.168.1.25:8080/r#AAAAAAAAAAAAAAAAAAAAAA", false)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(q, "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("too small: %d lines", len(lines))
	}
	w := len([]rune(lines[0]))
	for _, l := range lines {
		if len([]rune(l)) != w {
			t.Fatal("ragged QR rows")
		}
	}
}

func TestPad(t *testing.T) {
	if got := pad("abc", 5); got != "abc  " {
		t.Errorf("%q", got)
	}
	if got := pad("abcdefgh", 5); got != "abcd…" {
		t.Errorf("%q", got)
	}
}

// TestRenderQRRoundTrip decodes the rendered glyphs back into modules and
// compares them with the encoder's matrix.
func TestRenderQRRoundTrip(t *testing.T) {
	const url = "https://example.trycloudflare.com/r#AAAAAAAAAAAAAAAAAAAAAA"
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	for _, color := range []bool{false, true} {
		out, _ := RenderQR(url, color)
		out = strings.NewReplacer("\x1b[30;47m", "", "\x1b[0m", "").Replace(out)
		rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
		const quiet = 2
		for ri, row := range rows {
			cells := []rune(strings.TrimPrefix(row, "  "))
			for ci, c := range cells {
				var top, bot bool // true = dark module
				switch c {
				case '█':
					top, bot = true, true
				case '▀':
					top = true
				case '▄':
					bot = true
				}
				if !color { // glyphs draw light modules
					top, bot = !top, !bot
				}
				x, y := ci-quiet, ri*2-quiet
				check := func(y int, got bool) {
					want := x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
					if got != want {
						t.Fatalf("color=%v module (%d,%d) = %v, want %v", color, x, y, got, want)
					}
				}
				check(y, top)
				check(y+1, bot)
			}
		}
	}
}
