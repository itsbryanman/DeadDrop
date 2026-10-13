package storage

import (
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// MaxNameBytes is the longest accepted filename, in UTF-8 bytes. Most
// filesystems cap a path component at 255 bytes.
const MaxNameBytes = 255

// NameError explains why a client-supplied filename was refused.
type NameError struct{ Reason string }

func (e *NameError) Error() string { return "invalid filename: " + e.Reason }

// IsNameError reports whether err came from CleanName.
func IsNameError(err error) bool {
	var ne *NameError
	return errors.As(err, &ne)
}

// CleanName validates a client-supplied filename and returns its NFC form.
// It never repairs a name: anything that could be read as a path, a device,
// or a confusable on some platform is rejected outright, on every platform,
// so behaviour does not depend on where the receiver runs.
func CleanName(raw string) (string, error) {
	if raw == "" {
		return "", &NameError{"empty name"}
	}
	if !utf8.ValidString(raw) {
		return "", &NameError{"not valid UTF-8"}
	}
	name := norm.NFC.String(raw)
	if len(name) > MaxNameBytes {
		return "", &NameError{"name is too long"}
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			return "", &NameError{"contains a path separator"}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return "", &NameError{"contains a control character"}
		case strings.ContainsRune(`<>:"|?*`, r):
			return "", &NameError{"contains a character Windows does not allow (< > : \" | ? *)"}
		case isInvisibleFormat(r):
			return "", &NameError{"contains an invisible formatting character"}
		}
	}
	if name == "." || name == ".." {
		return "", &NameError{"reserved name"}
	}
	if strings.HasPrefix(name, PartialPrefix) {
		return "", &NameError{"reserved prefix"}
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return "", &NameError{"ends with a space or dot"}
	}
	if strings.HasPrefix(name, " ") {
		return "", &NameError{"starts with a space"}
	}
	if isWindowsDevice(name) {
		return "", &NameError{"reserved device name"}
	}
	return name, nil
}

// isInvisibleFormat covers bidi overrides and zero-width characters that
// make a name display differently from what it is.
func isInvisibleFormat(r rune) bool {
	switch {
	case r >= 0x200b && r <= 0x200f, // zero-width space/joiners, LRM, RLM
		r >= 0x202a && r <= 0x202e, // bidi embeddings and overrides
		r >= 0x2066 && r <= 0x2069, // bidi isolates
		r == 0x061c, r == 0xfeff:
		return true
	}
	return false
}

var windowsDevices = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"COM¹": true, "COM²": true, "COM³": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"LPT¹": true, "LPT²": true, "LPT³": true,
}

func isWindowsDevice(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	return windowsDevices[strings.ToUpper(strings.TrimRight(stem, " "))]
}

var folder = cases.Fold()

// collisionKey maps names that would collide on a case- or
// normalization-insensitive filesystem to the same key.
func collisionKey(name string) string {
	return folder.String(norm.NFC.String(name))
}
