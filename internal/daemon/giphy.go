package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// GIF search runs in the daemon rather than the panel so the API key never
// reaches QML, and so the chosen GIF is downloaded with the same size limits
// and error handling as any other attachment.

const (
	giphySearchURL   = "https://api.giphy.com/v1/gifs/search"
	giphyTrendingURL = "https://api.giphy.com/v1/gifs/trending"

	// GIPHY requires this wherever their content is shown.
	giphyAttribution = "Powered by GIPHY"

	// Cap what gets sent. GIPHY's "downsized_medium" is capped at 5 MB, which
	// is already generous for a carrier; anything larger tends to be rejected
	// or silently transcoded.
	maxGifBytes = 8 << 20

	giphyTimeout = 15 * time.Second
)

// giphyResponse is the subset of the API payload actually used.
type giphyResponse struct {
	Data []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Images struct {
			FixedHeight      giphyRendition `json:"fixed_height"`
			FixedHeightSmall giphyRendition `json:"fixed_height_small"`
			Downsized        giphyRendition `json:"downsized"`
			DownsizedMedium  giphyRendition `json:"downsized_medium"`
			Original         giphyRendition `json:"original"`
		} `json:"images"`
	} `json:"data"`
	Meta struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	} `json:"meta"`
}

type giphyRendition struct {
	URL    string `json:"url"`
	Width  string `json:"width"`
	Height string `json:"height"`
	Size   string `json:"size"`
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// pickSendRendition prefers the smallest rendition that still looks like the
// original, falling back upward only when a smaller one is missing.
func (r giphyResponse) sendURL(i int) string {
	img := r.Data[i].Images
	for _, candidate := range []giphyRendition{img.Downsized, img.DownsizedMedium, img.FixedHeight, img.Original} {
		if candidate.URL != "" {
			return candidate.URL
		}
	}
	return ""
}

// GifSearch queries GIPHY. An empty query returns what is trending, which is a
// better empty state than a blank grid.
func (d *Daemon) GifSearch(ctx context.Context, p wire.GifSearchParams) (*wire.GifSearchResult, error) {
	key := d.config.Get().GiphyAPIKey
	if key == "" {
		return &wire.GifSearchResult{NeedsKey: true, Attribution: giphyAttribution}, nil
	}

	limit := p.Limit
	if limit <= 0 || limit > 50 {
		limit = 24
	}

	query := strings.TrimSpace(p.Query)
	endpoint := giphyTrendingURL
	params := url.Values{
		"api_key": {key},
		"limit":   {strconv.Itoa(limit)},
		"rating":  {"pg-13"},
	}
	if query != "" {
		endpoint = giphySearchURL
		// GIPHY caps the query at 50 characters.
		if len(query) > 50 {
			query = query[:50]
		}
		params.Set("q", query)
	}

	ctx, cancel := context.WithTimeout(ctx, giphyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach GIPHY: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("GIPHY rejected the API key")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("GIPHY rate limit reached; try again shortly")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GIPHY returned %s", resp.Status)
	}

	var parsed giphyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse GIPHY response: %w", err)
	}

	out := &wire.GifSearchResult{Attribution: giphyAttribution}
	for i := range parsed.Data {
		preview := parsed.Data[i].Images.FixedHeight
		if preview.URL == "" {
			preview = parsed.Data[i].Images.FixedHeightSmall
		}
		send := parsed.sendURL(i)
		if preview.URL == "" || send == "" {
			continue
		}
		out.Gifs = append(out.Gifs, wire.Gif{
			ID:            parsed.Data[i].ID,
			Title:         parsed.Data[i].Title,
			PreviewURL:    preview.URL,
			PreviewWidth:  atoi(preview.Width),
			PreviewHeight: atoi(preview.Height),
			SendURL:       send,
		})
	}
	d.log.Debug().Str("query", query).Int("results", len(out.Gifs)).Msg("GIF search")
	return out, nil
}

// GifFetch downloads a chosen GIF into the media cache and returns its path,
// so it can go through the same staging and send path as any other image.
func (d *Daemon) GifFetch(ctx context.Context, p wire.GifFetchParams) (string, error) {
	if p.URL == "" {
		return "", errors.New("no GIF URL given")
	}
	parsed, err := url.Parse(p.URL)
	if err != nil || parsed.Scheme != "https" {
		return "", errors.New("refusing to fetch a non-https URL")
	}
	if !strings.HasSuffix(parsed.Hostname(), "giphy.com") {
		return "", fmt.Errorf("refusing to fetch from %q", parsed.Hostname())
	}

	sum := sha256.Sum256([]byte(p.URL))
	dest := filepath.Join(d.paths.MediaDir(), "gif-"+hex.EncodeToString(sum[:12])+".gif")
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}

	ctx, cancel := context.WithTimeout(ctx, giphyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download GIF: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GIPHY returned %s for the GIF", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGifBytes+1))
	if err != nil {
		return "", fmt.Errorf("read GIF: %w", err)
	}
	if len(data) > maxGifBytes {
		return "", fmt.Errorf("that GIF is larger than %d MB", maxGifBytes>>20)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return "", fmt.Errorf("write GIF: %w", err)
	}
	d.log.Debug().Int("bytes", len(data)).Msg("Fetched GIF")
	return dest, nil
}

// SetGiphyKey stores the API key. It is trimmed but not validated here; the
// next search reports plainly if GIPHY rejects it.
func (d *Daemon) SetGiphyKey(key string) error {
	return d.config.SetGiphyAPIKey(strings.TrimSpace(key))
}
