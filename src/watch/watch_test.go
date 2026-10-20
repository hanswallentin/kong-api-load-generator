package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileSignalsAfterSaveAndSurvivesAtomicReplace(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := File(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	// Most editors save by writing a temp file and renaming it over the original.
	temp := filepath.Join(directory, "config.tmp")
	if err := os.WriteFile(temp, []byte("a: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, changes)
}

func TestFileIgnoresOtherFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := File(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "other.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("a change to another file should not signal")
	case <-time.After(800 * time.Millisecond):
	}
}

func waitForSignal(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(3 * time.Second):
		t.Fatal("no change signal after the file was replaced")
	}
}
