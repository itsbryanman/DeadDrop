package units

import "testing"

func TestParseSize(t *testing.T) {
	good := map[string]int64{
		"1":       1,
		"512B":    512,
		"10KiB":   10 * KiB,
		"2GiB":    2 * GiB,
		"2gib":    2 * GiB,
		"500 MiB": 500 * MiB,
		"1TiB":    TiB,
	}
	for in, want := range good {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "2GB", "2G", "1.5GiB", "abc", "GiB", "99999999999TiB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) succeeded, want error", in)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		0:                 "0 B",
		1023:              "1023 B",
		KiB:               "1 KiB",
		8*MiB + 300*KiB:   "8.3 MiB",
		2 * GiB:           "2 GiB",
		150*MiB + 800*KiB: "151 MiB",
		5 * GiB:           "5 GiB",
	}
	for in, want := range cases {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}
