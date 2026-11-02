package decisions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTempDecisions(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, WordingFile, baseWording)
	writeConfig(t, dir, PolicyFile, basePolicy)
	return dir
}

func writeConfig(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSwapsOnAChange(t *testing.T) {
	dir := newTempDecisions(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := store.Current().Checks["intent"].Threshold; got != 0.65 {
		t.Fatalf("threshold = %v", got)
	}

	writeConfig(t, dir, PolicyFile, replaceOnce(basePolicy, "      threshold: 0.65", "      threshold: 0.70"))
	if notice := store.Poll(); notice != nil {
		t.Fatalf("Poll after a good edit: %+v", notice)
	}
	if got := store.Current().Checks["intent"].Threshold; got != 0.70 {
		t.Errorf("threshold = %v, want the edited 0.70", got)
	}
}

func TestStoreKeepsTheOldRegistryOnABrokenFile(t *testing.T) {
	dir := newTempDecisions(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	before := store.Current()

	writeConfig(t, dir, PolicyFile, "backend:\n  provider: openrouter\nnonsense: true\n")
	notice := store.Poll()
	if notice == nil || notice.Err == nil {
		t.Fatalf("Poll = %+v, want a notice", notice)
	}
	if store.Current() != before {
		t.Error("the broken policy replaced the working registry")
	}
	if again := store.Poll(); again != nil {
		t.Errorf("the same broken file was reported twice: %+v", again)
	}

	writeConfig(t, dir, PolicyFile, replaceOnce(basePolicy, "      threshold: 0.65", "      threshold: 0.55"))
	if notice := store.Poll(); notice != nil {
		t.Fatalf("Poll after the fix: %+v", notice)
	}
	if got := store.Current().Checks["intent"].Threshold; got != 0.55 {
		t.Errorf("threshold = %v, want the fixed 0.55", got)
	}
}

func TestStoreWatchStopsOnCancel(t *testing.T) {
	dir := newTempDecisions(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.Watch(ctx, ticks, nil)
	}()

	// 0.70 rather than 0.90: the intent check's log_review option already needs 0.80, and a check threshold
	// above an option threshold is a load error (proved in registry_test.go).
	writeConfig(t, dir, PolicyFile, replaceOnce(basePolicy, "      threshold: 0.65", "      threshold: 0.70"))
	ticks <- time.Now()
	waitUntil(t, func() bool { return store.Current().Checks["intent"].Threshold == 0.70 })

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

// waitUntil waits for something the watcher produces, bounded, without a fixed sleep.
func waitUntil(t *testing.T, ok func() bool) {
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

func TestStoreDir(t *testing.T) {
	dir := newTempDecisions(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", store.Dir(), dir)
	}
}
