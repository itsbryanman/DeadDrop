package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/itsbryanman/DeadDrop/internal/session"
	"github.com/itsbryanman/DeadDrop/internal/storage"
	"github.com/itsbryanman/DeadDrop/internal/units"
)

// FileNameHeader carries the original filename, percent-encoded UTF-8
// (as produced by JavaScript's encodeURIComponent). Header values are
// effectively Latin-1 in browsers, so raw UTF-8 cannot be sent reliably.
const FileNameHeader = "X-File-Name"

const maxFileNameHeader = 1024

// retryAfterBusy is how long a client should wait while another upload
// holds the single slot.
const retryAfterBusy = 3

type uploadResponse struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !s.authorized(r) {
		unauthorized(w)
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "Uploads must come from the Dead Drop page.")
		return
	}
	sess := s.cfg.Session
	if sess.Context().Err() != nil {
		writeError(w, http.StatusForbidden, "closed", "This drop has closed.")
		return
	}

	raw := r.Header.Get(FileNameHeader)
	if len(raw) > maxFileNameHeader {
		writeError(w, http.StatusBadRequest, "bad_name", "The file name is too long.")
		return
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_name", "The file name is not encoded correctly.")
		return
	}
	name, err := storage.CleanName(decoded)
	if err != nil {
		var ne *storage.NameError
		errors.As(err, &ne)
		writeError(w, http.StatusBadRequest, "bad_name", "This file name can't be used: "+ne.Reason+". Rename the file and try again.")
		return
	}

	declared := r.ContentLength
	limits := sess.Limits
	if declared > limits.MaxFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("Files can be at most %s.", units.FormatSize(limits.MaxFileBytes)))
		return
	}
	if s.cfg.Storage.Exists(name) {
		writeError(w, http.StatusConflict, "exists", "A file with this name was already received. Rename it and try again.")
		return
	}

	ticket, err := sess.Admit(declared)
	if err != nil {
		s.admitError(w, err)
		return
	}
	defer ticket.Abort()

	if free, err := storage.FreeSpace(s.cfg.Storage.Path()); err == nil && declared > 0 && uint64(declared) > free {
		s.cfg.Reporter.UploadFailed(name, "not enough disk space")
		writeError(w, http.StatusInsufficientStorage, "no_space", "The receiving computer doesn't have enough free space.")
		return
	}

	partial, err := s.cfg.Storage.CreatePartial()
	if err != nil {
		s.cfg.Reporter.UploadFailed(name, "could not create a temporary file")
		writeError(w, http.StatusInternalServerError, "io_error", "The receiving computer couldn't save the file.")
		return
	}
	defer partial.Discard()

	// The copy stops when the sender disconnects or the session ends.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rc := http.NewResponseController(w)
	stopOnClose := context.AfterFunc(sess.Context(), func() {
		cancel()
		rc.SetReadDeadline(time.Now()) // unblock a pending Read
	})
	defer stopOnClose()

	start := time.Now()
	s.cfg.Reporter.UploadStarted(name, declared)
	limit := ticket.Limit()
	n, err := copyBody(ctx, partial, io.LimitReader(r.Body, limit+1), rc, s.cfg.StallTimeout,
		func(written int64) { s.cfg.Reporter.UploadProgress(name, written, declared) })
	if err != nil || n > limit {
		// Expire reads now so net/http's post-handler body drain cannot
		// block on a stalled or oversized sender.
		rc.SetReadDeadline(time.Now())
	} else {
		rc.SetReadDeadline(time.Time{})
	}

	if err == nil && n > limit {
		if n > limits.MaxFileBytes {
			s.fail(w, name, http.StatusRequestEntityTooLarge, "file_too_large", "file too large",
				fmt.Sprintf("Files can be at most %s.", units.FormatSize(limits.MaxFileBytes)))
		} else {
			s.fail(w, name, http.StatusRequestEntityTooLarge, "quota", "session storage limit reached",
				"This drop doesn't have room for that file.")
		}
		return
	}
	if err != nil {
		s.copyError(w, name, err, sess)
		return
	}

	if err := partial.Commit(name); err != nil {
		switch {
		case errors.Is(err, storage.ErrExists):
			s.fail(w, name, http.StatusConflict, "exists", "name already taken",
				"A file with this name was already received. Rename it and try again.")
		case storage.IsNoSpace(err):
			s.fail(w, name, http.StatusInsufficientStorage, "no_space", "disk full",
				"The receiving computer ran out of space.")
		default:
			s.fail(w, name, http.StatusInternalServerError, "io_error", "could not save file",
				"The receiving computer couldn't save the file.")
		}
		return
	}
	ticket.Commit(n)
	s.cfg.Reporter.UploadSaved(name, n, time.Since(start))
	writeJSON(w, http.StatusCreated, uploadResponse{Name: name, Size: n})
}

func (s *Server) admitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterBusy))
		writeError(w, http.StatusTooManyRequests, "busy", "Another upload is in progress. Waiting for it to finish.")
	case errors.Is(err, session.ErrClosed):
		writeError(w, http.StatusForbidden, "closed", "This drop has closed.")
	case errors.Is(err, session.ErrFileCount):
		writeError(w, http.StatusForbidden, "file_limit", "This drop has received as many files as it allows.")
	case errors.Is(err, session.ErrFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "This file is larger than the drop allows.")
	case errors.Is(err, session.ErrQuota):
		writeError(w, http.StatusRequestEntityTooLarge, "quota", "This drop doesn't have room for that file.")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

func (s *Server) copyError(w http.ResponseWriter, name string, err error, sess *session.Session) {
	switch {
	case sess.Context().Err() != nil:
		s.fail(w, name, http.StatusForbidden, "closed", "session ended during upload", "This drop closed before the upload finished.")
	case errors.Is(err, os.ErrDeadlineExceeded):
		s.fail(w, name, http.StatusRequestTimeout, "stalled", "upload stalled", "The upload stopped making progress.")
	case errors.Is(err, context.Canceled):
		// The client went away; nobody is listening for a response body.
		s.cfg.Reporter.UploadFailed(name, "cancelled by sender")
	case errors.Is(err, errWrite):
		if storage.IsNoSpace(err) {
			s.fail(w, name, http.StatusInsufficientStorage, "no_space", "disk full", "The receiving computer ran out of space.")
		} else {
			s.fail(w, name, http.StatusInternalServerError, "io_error", "write failed", "The receiving computer couldn't save the file.")
		}
	default:
		s.fail(w, name, http.StatusBadRequest, "interrupted", "connection interrupted", "The upload was interrupted.")
	}
}

// fail reports an upload that stopped after its body started streaming.
// The rest of the body is not wanted, so the connection is closed instead
// of letting net/http try to drain it (which would block on a stalled
// sender).
func (s *Server) fail(w http.ResponseWriter, name string, status int, code, logReason, msg string) {
	s.cfg.Reporter.UploadFailed(name, logReason)
	w.Header().Set("Connection", "close")
	writeError(w, status, code, msg)
}

var errWrite = errors.New("write failed")

type diskWriteError struct{ err error }

func (e diskWriteError) Error() string        { return "write: " + e.err.Error() }
func (e diskWriteError) Is(target error) bool { return target == errWrite }
func (e diskWriteError) Unwrap() error        { return e.err }

// copyBody streams src into dst. Before each read it pushes the connection
// read deadline forward by stall, so a sender that stops transmitting is
// cut off while a slow one is not.
func copyBody(ctx context.Context, dst io.Writer, src io.Reader, rc *http.ResponseController,
	stall time.Duration, progress func(int64)) (int64, error) {
	buf := make([]byte, 256<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		rc.SetReadDeadline(time.Now().Add(stall))
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			n += int64(nw)
			if werr == nil && nw != nr {
				werr = io.ErrShortWrite
			}
			if werr != nil {
				return n, diskWriteError{werr}
			}
			progress(n)
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			// A stall trips the read deadline, which also makes net/http
			// cancel the request context; report it as a stall.
			if errors.Is(rerr, os.ErrDeadlineExceeded) {
				return n, rerr
			}
			if err := ctx.Err(); err != nil {
				return n, err
			}
			return n, rerr
		}
	}
}
