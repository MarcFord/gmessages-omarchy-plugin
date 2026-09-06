package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscardCapture deletes a webcam capture the user chose not to send.
//
// Without this, every retake and cancel leaves a full-resolution JPEG in the
// cache directory forever. The path is checked rather than trusted: this is
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
	base := filepath.Base(resolved)
	if !strings.HasPrefix(base, "webcam-") || !strings.HasSuffix(base, ".jpg") {
		return fmt.Errorf("refusing to delete %q: not a webcam capture", base)
	}

	if err := os.Remove(resolved); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove capture: %w", err)
	}
	d.log.Debug().Str("file", base).Msg("Discarded webcam capture")
	return nil
}
