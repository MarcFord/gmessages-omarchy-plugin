package daemon

import "testing"

// An .m4a is an MP4 container, so content sniffing types it as video or fails
// outright. Getting this wrong sends a voice note as a video message, or with
// a media format libgm cannot map at all.
func TestDetectMimeTypesAudioByExtension(t *testing.T) {
	// A minimal MP4 header, the shape ffmpeg writes for an AAC recording.
	m4a := []byte("\x00\x00\x00\x20ftypM4A \x00\x00\x00\x00M4A mp42isom")
	if got := detectMime(m4a, "voice-1.m4a"); got != "audio/mp4" {
		t.Errorf("m4a: got %q, want audio/mp4", got)
	}

	cases := map[string]string{
		"a.aac": "audio/aac", "a.mp3": "audio/mpeg", "a.ogg": "audio/ogg",
		"a.opus": "audio/ogg", "a.amr": "audio/amr", "a.3ga": "audio/3gpp",
	}
	for name, want := range cases {
		if got := detectMime([]byte("\x00\x00\x00\x00"), name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

// The audio override must not swallow the existing image behaviour.
func TestDetectMimeStillSniffsImages(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	if got := detectMime(png, "shot.png"); got != "image/png" {
		t.Errorf("png: got %q, want image/png", got)
	}
	// Content wins over a misleading extension for non-audio files.
	if got := detectMime(png, "shot.jpg"); got != "image/png" {
		t.Errorf("mislabelled png: got %q, want image/png", got)
	}
}
