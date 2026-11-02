package decisions

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// stamp is how a poll notices an edit without reading the file: modification time and size.
type stamp struct {
	mod  time.Time
	size int64
}

// Notice is raised when a reload could not be used. It carries the path and the error, never the file's text.
type Notice struct {
	Path string
	Err  error
}

// Store holds the live registry and swaps it atomically on reload, so a pre-read either sees the old registry or
// the new one and never a half-parsed one. Polling rather than inotify, for the reason in banks.Store.
type Store struct {
	dir string

	mu   sync.RWMutex
	reg  *Registry
	seen map[string]stamp
}

// NewStore loads the directory once and remembers the stamps. A broken registry at startup is a startup error:
// there is no previous config to keep.
func NewStore(dir string) (*Store, error) {
	reg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, reg: reg}
	seen, err := s.readStamps()
	if err != nil {
		return nil, err
	}
	s.seen = seen
	return s, nil
}

// Current returns the live registry, safe to use from any goroutine.
func (s *Store) Current() *Registry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg
}

// Dir returns the directory being watched.
func (s *Store) Dir() string { return s.dir }

// Reload loads and swaps, or keeps the previous registry and returns the error.
func (s *Store) Reload() error {
	reg, err := Load(s.dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reg = reg
	return nil
}

// Poll reloads if either file changed, reporting a broken edit once and keeping the working registry.
func (s *Store) Poll() *Notice {
	current, err := s.readStamps()
	if err != nil {
		return &Notice{Path: s.dir, Err: err}
	}
	if sameStamps(s.snapshot(), current) {
		return nil
	}

	s.mu.Lock()
	s.seen = current
	s.mu.Unlock()

	if err := s.Reload(); err != nil {
		return &Notice{Path: s.dir, Err: err}
	}
	return nil
}

// Watch polls on every tick until ctx is cancelled. The caller owns the ticker.
func (s *Store) Watch(ctx context.Context, ticks <-chan time.Time, onNotice func(Notice)) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if notice := s.Poll(); notice != nil && onNotice != nil {
				onNotice(*notice)
			}
		}
	}
}

// snapshot copies the stamps under the lock.
func (s *Store) snapshot() map[string]stamp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]stamp, len(s.seen))
	for name, st := range s.seen {
		out[name] = st
	}
	return out
}

// readStamps reads the two config files' modification times and sizes.
func (s *Store) readStamps() (map[string]stamp, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]stamp, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out[entry.Name()] = stamp{mod: info.ModTime(), size: info.Size()}
	}
	return out, nil
}

// sameStamps reports whether two stamp maps describe the same files in the same state.
func sameStamps(a, b map[string]stamp) bool {
	if len(a) != len(b) {
		return false
	}
	for name, want := range a {
		got, ok := b[name]
		if !ok || got != want {
			return false
		}
	}
	return true
}
