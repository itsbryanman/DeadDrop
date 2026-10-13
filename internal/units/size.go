// Package units parses and formats byte sizes using binary (IEC) units.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	KiB int64 = 1 << 10
	MiB int64 = 1 << 20
	GiB int64 = 1 << 30
	TiB int64 = 1 << 40
)

var suffixes = []struct {
	name string
	mult int64
}{
	{"tib", TiB},
	{"gib", GiB},
	{"mib", MiB},
	{"kib", KiB},
	{"b", 1},
}

// ParseSize parses strings such as "2GiB", "512 MiB" or "1048576".
// Decimal units (GB, MB, ...) are rejected rather than guessed at.
func ParseSize(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	mult := int64(1)
	num := s
	for _, u := range suffixes {
		if strings.HasSuffix(lower, u.name) {
			mult = u.mult
			num = strings.TrimSpace(s[:len(s)-len(u.name)])
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		if looksDecimal(lower) {
			return 0, fmt.Errorf("invalid size %q: use binary units like KiB, MiB or GiB", orig)
		}
		return 0, fmt.Errorf("invalid size %q: expected a whole number with an optional unit (B, KiB, MiB, GiB, TiB)", orig)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid size %q: must be greater than zero", orig)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q: too large", orig)
	}
	return n * mult, nil
}

func looksDecimal(s string) bool {
	for _, suf := range []string{"kb", "mb", "gb", "tb", "k", "m", "g", "t"} {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}

// FormatSize renders n with a binary unit, e.g. "8.3 MiB" or "2 GiB".
func FormatSize(n int64) string {
	if n < KiB {
		return fmt.Sprintf("%d B", n)
	}
	units := []struct {
		name string
		mult int64
	}{{"TiB", TiB}, {"GiB", GiB}, {"MiB", MiB}, {"KiB", KiB}}
	for _, u := range units {
		if n >= u.mult {
			if n%u.mult == 0 {
				return fmt.Sprintf("%d %s", n/u.mult, u.name)
			}
			v := float64(n) / float64(u.mult)
			s := strconv.FormatFloat(v, 'f', 1, 64)
			if v >= 100 {
				s = strconv.FormatFloat(v, 'f', 0, 64)
			}
			return s + " " + u.name
		}
	}
	return fmt.Sprintf("%d B", n)
}
