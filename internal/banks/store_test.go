package banks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeBank writes one file into a temporary config directory.
func writeBank(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const sharedBody = `categories:
  - name: feel
    label: Feel
    positive: [cozy]
`

func newTempBanks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeBank(t, dir, "shared.yaml", sharedBody)
	writeBank(t, dir, "books.yaml", "medium: book\ninclude: [feel]\n")
	return dir
}

func TestStoreSwapsOnAChange(t *testing.T) {
	dir := newTempBanks(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	book, _ := store.Current().Bank("book")
	feel, _ := book.Category("feel")
	if len(feel.Chips) != 1 {
		t.Fatalf("chips = %+v", feel.Chips)
	}

	writeBank(t, dir, "shared.yaml", `categories:
  - name: feel
    label: Feel
    positive: [cozy, warm]
`)
	if notice := store.Poll(); notice != nil {
		t.Fatalf("Poll after a good edit: %+v", notice)
	}
	book, _ = store.Current().Bank("book")
	feel, _ = book.Category("feel")
	if len(feel.Chips) != 2 {
		t.Errorf("chips = %+v, want the edited vocabulary", feel.Chips)
	}
}

// A broken edit must not take the app down: the previous banks keep serving and one notice says why.
func TestStoreKeepsTheOldSetOnABrokenFile(t *testing.T) {
	dir := newTempBanks(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	before := store.Current()

	writeBank(t, dir, "shared.yaml", "categories:\n  - name: feel\n    colour: blue\n")
	notice := store.Poll()
	if notice == nil {
		t.Fatal("Poll on a broken file returned no notice")
	}
	if notice.Path != dir {
		t.Errorf("notice path = %q, want the watched directory %q", notice.Path, dir)
	}
	if store.Current() != before {
		t.Error("the broken file replaced the working config")
	}

	// It is reported once, not on every tick of the same broken content.
	if again := store.Poll(); again != nil {
		t.Errorf("the same broken file was reported twice: %+v", again)
	}

	// Fixing it brings the new vocabulary in without a restart.
	writeBank(t, dir, "shared.yaml", `categories:
  - name: feel
    label: Feel
    positive: [cozy, warm, quiet]
`)
	if notice := store.Poll(); notice != nil {
		t.Fatalf("Poll after the fix: %+v", notice)
	}
	book, _ := store.Current().Bank("book")
	feel, _ := book.Category("feel")
	if len(feel.Chips) != 3 {
		t.Errorf("chips = %+v, want the fixed vocabulary", feel.Chips)
	}
}

func TestStoreWatch(t *testing.T) {
	dir := newTempBanks(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 4)
	done := make(chan struct{})
	notices := make(chan Notice, 4)
	go func() {
		defer close(done)
		store.Watch(ctx, ticks, func(n Notice) { notices <- n })
	}()

	writeBank(t, dir, "film.yaml", "medium: film\ninclude: [feel]\n")
	ticks <- time.Now()
	waitFor(t, func() bool {
		_, ok := store.Current().Bank("film")
		return ok
	})

	writeBank(t, dir, "film.yaml", "medium: film\nnonsense: true\n")
	ticks <- time.Now()
	select {
	case notice := <-notices:
		if notice.Err == nil {
			t.Error("notice with no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notice after a broken edit")
	}
	if _, ok := store.Current().Bank("film"); !ok {
		t.Error("the broken edit removed the working film bank")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop when its context was cancelled")
	}
}

func TestStoreStartupFailsLoudly(t *testing.T) {
	if _, err := NewStore(t.TempDir()); err == nil {
		t.Fatal("NewStore on an empty directory succeeded")
	}
}

// waitFor waits for a condition the watcher produces, so a test never sleeps a fixed time.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
