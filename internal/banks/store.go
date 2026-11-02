package banks

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// stamp is how a poll notices an edit without reading the file: the modification time and the size. A bind-mounted
// host edit changes both, and reading a file to hash it on every tick would be wasteful on the potato.
type stamp struct {
	mod  time.Time
	size int64
}

// Notice is raised when a reload could not be used. It carries the path and the error, never the file's text,
// and the store keeps serving the previous config while it is raised (S05.6).
type Notice struct {
	Path string
	Err  error
}

// Store holds the live bank set and swaps it atomically on a reload, so a form being built never sees a half-set
// and a reader never takes a lock to look at a bank.
//
// Reload is by polling rather than inotify: a Docker bind mount on macOS does not reliably deliver change events,
// and a 2 s stat of three small files is cheaper than a dependency that silently misses an edit
// (docs/slices/S05-banks-and-decisions.md, Decisions 6).
type Store struct {
	dir string

	mu   sync.RWMutex
	set  *Set
	seen map[string]stamp
}

// NewStore loads the directory once and remembers the files' stamps. A broken bank at startup is a startup
// error, not a notice: there is no previous config to keep.
func NewStore(dir string) (*Store, error) {
	set, err := LoadDir(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, set: set}
	seen, err := s.readStamps()
	if err != nil {
		return nil, err
	}
	s.seen = seen
	return s, nil
}

// Current returns the live set. It is safe to call from any goroutine and never blocks on a reload.
func (s *Store) Current() *Set {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.set
}

// Dir returns the directory being watched.
func (s *Store) Dir() string { return s.dir }

// Reload loads the directory and swaps the set in, or keeps the previous one and returns the error. It is the
// second half of a poll; Poll is the exported entry point that also updates the stamps.
func (s *Store) Reload() error {
	set, err := LoadDir(s.dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set = set
	return nil
}

// Poll stats the directory and reloads if any file changed. A broken file produces one notice and the previous
// set keeps serving; the broken version is stamped, so it is reported once rather than on every tick.
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

// Watch polls on every tick until ctx is cancelled. The caller owns the ticker, so a test drives reloads by
// sending on a channel instead of sleeping.
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

// snapshot copies the stamps under the lock, so a comparison never races a reload.
func (s *Store) snapshot() map[string]stamp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]stamp, len(s.seen))
	for name, st := range s.seen {
		out[name] = st
	}
	return out
}

// readStamps reads the modification time and size of every YAML file in the directory.
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
