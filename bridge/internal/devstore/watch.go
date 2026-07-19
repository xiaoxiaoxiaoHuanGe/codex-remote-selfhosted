package devstore

import (
	"context"
	"os"
	"time"
)

// Watch polls the devices.json mtime every interval and calls Reload on change,
// reporting each outcome via onReload (nil on success). Zero new dependencies,
// cross-platform (no SIGHUP/fsnotify). Blocks until ctx is done. Decision D-a.
func (s *Store) Watch(ctx context.Context, interval time.Duration, onReload func(error)) {
	var last time.Time
	if fi, err := os.Stat(s.path); err == nil {
		last = fi.ModTime()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fi, err := os.Stat(s.path)
			if err != nil {
				continue // transiently absent (e.g. mid-rename); try next tick
			}
			if mt := fi.ModTime(); mt.After(last) {
				last = mt
				err := s.Reload()
				if onReload != nil {
					onReload(err)
				}
			}
		}
	}
}
