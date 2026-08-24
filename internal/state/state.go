// Package state persists per-session loop-guard history as JSON files with
// advisory locking, so multiple harness processes can share one session.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	Type        string `json:"type"` // "tool" or "response"
	Name        string `json:"name,omitempty"`
	Args        any    `json:"args,omitempty"`
	Text        string `json:"text,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
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

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// FakeClock is a controllable Clock for tests.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a FakeClock fixed at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the fake clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
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

// SessionStatePath is the JSON file backing a session id.
func SessionStatePath(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

// Store reads and writes sessions under Dir using Clock for timestamps.
// A zero-value Store uses the default clock; tests inject a FakeClock.
type Store struct {
	Dir   string
	Clock Clock

	initOnce sync.Once
	clock    Clock
}

func (s *Store) now() time.Time {
	s.initOnce.Do(func() {
		if s.Clock == nil {
			s.Clock = realClock{}
		}
		s.clock = s.Clock
	})
	return s.clock.Now()
}

// Load returns the session for id. A missing or corrupt file yields a fresh
// session — corrupt-state recovery means a broken cache can never wedge the
// harness.
func (s *Store) Load(id string) (*Session, error) {
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
	return &sess, nil
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
	release, err := s.lock(id)
	if err != nil {
		return err
	}
	defer release()

	sess, err := s.Load(id)
	if err != nil {
		return err
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
	return os.Rename(tmp, path)
}

const (
	lockPollInterval = 25 * time.Millisecond
	lockTimeout      = 3 * time.Second
)

// lock acquires <id>.lock exclusively, stealing locks older than
// StaleLockAge. It returns a release func that is always safe to call.
func (s *Store) lock(id string) (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return func() {}, err
	}
	lockPath := filepath.Join(s.Dir, id+".lock")
	deadline := time.Now().Add(lockTimeout)

	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return func() {}, fmt.Errorf("acquire lock: %w", err)
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
