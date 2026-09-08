package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// The cached file keeps its container's extension. Players sniff content, but
// an mp3 saved as .m4a is misleading to anything that trusts the name, and
// audio/mpeg used to be mapped to .m4a.
func TestExtForMimeAudio(t *testing.T) {
	cases := map[string]string{
		"audio/mp4":  ".m4a",
		"audio/aac":  ".m4a",
		"audio/mpeg": ".mp3",
		"audio/mp3":  ".mp3",
		"audio/ogg":  ".ogg",
		"audio/amr":  ".amr",
		"audio/3gpp": ".3ga",
		"image/png":  ".png",
		"video/mp4":  ".mp4",
		"":           ".bin",
	}
	for mime, want := range cases {
		if got := extForMime(mime); got != want {
			t.Errorf("extForMime(%q) = %q, want %q", mime, got, want)
		}
	}
}

// Two attachments must not collide, and the name has to stay filesystem-safe
// even though keys are arbitrary strings.
func TestMediaCachePathIsSafeAndDistinct(t *testing.T) {
	c := &mediaCache{dir: "/tmp/cache"}
	a := c.path("some/key with spaces:and|junk", "audio/mp4")
	b := c.path("another-key", "audio/mp4")
	if a == b {
		t.Error("different keys must not share a path")
	}
	for _, p := range []string{a, b} {
		if filepath.Dir(p) != "/tmp/cache" {
			t.Errorf("%q escaped the cache directory", p)
		}
		if !strings.HasSuffix(p, ".m4a") {
			t.Errorf("%q lost its extension", p)
		}
		if strings.ContainsAny(filepath.Base(p), "/ :|") {
			t.Errorf("%q is not filesystem-safe", filepath.Base(p))
		}
	}
}
