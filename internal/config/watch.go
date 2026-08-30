package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches a config file for changes, re-parses and re-validates it,
// and invokes onChange only when the new config passes validation — a
// broken edit is logged and ignored so the running servers keep their last
// good configuration (docs/PLAN.md §4.4).
type Watcher struct {
	path     string
	log      *slog.Logger
	onChange func(*Config)
}

func NewWatcher(path string, log *slog.Logger, onChange func(*Config)) *Watcher {
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{path: path, log: log, onChange: onChange}
}

// Run blocks watching for filesystem events until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	// Watch the parent directory rather than the file itself: on Windows,
	// fsnotify's ReadDirectoryChangesW-based backend never delivers events
	// for a watch added directly on a single file, and on every platform a
	// save that replaces the file (temp-write-then-rename, which most
	// editors and tools use) would otherwise orphan a per-file watch. We
	// filter events down to just our file below.
	absPath, err := filepath.Abs(w.path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(absPath)
	if err := fsw.Add(dir); err != nil {
		return err
	}

	// Debounce: editors often emit several events (write+chmod+rename) for
	// a single logical save.
	var debounce *time.Timer
	const debounceDelay = 300 * time.Millisecond

	reload := func() {
		cfg, err := Load(w.path)
		if err != nil {
			w.log.Error("config reload failed, keeping previous configuration", "error", err)
			return
		}
		w.log.Info("config reloaded")
		w.onChange(cfg)
	}

	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return nil
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(ev.Name) != absPath {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(debounceDelay, reload)
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Error("config watcher error", "error", err)
		}
	}
}
