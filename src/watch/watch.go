// Package watch reports changes to one file. It watches the directory rather
// than the file, so saves that replace the file (as most editors do) are seen.
package watch

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce collapses the burst of events one save produces into one signal.
const debounce = 300 * time.Millisecond

// File sends a value on the returned channel after the file changes, once the
// writes have settled. It stops when ctx is cancelled.
func File(ctx context.Context, path string) (<-chan struct{}, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", path, err)
	}
	if err := watcher.Add(filepath.Dir(absolute)); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch %s: %w", path, err)
	}

	changes := make(chan struct{}, 1)
	notify := func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	// changes is never closed: a debounce timer may still fire after the
	// watcher stops, and sending on a closed channel would panic. Consumers
	// stop on ctx instead.
	go func() {
		defer watcher.Close()
		var timer *time.Timer
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Clean(event.Name) != absolute || event.Op == fsnotify.Chmod {
					continue
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(debounce, notify)
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return changes, nil
}
