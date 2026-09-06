package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/store"
)

func newCaptureDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	return &Daemon{
		log:   zerolog.Nop(),
		paths: &store.Paths{Data: dir, Cache: dir, Runtime: dir},
	}, dir
}

func TestDiscardCaptureRemovesOwnFiles(t *testing.T) {
	d, dir := newCaptureDaemon(t)
	path := filepath.Join(dir, "webcam-123.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.DiscardCapture(path); err != nil {
		t.Fatalf("discard failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("capture should have been removed")
	}
	// Removing something already gone is not an error; a retake can race a cancel.
	if err := d.DiscardCapture(path); err != nil {
		t.Errorf("second discard should be a no-op, got %v", err)
	}
	if err := d.DiscardCapture(""); err != nil {
		t.Errorf("empty path should be a no-op, got %v", err)
	}
}

func TestDiscardCaptureRefusesAnythingElse(t *testing.T) {
	d, dir := newCaptureDaemon(t)

	// A file in the right place but not one of ours.
	session := filepath.Join(dir, "session.json")
	if err := os.WriteFile(session, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.DiscardCapture(session); err == nil {
		t.Error("must refuse files that are not webcam captures")
	}
	if _, err := os.Stat(session); err != nil {
		t.Error("session.json must survive")
	}

	// Correct name, wrong directory.
	outside := filepath.Join(t.TempDir(), "webcam-1.jpg")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.DiscardCapture(outside); err == nil {
		t.Error("must refuse paths outside the cache directory")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a file outside the cache dir must survive")
	}

	// Traversal dressed up to look like ours.
	victim := filepath.Join(dir, "sub")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	traversal := filepath.Join(dir, "sub", "..", "..", "webcam-evil.jpg")
	if err := d.DiscardCapture(traversal); err == nil {
		t.Error("must refuse traversal out of the cache directory")
	}

	// A nested path inside the cache dir is still not directly in it.
	nested := filepath.Join(dir, "media", "webcam-1.jpg")
	if err := d.DiscardCapture(nested); err == nil {
		t.Error("must refuse subdirectories of the cache directory")
	}
}
