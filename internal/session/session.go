// Package session tracks one receive session: its secret, lifetime, and the
// single-upload admission and quota accounting.
package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// TokenBytes is the size of the bearer secret (128 bits).
const TokenBytes = 16

var tokenEncoding = base64.RawURLEncoding.Strict()

var (
	ErrBusy         = errors.New("another upload is in progress")
	ErrClosed       = errors.New("the session has ended")
	ErrFileCount    = errors.New("the session's file limit has been reached")
	ErrFileTooLarge = errors.New("the file is larger than the per-file limit")
	ErrQuota        = errors.New("the session's storage limit has been reached")
)

// Limits bounds what a session will accept.
type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxUploads    int
}

// Session is safe for concurrent use.
type Session struct {
	// ID names the storage folder. It is not secret and is unrelated to the token.
	ID        string
	Name      string
	Limits    Limits
	StartedAt time.Time
	ExpiresAt time.Time

	token  []byte
	ctx    context.Context
	cancel context.CancelFunc

	// slot holds one value while an upload is admitted.
	slot chan struct{}

	mu             sync.Mutex
	closed         bool
	completedFiles int
	completedBytes int64
	reservedFiles  int
	reservedBytes  int64
}

// New starts a session that ends when ttl elapses, Close is called, or
// parent is cancelled.
func New(parent context.Context, name string, limits Limits, ttl time.Duration) (*Session, error) {
	tok := make([]byte, TokenBytes)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	idRand := make([]byte, 8)
	if _, err := rand.Read(idRand); err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID:        now.Format("20060102-150405") + "-" + hex.EncodeToString(idRand),
		Name:      name,
		Limits:    limits,
		StartedAt: now,
		ExpiresAt: now.Add(ttl),
		token:     tok,
		slot:      make(chan struct{}, 1),
	}
	s.ctx, s.cancel = context.WithDeadline(parent, s.ExpiresAt)
	context.AfterFunc(s.ctx, func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	})
	return s, nil
}

// Token returns the encoded bearer secret for building the share link.
func (s *Session) Token() string { return tokenEncoding.EncodeToString(s.token) }

// CheckToken reports whether presented is this session's secret. Malformed
// input is rejected the same way as a wrong token.
func (s *Session) CheckToken(presented string) bool {
	if len(presented) != tokenEncoding.EncodedLen(TokenBytes) {
		return false
	}
	b, err := tokenEncoding.DecodeString(presented)
	if err != nil || len(b) != TokenBytes {
		return false
	}
	return subtle.ConstantTimeCompare(b, s.token) == 1
}

// Context is cancelled when the session ends.
func (s *Session) Context() context.Context { return s.ctx }

// Done is closed when the session ends.
func (s *Session) Done() <-chan struct{} { return s.ctx.Done() }

// Expired reports whether the session ended because its deadline passed.
func (s *Session) Expired() bool {
	return errors.Is(s.ctx.Err(), context.DeadlineExceeded)
}

// Close ends the session. Active uploads see their context cancelled.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
}

// Snapshot is a point-in-time view of public counters.
type Snapshot struct {
	Name           string
	ExpiresAt      time.Time
	Limits         Limits
	CompletedFiles int
	CompletedBytes int64
	Busy           bool
	Closed         bool
}

func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Name:           s.Name,
		ExpiresAt:      s.ExpiresAt,
		Limits:         s.Limits,
		CompletedFiles: s.completedFiles,
		CompletedBytes: s.completedBytes,
		Busy:           s.reservedFiles > 0,
		Closed:         s.closed || s.ctx.Err() != nil,
	}
}

// Admit claims the upload slot and reserves quota for one file.
// declared is the Content-Length, or -1 when unknown. On success the
// caller must call exactly one of Ticket.Commit or Ticket.Abort.
func (s *Session) Admit(declared int64) (*Ticket, error) {
	select {
	case s.slot <- struct{}{}:
	default:
		return nil, ErrBusy
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(err error) (*Ticket, error) {
		<-s.slot
		return nil, err
	}
	// The deadline's AfterFunc sets closed asynchronously; check the
	// context too so an expired session never admits another upload.
	if s.closed || s.ctx.Err() != nil {
		return fail(ErrClosed)
	}
	if s.completedFiles+s.reservedFiles >= s.Limits.MaxUploads {
		return fail(ErrFileCount)
	}
	if declared > s.Limits.MaxFileBytes {
		return fail(ErrFileTooLarge)
	}
	remaining := s.Limits.MaxTotalBytes - s.completedBytes - s.reservedBytes
	if declared > remaining || (declared < 0 && remaining <= 0) {
		return fail(ErrQuota)
	}
	limit := min(s.Limits.MaxFileBytes, max(remaining, 0))
	s.reservedFiles++
	s.reservedBytes += limit
	return &Ticket{s: s, limit: limit}, nil
}

// Ticket is an admitted upload holding the slot and a byte reservation.
type Ticket struct {
	s     *Session
	limit int64
	done  bool
}

// Limit is the most bytes this upload may write.
func (t *Ticket) Limit() int64 { return t.limit }

// Commit records a stored file of size n and releases the reservation.
// n must not exceed Limit.
func (t *Ticket) Commit(n int64) {
	t.finish(func(s *Session) {
		s.completedFiles++
		s.completedBytes += n
	})
}

// Abort releases the reservation without recording anything. It is safe
// to call after Commit, which makes `defer t.Abort()` convenient.
func (t *Ticket) Abort() { t.finish(nil) }

func (t *Ticket) finish(record func(*Session)) {
	s := t.s
	s.mu.Lock()
	if t.done {
		s.mu.Unlock()
		return
	}
	t.done = true
	s.reservedFiles--
	s.reservedBytes -= t.limit
	if record != nil {
		record(s)
	}
	s.mu.Unlock()
	<-s.slot
}
