// Package cli parses the deaddrop command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/itsbryanman/DeadDrop/internal/units"
)

// Exit codes. Keep in sync with the README.
const (
	ExitOK      = 0
	ExitRuntime = 1 // unexpected runtime failure
	ExitUsage   = 2 // bad command line
	ExitNetwork = 3 // no usable address, port in use, listener failure
	ExitTunnel  = 4 // cloudflared missing, failed to start, or died
	ExitStorage = 5 // destination directory unusable
)

// Defaults and bounds for `deaddrop receive`.
const (
	DefaultExpires    = 30 * time.Minute
	MaxExpires        = 24 * time.Hour
	DefaultMaxFile    = 2 * units.GiB
	DefaultMaxTotal   = 5 * units.GiB
	DefaultMaxUploads = 20
	MaxUploadsCeiling = 10000
	DefaultLANPort    = 8080
	MaxNameRunes      = 80
)

type Command int

const (
	CommandHelp Command = iota
	CommandVersion
	CommandReceive
)

// Invocation is the result of parsing argv.
type Invocation struct {
	Command Command
	// HelpTopic is "receive" for `deaddrop receive --help`, otherwise empty.
	HelpTopic string
	Receive   ReceiveOptions
}

// ReceiveOptions holds validated flags for `deaddrop receive`.
type ReceiveOptions struct {
	Dir               string // empty means the platform default
	Expires           time.Duration
	MaxFile           int64
	MaxTotal          int64
	MaxUploads        int
	Port              int
	Bind              string
	Tunnel            string // "" or "cloudflare"
	Name              string
	JSON              bool
	JSONIncludeSecret bool
}

// UsageError reports a malformed command line.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

func usagef(format string, args ...any) error {
	return &UsageError{Msg: fmt.Sprintf(format, args...)}
}

// Parse interprets args (without the program name).
func Parse(args []string) (Invocation, error) {
	if len(args) == 0 {
		return Invocation{}, usagef("missing command")
	}
	switch args[0] {
	case "-h", "--help", "help":
		if len(args) > 1 && args[1] == "receive" {
			return Invocation{Command: CommandHelp, HelpTopic: "receive"}, nil
		}
		return Invocation{Command: CommandHelp}, nil
	case "-v", "--version", "version":
		return Invocation{Command: CommandVersion}, nil
	case "receive":
		return parseReceive(args[1:])
	case "send":
		return Invocation{}, usagef("`deaddrop send` is planned for v0.2 and is not part of this release")
	default:
		if strings.HasPrefix(args[0], "-") {
			return Invocation{}, usagef("unknown flag %q (flags go after the command, e.g. `deaddrop receive --port 9000`)", args[0])
		}
		return Invocation{}, usagef("unknown command %q", args[0])
	}
}

func parseReceive(args []string) (Invocation, error) {
	opts := ReceiveOptions{
		Expires:    DefaultExpires,
		MaxFile:    DefaultMaxFile,
		MaxTotal:   DefaultMaxTotal,
		MaxUploads: DefaultMaxUploads,
		Port:       -1,
	}

	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Dir, "dir", "", "")
	fs.DurationVar(&opts.Expires, "expires", DefaultExpires, "")
	fs.Func("max-file", "", sizeFlag(&opts.MaxFile))
	fs.Func("max-total", "", sizeFlag(&opts.MaxTotal))
	fs.IntVar(&opts.MaxUploads, "max-uploads", DefaultMaxUploads, "")
	fs.IntVar(&opts.Port, "port", -1, "")
	fs.StringVar(&opts.Bind, "bind", "", "")
	fs.StringVar(&opts.Tunnel, "tunnel", "", "")
	fs.StringVar(&opts.Name, "name", "", "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	fs.BoolVar(&opts.JSONIncludeSecret, "json-include-secret", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Invocation{Command: CommandHelp, HelpTopic: "receive"}, nil
		}
		return Invocation{}, usagef("%s", cleanFlagError(err))
	}
	if fs.NArg() > 0 {
		return Invocation{}, usagef("unexpected argument %q", fs.Arg(0))
	}

	if opts.Expires <= 0 {
		return Invocation{}, usagef("--expires must be greater than zero")
	}
	if opts.Expires > MaxExpires {
		return Invocation{}, usagef("--expires %s is longer than the 24h maximum", opts.Expires)
	}
	if opts.MaxUploads < 1 || opts.MaxUploads > MaxUploadsCeiling {
		return Invocation{}, usagef("--max-uploads must be between 1 and %d", MaxUploadsCeiling)
	}
	if opts.Port > 65535 || (opts.Port < 0 && opts.Port != -1) {
		return Invocation{}, usagef("--port must be between 0 and 65535")
	}
	switch opts.Tunnel {
	case "", "cloudflare":
	default:
		return Invocation{}, usagef("unsupported --tunnel %q (the only option is \"cloudflare\")", opts.Tunnel)
	}
	if opts.Bind != "" {
		ip := net.ParseIP(strings.Trim(opts.Bind, "[]"))
		if ip == nil {
			return Invocation{}, usagef("--bind %q is not an IP address", opts.Bind)
		}
		if opts.Tunnel != "" && !ip.IsLoopback() {
			return Invocation{}, usagef("--bind must be a loopback address when using --tunnel, so the session is not also exposed on the LAN")
		}
		opts.Bind = ip.String()
	}
	if opts.Port == -1 {
		if opts.Tunnel != "" {
			opts.Port = 0
		} else {
			opts.Port = DefaultLANPort
		}
	}
	name, err := cleanTitle(opts.Name)
	if err != nil {
		return Invocation{}, err
	}
	opts.Name = name
	if opts.JSONIncludeSecret && !opts.JSON {
		return Invocation{}, usagef("--json-include-secret only applies with --json")
	}

	return Invocation{Command: CommandReceive, Receive: opts}, nil
}

func sizeFlag(dst *int64) func(string) error {
	return func(s string) error {
		n, err := units.ParseSize(s)
		if err != nil {
			return err
		}
		*dst = n
		return nil
	}
}

// cleanFlagError rewrites the flag package's messages to use the
// double-dash spelling shown in --help.
func cleanFlagError(err error) string {
	msg := err.Error()
	msg = strings.Replace(msg, "flag provided but not defined: -", "unknown flag --", 1)
	msg = strings.Replace(msg, "flag needs an argument: -", "flag needs a value: --", 1)
	if strings.HasPrefix(msg, "invalid value") {
		msg = strings.Replace(msg, " for flag -", " for --", 1)
	}
	return msg
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !utf8.ValidString(s) {
		return "", usagef("--name must be valid UTF-8")
	}
	if utf8.RuneCountInString(s) > MaxNameRunes {
		return "", usagef("--name is limited to %d characters", MaxNameRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", usagef("--name may not contain control or formatting characters")
		}
	}
	return s, nil
}

// Usage is the top-level help text.
const Usage = `Dead Drop — ephemeral file transfer from a browser to this computer.

Usage:
  deaddrop receive [flags]   Start a temporary upload page and print its link
  deaddrop --help            Show this help
  deaddrop --version         Print the version

Run 'deaddrop receive --help' for receive options.
`

// ReceiveUsage is the help text for `deaddrop receive`.
const ReceiveUsage = `Usage: deaddrop receive [flags]

Starts a temporary, link-protected upload page. Share the printed link or
QR code; press Ctrl+C to end the session.

Flags:
  --dir PATH              Destination base (default ~/Downloads/deaddrop)
  --expires DURATION      Session lifetime, e.g. 10m or 2h (default 30m, max 24h)
  --max-file SIZE         Per-file limit (default 2GiB)
  --max-total SIZE        Total for the session (default 5GiB)
  --max-uploads N         Maximum completed files (default 20)
  --port PORT             Listen port (default 8080 on LAN, random with --tunnel)
  --bind ADDRESS          Listen on this address instead of the detected LAN one
  --tunnel cloudflare     Serve over HTTPS through a Cloudflare quick tunnel
                          (requires cloudflared to be installed)
  --name TEXT             Title shown on the upload page (not verified)
  --json                  Emit machine-readable events, one JSON object per line
  --json-include-secret   Include the secret link in --json output
  --help                  Show this help

Sizes use binary units: B, KiB, MiB, GiB, TiB.

Security: on a LAN the link is plain HTTP. Anyone on the same network who can
observe traffic can see the files and the access key. Use --tunnel cloudflare
or a network you trust for anything sensitive.
`
