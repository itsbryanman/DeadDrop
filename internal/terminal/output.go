// Package terminal renders session status for the operator, either as
// human-readable text or as JSON lines.
package terminal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/width"
	"rsc.io/qr"

	"github.com/itsbryanman/DeadDrop/internal/units"
)

// Printer is safe for concurrent use. It implements httpserver.Reporter.
type Printer struct {
	mu       sync.Mutex
	out      io.Writer
	errOut   io.Writer // text-mode errors; JSON mode keeps everything on out
	json     bool
	tty      bool
	color    bool
	progress bool // a progress line is currently drawn
	lastDraw time.Time
}

// New writes to out, and text-mode errors to errOut. TTY features
// (progress line, QR colours) are enabled only when out is a terminal.
func New(out, errOut *os.File, jsonMode bool) *Printer {
	tty := isTerminal(out)
	return &Printer{
		out:    out,
		errOut: errOut,
		json:   jsonMode,
		tty:    tty && !jsonMode,
		color:  tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
	}
}

// NewWriter is for tests and pipes: no TTY features.
func NewWriter(out io.Writer, jsonMode bool) *Printer {
	return &Printer{out: out, errOut: out, json: jsonMode}
}

// Banner describes a started session.
type Banner struct {
	Network      string // human description of the transport
	Mode         string // "lan" or "tunnel"
	Link         string // includes the secret
	ListenAddr   string
	SaveDir      string
	Limits       string
	MaxFile      int64
	MaxTotal     int64
	MaxUploads   int
	Expires      time.Duration
	ExpiresAt    time.Time
	Name         string
	Warnings     []string
	IncludeLink  bool // JSON mode only: emit the secret link
	DisplaySaved string
}

func (p *Printer) Banner(b Banner) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		ev := map[string]any{
			"network":     b.Mode,
			"listen":      b.ListenAddr,
			"saveDir":     b.SaveDir,
			"expiresAt":   b.ExpiresAt.UTC().Format(time.RFC3339),
			"maxFile":     b.MaxFile,
			"maxTotal":    b.MaxTotal,
			"maxUploads":  b.MaxUploads,
			"linkPrinted": b.IncludeLink,
		}
		if b.Name != "" {
			ev["name"] = b.Name
		}
		if b.IncludeLink {
			ev["link"] = b.Link
		}
		p.emit("session_started", ev)
		for _, w := range b.Warnings {
			p.emit("warning", map[string]any{"message": w})
		}
		return
	}

	w := p.out
	fmt.Fprintln(w)
	fmt.Fprintln(w, p.bold("Dead Drop is receiving files"))
	if b.Name != "" {
		fmt.Fprintf(w, "  Title:    %s %s\n", b.Name, p.dim("(shown to senders)"))
	}
	fmt.Fprintf(w, "  Network:  %s\n", b.Network)
	fmt.Fprintf(w, "  Link:     %s\n", b.Link)
	fmt.Fprintf(w, "  Saving:   %s\n", b.DisplaySaved)
	fmt.Fprintf(w, "  Limit:    %s\n", b.Limits)
	fmt.Fprintf(w, "  Expires:  %s %s\n", humanDuration(b.Expires), p.dim("(at "+b.ExpiresAt.Format("15:04")+")"))
	for _, warn := range b.Warnings {
		fmt.Fprintf(w, "  %s %s\n", p.yellow("!"), warn)
	}
	if p.tty {
		if q, err := RenderQR(b.Link, p.color); err == nil {
			fmt.Fprintln(w)
			fmt.Fprint(w, q)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Waiting for files. Press Ctrl+C to end this session.")
}

// Warn prints an operator-facing warning.
func (p *Printer) Warn(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		p.emit("warning", map[string]any{"message": msg})
		return
	}
	p.clearProgress()
	fmt.Fprintf(p.out, "%s %s\n", p.yellow("!"), msg)
}

// Error prints a fatal error with an optional hint.
func (p *Printer) Error(msg, hint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		ev := map[string]any{"message": msg}
		if hint != "" {
			ev["hint"] = hint
		}
		p.emit("error", ev)
		return
	}
	p.clearProgress()
	fmt.Fprintf(p.errOut, "%s %s\n", p.red("error:"), msg)
	if hint != "" {
		fmt.Fprintf(p.errOut, "  %s\n", hint)
	}
}

// Status prints an informational line.
func (p *Printer) Status(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		p.emit("status", map[string]any{"message": msg})
		return
	}
	p.clearProgress()
	fmt.Fprintln(p.out, p.dim(msg))
}

// Summary reports the end of the session.
func (p *Printer) Summary(reason string, files int, bytes int64, dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		p.emit("session_ended", map[string]any{"reason": reason, "files": files, "bytes": bytes, "saveDir": dir})
		return
	}
	p.clearProgress()
	if p.tty {
		// wipe the "^C" the terminal echoed so the summary starts cleanly
		fmt.Fprint(p.out, "\r\x1b[K")
	}
	head := "Session ended."
	if reason == "expired" {
		head = "Session expired."
	}
	switch files {
	case 0:
		fmt.Fprintf(p.out, "%s No files received.\n", head)
	case 1:
		fmt.Fprintf(p.out, "%s Received 1 file (%s) in %s\n", head, units.FormatSize(bytes), dir)
	default:
		fmt.Fprintf(p.out, "%s Received %d files (%s) in %s\n", head, files, units.FormatSize(bytes), dir)
	}
}

// UploadStarted implements httpserver.Reporter.
func (p *Printer) UploadStarted(name string, size int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		ev := map[string]any{"name": name}
		if size >= 0 {
			ev["size"] = size
		}
		p.emit("upload_started", ev)
		return
	}
	p.drawProgress(name, 0, size)
}

// UploadProgress implements httpserver.Reporter.
func (p *Printer) UploadProgress(name string, written, size int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	interval := 150 * time.Millisecond
	if p.json {
		interval = time.Second
	}
	if time.Since(p.lastDraw) < interval {
		return
	}
	if p.json {
		p.lastDraw = time.Now()
		p.emit("upload_progress", map[string]any{"name": name, "written": written, "size": size})
		return
	}
	p.drawProgress(name, written, size)
}

// UploadSaved implements httpserver.Reporter.
func (p *Printer) UploadSaved(name string, size int64, elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		p.emit("upload_saved", map[string]any{"name": name, "size": size, "seconds": elapsed.Seconds()})
		return
	}
	p.clearProgress()
	fmt.Fprintf(p.out, "%s %s %10s    saved\n", p.green("✓"), pad(name, 24), units.FormatSize(size))
}

// UploadFailed implements httpserver.Reporter.
func (p *Printer) UploadFailed(name, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.json {
		p.emit("upload_failed", map[string]any{"name": name, "reason": reason})
		return
	}
	p.clearProgress()
	fmt.Fprintf(p.out, "%s %s %10s    %s\n", p.red("✗"), pad(name, 24), "", reason)
}

func (p *Printer) drawProgress(name string, written, size int64) {
	if !p.tty {
		return
	}
	p.lastDraw = time.Now()
	var detail string
	if size > 0 {
		detail = fmt.Sprintf("%3d%%  %s / %s", written*100/size, units.FormatSize(written), units.FormatSize(size))
	} else {
		detail = units.FormatSize(written)
	}
	fmt.Fprintf(p.out, "\r\x1b[K%s %s %s", p.dim("↑"), pad(name, 24), p.dim(detail))
	p.progress = true
}

func (p *Printer) clearProgress() {
	if p.progress {
		fmt.Fprint(p.out, "\r\x1b[K")
		p.progress = false
	}
}

func (p *Printer) emit(event string, fields map[string]any) {
	fields["event"] = event
	fields["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(fields)
	p.out.Write(append(b, '\n'))
}

func (p *Printer) style(code, s string) string {
	if !p.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p *Printer) bold(s string) string   { return p.style("1", s) }
func (p *Printer) dim(s string) string    { return p.style("2", s) }
func (p *Printer) red(s string) string    { return p.style("31", s) }
func (p *Printer) green(s string) string  { return p.style("32", s) }
func (p *Printer) yellow(s string) string { return p.style("33", s) }

// pad truncates or pads s to cols terminal columns. East Asian wide and
// fullwidth characters take two columns.
func pad(s string, cols int) string {
	if w := displayWidth(s); w <= cols {
		return s + strings.Repeat(" ", cols-w)
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runeWidth(r)
		if used+w > cols-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…" + strings.Repeat(" ", cols-1-used)
}

func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

func humanDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		h := int(d / time.Hour)
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	case d%time.Minute == 0:
		m := int(d / time.Minute)
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	default:
		return d.String()
	}
}

// RenderQR draws url as a QR code using half-block characters, two
// modules per character row. With colour it sets explicit black-on-white
// so it scans on dark and light terminal themes alike; without colour it
// assumes light text on a dark background.
func RenderQR(url string, color bool) (string, error) {
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return "", err
	}
	// The QR spec asks for a four-module light border; scanners struggle
	// when the terminal's dark background sits right against the code.
	const quiet = 4
	n := code.Size
	dark := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return false
		}
		return code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString("  ")
		if color {
			// bright white (107): plain white (47) renders as mid grey in
			// many terminal palettes, which costs scanner contrast
			b.WriteString("\x1b[30;107m")
		}
		for x := -quiet; x < n+quiet; x++ {
			top, bot := dark(x, y), dark(x, y+1)
			if color {
				// foreground draws the top half; black = dark module
				switch {
				case top && bot:
					b.WriteString("█")
				case top:
					b.WriteString("▀")
				case bot:
					b.WriteString("▄")
				default:
					b.WriteString(" ")
				}
				continue
			}
			// light glyphs on a dark terminal: draw the light modules
			switch {
			case !top && !bot:
				b.WriteString("█")
			case !top:
				b.WriteString("▀")
			case !bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
