package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscardCapture deletes a webcam photo or voice recording the user chose not
// to send.
//
// Without this, every retake and cancel leaves a full-resolution JPEG or an
// audio file in the cache directory forever. The path is checked rather than trusted: this is
// reachable over the socket, and a delete that accepts any path it is handed
// is a liability regardless of who is expected to call it.
func (d *Daemon) DiscardCapture(path string) error {
	if path == "" {
		return nil
	}

	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	captureDir, err := filepath.Abs(d.paths.Cache)
	if err != nil {
		return fmt.Errorf("resolve cache dir: %w", err)
	}

	// Must live directly in the cache directory and look like one of ours.
	if filepath.Dir(resolved) != captureDir {
		return fmt.Errorf("refusing to delete outside the cache directory")
	}
	if !isOwnCapture(filepath.Base(resolved)) {
		return fmt.Errorf("refusing to delete %q: not a capture this plugin made", filepath.Base(resolved))
	}

	if err := os.Remove(resolved); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove capture: %w", err)
	}
	d.log.Debug().Str("file", filepath.Base(resolved)).Msg("Discarded capture")
	return nil
}

// isOwnCapture matches only the names this plugin generates, so a discard
// cannot be talked into deleting an unrelated file that happens to sit in the
// cache directory -- downloaded attachments live there too.
func isOwnCapture(base string) bool {
	switch {
	case strings.HasPrefix(base, "webcam-") && strings.HasSuffix(base, ".jpg"):
		return true
	case strings.HasPrefix(base, "voice-") && strings.HasSuffix(base, ".m4a"):
		return true
	}
	return false
}
