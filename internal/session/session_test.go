package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTest(t *testing.T, l Limits) *Session {
	t.Helper()
	s, err := New(context.Background(), "", l, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestTokenCheck(t *testing.T) {
	s := newTest(t, Limits{10, 10, 1})
	tok := s.Token()
	if len(tok) != 22 {
		t.Fatalf("token length %d", len(tok))
	}
	if !s.CheckToken(tok) {
		t.Fatal("valid token rejected")
	}
	flipped := []byte(tok)
	if flipped[0] == 'A' {
		flipped[0] = 'B'
	} else {
		flipped[0] = 'A'
	}
	for _, bad := range []string{"", "x", tok + "A", tok[:21], string(flipped), strings.Repeat("=", 22), tok[:21] + "!"} {
		if s.CheckToken(bad) {
			t.Errorf("CheckToken(%q) = true", bad)
		}
	}
	if strings.Contains(s.ID, tok) {
		t.Fatal("session ID must not contain the token")
	}
}

func TestSingleSlot(t *testing.T) {
	s := newTest(t, Limits{100, 1000, 10})
	t1, err := s.Admit(10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(10); !errors.Is(err, ErrBusy) {
		t.Fatalf("second admit: %v", err)
	}
	t1.Abort()
	t2, err := s.Admit(10)
	if err != nil {
		t.Fatalf("after abort: %v", err)
	}
	t2.Commit(10)
}

func TestQuotaAccounting(t *testing.T) {
	s := newTest(t, Limits{MaxFileBytes: 60, MaxTotalBytes: 100, MaxUploads: 10})

	tk, err := s.Admit(-1)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Limit() != 60 {
		t.Fatalf("limit %d, want per-file cap 60", tk.Limit())
	}
	tk.Commit(50)
	tk.Abort() // no double refund
	if snap := s.Snapshot(); snap.CompletedBytes != 50 || snap.CompletedFiles != 1 || snap.Busy {
		t.Fatalf("snapshot %+v", snap)
	}

	tk, err = s.Admit(-1)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Limit() != 50 {
		t.Fatalf("limit %d, want remaining session quota 50", tk.Limit())
	}
	tk.Abort() // failed upload: nothing counted
	if snap := s.Snapshot(); snap.CompletedBytes != 50 || snap.CompletedFiles != 1 {
		t.Fatalf("abort leaked: %+v", snap)
	}

	if _, err := s.Admit(51); !errors.Is(err, ErrQuota) {
		t.Fatalf("declared over remaining: %v", err)
	}
	if _, err := s.Admit(61); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("declared over per-file: %v", err)
	}

	tk, _ = s.Admit(50)
	tk.Commit(50)
	if _, err := s.Admit(-1); !errors.Is(err, ErrQuota) {
		t.Fatalf("unknown length with full quota: %v", err)
	}
	// Empty files are still allowed once bytes run out.
	tk, err = s.Admit(0)
	if err != nil {
		t.Fatalf("empty file at full quota: %v", err)
	}
	tk.Commit(0)
}

func TestFileCount(t *testing.T) {
	s := newTest(t, Limits{10, 100, 2})
	for i := 0; i < 2; i++ {
		tk, err := s.Admit(1)
		if err != nil {
			t.Fatal(err)
		}
		tk.Commit(1)
	}
	if _, err := s.Admit(1); !errors.Is(err, ErrFileCount) {
		t.Fatalf("got %v", err)
	}
}

func TestClosedAndExpiry(t *testing.T) {
	s, err := New(context.Background(), "", Limits{10, 10, 10}, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	if !s.Expired() {
		t.Fatal("expected expiry")
	}
	if _, err := s.Admit(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}

	s2 := newTest(t, Limits{10, 10, 10})
	s2.Close()
	if s2.Expired() {
		t.Fatal("Close is not expiry")
	}
	if _, err := s2.Admit(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}
