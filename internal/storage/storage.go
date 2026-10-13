// Package storage writes received files into a per-session directory.
//
// Uploads stream into a private ".partial-*" file, are synced, and are then
// committed under their final name without ever replacing an existing file.
package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PartialPrefix marks in-progress uploads. Client names may not use it.
const PartialPrefix = ".partial-"

// ErrExists means the final name is already taken.
var ErrExists = errors.New("a file with that name already exists")

// Dir is one session's destination directory.
type Dir struct {
	path string
	root *os.Root

	mu       sync.Mutex
	taken    map[string]struct{} // collision keys of committed names
	partials map[string]*Partial
}

// Create makes a new, empty session directory base/id with mode 0700.
// It fails if the directory already exists.
func Create(base, id string) (*Dir, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(base, id)
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	return &Dir{
		path:     path,
		root:     root,
		taken:    make(map[string]struct{}),
		partials: make(map[string]*Partial),
	}, nil
}

// Path is the absolute directory path.
func (d *Dir) Path() string { return d.path }

// Exists reports whether name (already cleaned) would collide with a
// committed file or anything else present in the directory.
func (d *Dir) Exists(name string) bool {
	d.mu.Lock()
	_, ok := d.taken[collisionKey(name)]
	d.mu.Unlock()
	if ok {
		return true
	}
	_, err := d.root.Lstat(name)
	return !errors.Is(err, fs.ErrNotExist)
}

// CreatePartial opens a new private temporary file in the directory.
func (d *Dir) CreatePartial() (*Partial, error) {
	for range 5 {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		name := PartialPrefix + hex.EncodeToString(b)
		f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		p := &Partial{d: d, name: name, f: f}
		d.mu.Lock()
		d.partials[name] = p
		d.mu.Unlock()
		return p, nil
	}
	return nil, errors.New("could not create a unique temporary file")
}

// DiscardAll removes any partial files still open. Call it after the HTTP
// server has finished shutting down.
func (d *Dir) DiscardAll() {
	d.mu.Lock()
	ps := make([]*Partial, 0, len(d.partials))
	for _, p := range d.partials {
		ps = append(ps, p)
	}
	d.mu.Unlock()
	for _, p := range ps {
		p.Discard()
	}
}

// Close releases the directory handle and removes the directory if nothing
// was received. It returns true if the directory was removed.
func (d *Dir) Close() (removed bool) {
	d.DiscardAll()
	d.root.Close()
	return os.Remove(d.path) == nil
}

// Partial is an in-progress upload. It is not safe for concurrent use.
type Partial struct {
	d      *Dir
	name   string
	f      *os.File
	closed bool
	done   bool
}

func (p *Partial) Write(b []byte) (int, error) { return p.f.Write(b) }

// Commit makes the data durable and publishes it as final, which must
// come from CleanName. It returns ErrExists if the name is taken; an
// existing file is never replaced. After a successful Commit, Discard
// is a no-op.
func (p *Partial) Commit(final string) error {
	if p.done {
		return errors.New("partial already finished")
	}
	if err := p.f.Sync(); err != nil {
		return err
	}
	p.closed = true
	if err := p.f.Close(); err != nil {
		return err
	}

	d := p.d
	key := collisionKey(final)
	d.mu.Lock()
	if _, ok := d.taken[key]; ok {
		d.mu.Unlock()
		return ErrExists
	}
	d.taken[key] = struct{}{}
	d.mu.Unlock()

	if err := commitNoReplace(d, p.name, final); err != nil {
		d.mu.Lock()
		delete(d.taken, key)
		d.mu.Unlock()
		return err
	}
	p.done = true
	d.root.Remove(p.name)
	d.mu.Lock()
	delete(d.partials, p.name)
	d.mu.Unlock()
	syncDir(d.path)
	return nil
}

// Discard closes and deletes the partial file unless it was committed.
func (p *Partial) Discard() {
	if p.done {
		return
	}
	p.done = true
	if !p.closed {
		p.closed = true
		p.f.Close()
	}
	p.d.root.Remove(p.name)
	p.d.mu.Lock()
	delete(p.d.partials, p.name)
	p.d.mu.Unlock()
}

// commitNoReplace publishes partial as final inside d.
//
// The primary strategy is a hard link: link(2) and CreateHardLink both fail
// atomically if the destination exists, and the final name only ever points
// at fully synced data. On filesystems without hard links (FAT, exFAT, some
// network mounts) it falls back to an exclusive create plus copy, which is
// still no-replace but makes the file visible while it is being copied.
//
// os.Root has no Link in Go 1.24, so the paths are joined directly. That is
// safe because final has passed CleanName (no separators, no "..") and the
// session directory was freshly created with mode 0700.
func commitNoReplace(d *Dir, partial, final string) error {
	err := os.Link(filepath.Join(d.path, partial), filepath.Join(d.path, final))
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	return copyNoReplace(d, partial, final)
}

func copyNoReplace(d *Dir, partial, final string) (err error) {
	in, err := d.root.Open(partial)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := d.root.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			d.root.Remove(final)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// StaleReport summarises leftovers from earlier sessions that did not shut
// down cleanly.
type StaleReport struct {
	Sessions int
	Files    int
	Bytes    int64
}

// ScanStale looks one level below base for session directories holding
// partial files. It only reports; deleting them is left to the operator,
// since another deaddrop process may still be using one.
func ScanStale(base string) StaleReport {
	var rep StaleReport
	entries, err := os.ReadDir(base)
	if err != nil {
		return rep
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner, err := os.ReadDir(filepath.Join(base, e.Name()))
		if err != nil {
			continue
		}
		found := false
		for _, f := range inner {
			if !f.Type().IsRegular() || !strings.HasPrefix(f.Name(), PartialPrefix) {
				continue
			}
			found = true
			rep.Files++
			if info, err := f.Info(); err == nil {
				rep.Bytes += info.Size()
			}
		}
		if found {
			rep.Sessions++
		}
	}
	return rep
}
