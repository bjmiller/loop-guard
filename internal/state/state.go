// Package state persists per-session loop-guard history as JSON files with
// advisory locking, so multiple harness processes can share one session.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CurrentVersion is the schema version of the session file.
const CurrentVersion = 1

// MaxEvents caps the rolling event history kept per session.
const MaxEvents = 50

// StaleLockAge is how old a lock file must be before it is considered
// abandoned by a crashed process and stolen.
const StaleLockAge = 10 * time.Second

// Event is one recorded tool call or model response.
type Event struct {
	Type        string    `json:"type"` // "tool" or "response"
	Name        string    `json:"name,omitempty"`
	Args        any       `json:"args,omitempty"`
	Text        string    `json:"text,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	At          time.Time `json:"at"`
}

// Session is the full persisted state for one agent session.
type Session struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Events        []Event   `json:"events"`
	Interventions int       `json:"interventions"`
	Breaker       bool      `json:"breaker"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DefaultDir resolves the state directory: the LOOPGUARD_CACHE_DIR override,
// then the platform user cache dir plus "loop-guard". os.UserCacheDir maps to
// XDG_CACHE_HOME on Linux, ~/Library/Caches on macOS, %LocalAppData% on Windows.
func DefaultDir() (string, error) {
	if v := os.Getenv("LOOPGUARD_CACHE_DIR"); v != "" {
		return filepath.Clean(v), nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	return filepath.Join(base, "loop-guard"), nil
}

// ErrInvalidSessionID is returned when a session id could escape the state
// directory (path separators, "..", control characters) or is unusable.
var ErrInvalidSessionID = errors.New("invalid session id: use letters, digits, dot, dash, or underscore only")

// ValidSessionID reports whether id is safe to embed in a file name under the
// state directory. It rejects empty ids, path separators, "..", and any byte
// outside [A-Za-z0-9._-], so a hostile session id cannot traverse out of the
// cache dir.
func ValidSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if strings.Contains(id, "..") {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// SessionStatePath is the JSON file backing a session id.
func SessionStatePath(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

// Store reads and writes sessions under Dir using Clock for timestamps.
// A zero-value Store uses the default clock; tests inject a FakeClock.
type Store struct {
	Dir   string
	Clock Clock
}

func (s *Store) now() time.Time {
	if s.Clock != nil {
		return s.Clock.Now()
	}
	return time.Now()
}

// Now exposes the store's clock (default: real time) for callers that need
// matching timestamps, e.g. handoff artifacts.
func (s *Store) Now() time.Time { return s.now() }

// Load returns the session for id. A missing or corrupt file yields a fresh
// session — corrupt-state recovery means a broken cache can never wedge the
// harness.
func (s *Store) Load(id string) (*Session, error) {
	if !ValidSessionID(id) {
		return nil, ErrInvalidSessionID
	}
	path := SessionStatePath(s.Dir, id)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.fresh(id), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	var sess Session
	if json.Unmarshal(data, &sess) != nil || sess.Version == 0 {
		return s.fresh(id), nil
	}
	// A newer schema is returned as-is so read-only callers still work;
	// Update refuses to rewrite it (see below).
	return &sess, nil
}

// DecodeArgs parses tool-call arguments. Numbers are decoded as json.Number so
// fingerprints distinguish large integers that float64 would collapse.
func DecodeArgs(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Store) fresh(id string) *Session {
	now := s.now()
	return &Session{
		Version:   CurrentVersion,
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Update loads the session under an advisory lock, applies fn, and persists
// atomically. fn may mutate freely; MaxEvents is enforced afterwards.
func (s *Store) Update(id string, fn func(*Session)) error {
	if !ValidSessionID(id) {
		return ErrInvalidSessionID
	}
	release, err := s.lock(id)
	if err != nil {
		return err
	}
	defer release()

	sess, err := s.Load(id)
	if err != nil {
		return err
	}
	if sess.Version > CurrentVersion {
		return fmt.Errorf("session %q was written by a newer loop-guard (schema %d); refusing to overwrite", id, sess.Version)
	}
	fn(sess)
	sess.Version = CurrentVersion
	sess.UpdatedAt = s.now()
	if len(sess.Events) > MaxEvents {
		sess.Events = sess.Events[len(sess.Events)-MaxEvents:]
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	path := SessionStatePath(s.Dir, id)
	tmp := path + ".tmp"
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	return nil
}

const (
	lockPollInterval = 25 * time.Millisecond
	lockTimeout      = 3 * time.Second
)

// lock acquires <id>.lock exclusively, stealing locks older than StaleLockAge.
// The returned release function removes the lock file only if this process
// still owns it, so a holder whose stale lock was stolen cannot delete the
// thief's lock. It is always safe to call.
func (s *Store) lock(id string) (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return func() {}, err
	}
	lockPath := filepath.Join(s.Dir, id+".lock")
	token := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	deadline := time.Now().Add(lockTimeout)

	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, token)
			f.Close()
			return func() {
				if data, err := os.ReadFile(lockPath); err == nil &&
					strings.TrimSpace(string(data)) == token {
					os.Remove(lockPath)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			// Windows reports an exclusive create racing a still-open
			// handle from another writer as EACCES rather than EEXIST.
			// Treat it as ordinary contention: the stale/timeout logic
			// below bounds the wait, and genuine permission problems
			// still surface as a lock timeout.
			if runtime.GOOS != "windows" || !errors.Is(err, fs.ErrPermission) {
				return func() {}, fmt.Errorf("acquire lock: %w", err)
			}
		}
		if info, statErr := os.Stat(lockPath); statErr == nil &&
			time.Since(info.ModTime()) > StaleLockAge {
			os.Remove(lockPath) // crashed holder; steal and retry immediately
			continue
		}
		if time.Now().After(deadline) {
			return func() {}, fmt.Errorf("timed out acquiring lock for session %q", id)
		}
		time.Sleep(lockPollInterval)
	}
}
