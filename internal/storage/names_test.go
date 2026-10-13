package storage

import (
	"strings"
	"testing"
)

func TestCleanNameAccepts(t *testing.T) {
	for in, want := range map[string]string{
		"photo.jpg":              "photo.jpg",
		"Report (final).PDF":     "Report (final).PDF",
		".bashrc":                ".bashrc",
		"résumé.txt":             "résumé.txt",
		"re\u0301sume\u0301.txt": "résumé.txt", // NFD input is normalised to NFC
		"日本語.md":                 "日本語.md",
		"console.log":            "console.log",
		strings.Repeat("a", 255): strings.Repeat("a", 255),
	} {
		got, err := CleanName(in)
		if err != nil || got != want {
			t.Errorf("CleanName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestCleanNameRejects(t *testing.T) {
	for _, in := range []string{
		"", ".", "..", "../x", "a/b", `a\b`, "/etc/passwd", `C:\x`, "c:x",
		"nul", "NUL.txt", "com1", "Lpt9.tar.gz", "CON ", "conout$",
		"a\x00b", "a\nb", "a\rb", "tab\there", "del\x7f", "c1\u0085",
		"evil\u202etxt.exe", "zero\u200bwidth", "bom\ufeff",
		"trailing.", "trailing ", " leading",
		".partial-abc", "what?", "star*", "pipe|", `quote"`, "<lt", "gt>",
		strings.Repeat("a", 256),
		strings.Repeat("é", 128), // 256 bytes
		"\xff\xfe", "\xc0\xaf",   // invalid / overlong UTF-8
	} {
		if got, err := CleanName(in); err == nil {
			t.Errorf("CleanName(%q) = %q, want error", in, got)
		} else if !IsNameError(err) {
			t.Errorf("CleanName(%q) error %v is not a NameError", in, err)
		}
	}
}

func TestCollisionKey(t *testing.T) {
	if collisionKey("Photo.JPG") != collisionKey("photo.jpg") {
		t.Error("case variants should collide")
	}
	if collisionKey("re\u0301sume\u0301") != collisionKey("résumé") {
		t.Error("normalisation variants should collide")
	}
	if collisionKey("a.txt") == collisionKey("b.txt") {
		t.Error("distinct names collide")
	}
}
