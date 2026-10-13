package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newDir(t *testing.T) *Dir {
	t.Helper()
	d, err := Create(t.TempDir(), "session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func write(t *testing.T, d *Dir, data string) *Partial {
	t.Helper()
	p, err := d.CreatePartial()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	return p
}

func listNames(t *testing.T, d *Dir) []string {
	t.Helper()
	es, err := os.ReadDir(d.Path())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestCreateRejectsExisting(t *testing.T) {
	base := t.TempDir()
	d, err := Create(base, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := Create(base, "s"); err == nil {
		t.Fatal("second Create of the same id succeeded")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(d.Path())
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("session dir mode %v", info.Mode().Perm())
		}
	}
}

func TestCommit(t *testing.T) {
	d := newDir(t)
	p := write(t, d, "hello")
	if err := p.Commit("hello.txt"); err != nil {
		t.Fatal(err)
	}
	p.Discard() // no-op after commit
	b, err := os.ReadFile(filepath.Join(d.Path(), "hello.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("read back %q, %v", b, err)
	}
	if names := listNames(t, d); len(names) != 1 || names[0] != "hello.txt" {
		t.Fatalf("directory has %v; partial should be gone", names)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(d.Path(), "hello.txt"))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", info.Mode().Perm())
		}
	}
}

func TestCommitNeverOverwrites(t *testing.T) {
	d := newDir(t)
	if err := write(t, d, "first").Commit("a.txt"); err != nil {
		t.Fatal(err)
	}
	p := write(t, d, "second")
	if err := p.Commit("a.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v, want ErrExists", err)
	}
	p.Discard()
	if err := write(t, d, "third").Commit("A.TXT"); !errors.Is(err, ErrExists) {
		t.Fatalf("case variant: got %v, want ErrExists", err)
	}
	b, _ := os.ReadFile(filepath.Join(d.Path(), "a.txt"))
	if string(b) != "first" {
		t.Fatalf("content changed to %q", b)
	}
}

func TestCommitRespectsFilesNotCreatedByUs(t *testing.T) {
	d := newDir(t)
	if err := os.WriteFile(filepath.Join(d.Path(), "x.bin"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !d.Exists("x.bin") {
		t.Fatal("Exists missed a file on disk")
	}
	p := write(t, d, "inside")
	if err := p.Commit("x.bin"); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
	p.Discard()
	b, _ := os.ReadFile(filepath.Join(d.Path(), "x.bin"))
	if string(b) != "outside" {
		t.Fatal("existing file replaced")
	}
}

func TestConcurrentCommitsSameName(t *testing.T) {
	d := newDir(t)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range n {
		p := write(t, d, strings.Repeat("x", i+1))
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := p.Commit("race.bin")
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else {
				p.Discard()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d commits succeeded, want exactly 1", ok)
	}
	if names := listNames(t, d); len(names) != 1 {
		t.Fatalf("leftovers: %v", names)
	}
}

func TestCopyFallbackIsNoReplace(t *testing.T) {
	d := newDir(t)
	p := write(t, d, "data")
	p.f.Sync()
	p.f.Close()
	p.closed = true
	if err := copyNoReplace(d, p.name, "copied.txt"); err != nil {
		t.Fatal(err)
	}
	if err := copyNoReplace(d, p.name, "copied.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("second copy: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(d.Path(), "copied.txt"))
	if string(b) != "data" {
		t.Fatalf("got %q", b)
	}
	p.Discard()
}

func TestDiscardAndCloseCleanUp(t *testing.T) {
	base := t.TempDir()
	d, err := Create(base, "s")
	if err != nil {
		t.Fatal(err)
	}
	write(t, d, "abandoned")
	write(t, d, "also abandoned")
	if !d.Close() {
		t.Fatal("empty session dir was not removed")
	}
	if _, err := os.Stat(filepath.Join(base, "s")); !os.IsNotExist(err) {
		t.Fatal("directory still exists")
	}

	d2, _ := Create(base, "kept")
	write(t, d2, "x").Commit("x")
	write(t, d2, "partial")
	if d2.Close() {
		t.Fatal("non-empty session dir was removed")
	}
	es, _ := os.ReadDir(filepath.Join(base, "kept"))
	if len(es) != 1 || es[0].Name() != "x" {
		t.Fatalf("after close: %v", es)
	}
}

func TestScanStale(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "old1"), 0o700)
	os.MkdirAll(filepath.Join(base, "old2"), 0o700)
	os.MkdirAll(filepath.Join(base, "clean"), 0o700)
	os.WriteFile(filepath.Join(base, "old1", PartialPrefix+"a"), make([]byte, 10), 0o600)
	os.WriteFile(filepath.Join(base, "old1", PartialPrefix+"b"), make([]byte, 5), 0o600)
	os.WriteFile(filepath.Join(base, "old2", PartialPrefix+"c"), make([]byte, 1), 0o600)
	os.WriteFile(filepath.Join(base, "clean", "done.txt"), make([]byte, 100), 0o600)
	rep := ScanStale(base)
	if rep != (StaleReport{Sessions: 2, Files: 3, Bytes: 16}) {
		t.Fatalf("got %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(base, "old1", PartialPrefix+"a")); err != nil {
		t.Fatal("ScanStale must not delete anything")
	}
}

func TestFreeSpace(t *testing.T) {
	n, err := FreeSpace(t.TempDir())
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("unsupported")
	}
	if err != nil || n == 0 {
		t.Fatalf("FreeSpace = %d, %v", n, err)
	}
}
