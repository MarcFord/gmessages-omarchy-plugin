package daemon

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/store"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

func newGifDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	return &Daemon{
		log:    zerolog.Nop(),
		paths:  &store.Paths{Data: dir, Cache: dir, Runtime: dir},
		config: store.NewConfigStore(dir + "/config.json"),
	}
}

func TestGifSearchWithoutKeyAsksForOne(t *testing.T) {
	d := newGifDaemon(t)
	// Must not attempt a request or error out: the picker needs to explain
	// how to get a key, not show a failure.
	res, err := d.GifSearch(context.Background(), wire.GifSearchParams{Query: "cat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.NeedsKey {
		t.Error("expected NeedsKey when no API key is configured")
	}
	if res.Attribution == "" {
		t.Error("GIPHY requires attribution to be displayed")
	}
}

func TestGifFetchRefusesUntrustedSources(t *testing.T) {
	d := newGifDaemon(t)
	ctx := context.Background()

	for _, bad := range []string{
		"",
		"http://media.giphy.com/x.gif",         // not https
		"https://evil.example/x.gif",           // wrong host
		"https://giphy.com.evil.example/x.gif", // suffix trickery on the label
		"file:///etc/passwd",
	} {
		if _, err := d.GifFetch(ctx, wire.GifFetchParams{URL: bad}); err == nil {
			t.Errorf("expected %q to be refused", bad)
		}
	}
}

func TestSendRenditionPrefersSmallest(t *testing.T) {
	var r giphyResponse
	r.Data = make([]struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Images struct {
			FixedHeight      giphyRendition `json:"fixed_height"`
			FixedHeightSmall giphyRendition `json:"fixed_height_small"`
			Downsized        giphyRendition `json:"downsized"`
			DownsizedMedium  giphyRendition `json:"downsized_medium"`
			Original         giphyRendition `json:"original"`
		} `json:"images"`
	}, 1)

	r.Data[0].Images.Original = giphyRendition{URL: "https://media.giphy.com/original.gif"}
	r.Data[0].Images.DownsizedMedium = giphyRendition{URL: "https://media.giphy.com/medium.gif"}
	r.Data[0].Images.Downsized = giphyRendition{URL: "https://media.giphy.com/small.gif"}

	// Smallest first: carriers reject large files, so never reach for the
	// original when a downsized one exists.
	if got := r.sendURL(0); got != "https://media.giphy.com/small.gif" {
		t.Errorf("sendURL = %q, want the downsized rendition", got)
	}

	// With only the original present it must still return something.
	r.Data[0].Images.Downsized = giphyRendition{}
	r.Data[0].Images.DownsizedMedium = giphyRendition{}
	if got := r.sendURL(0); got != "https://media.giphy.com/original.gif" {
		t.Errorf("sendURL = %q, want the original as a fallback", got)
	}
}
