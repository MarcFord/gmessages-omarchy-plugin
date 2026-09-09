package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCacheFile(t *testing.T, dir, name string, size int, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPruneLeavesCacheUnderCapAndEvictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	m := &mediaCache{dir: dir}

	// Six files of 48 MiB against a 256 MiB cap: 288 MiB total, so the oldest
	// must go until the rest fit.
	const each = 48 << 20
	paths := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		age := time.Duration(6-i) * time.Hour // index 0 is the oldest
		paths = append(paths, writeCacheFile(t, dir, fmt.Sprintf("f%d.jpg", i), each, age))
	}

	freed, err := m.prune()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if freed == 0 {
		t.Error("expected the cache to be trimmed")
	}

	var total int64
	for _, p := range paths {
		if info, statErr := os.Stat(p); statErr == nil {
			total += info.Size()
		}
	}
	if total > maxCacheBytes {
		t.Errorf("cache still over cap: %d > %d", total, maxCacheBytes)
	}
	// The oldest must be the one that went, and the newest must survive.
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Error("oldest file should have been evicted first")
	}
	if _, err := os.Stat(paths[5]); err != nil {
		t.Error("newest file should have survived")
	}
}

func TestPruneLeavesSmallCacheAlone(t *testing.T) {
	dir := t.TempDir()
	m := &mediaCache{dir: dir}
	p := writeCacheFile(t, dir, "small.jpg", 1024, time.Hour)

	freed, err := m.prune()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if freed != 0 {
		t.Errorf("nothing should have been freed, got %d", freed)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("a cache under the cap must be left alone")
	}
}

func TestPruneMissingDirIsNotAnError(t *testing.T) {
	m := &mediaCache{dir: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := m.prune(); err != nil {
		t.Errorf("a missing cache dir should be a no-op, got %v", err)
	}
}

// The capture directory is the cache root, which also holds session.json and
// config.json. Trimming it must touch only files this plugin created.
func TestPruneDirLeavesForeignFilesAlone(t *testing.T) {
	dir := t.TempDir()

	session := writeCacheFile(t, dir, "session.json", 16, 10*time.Hour)
	config := writeCacheFile(t, dir, "config.json", 16, 10*time.Hour)
	// Well over the cap, and the oldest things present.
	old1 := writeCacheFile(t, dir, "voice-1.m4a", 40<<20, 9*time.Hour)
	writeCacheFile(t, dir, "webcam-2.jpg", 40<<20, 1*time.Hour)

	if _, err := pruneDir(dir, maxCaptureBytes, isOwnCapture); err != nil {
		t.Fatalf("prune: %v", err)
	}

	for _, p := range []string{session, config} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must never be deleted by a cache trim", filepath.Base(p))
		}
	}
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Error("the oldest capture should have been evicted")
	}
}
