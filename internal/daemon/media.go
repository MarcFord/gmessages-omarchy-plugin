package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// mediaSecret carries every route to one attachment's bytes.
//
// Google exposes three, and only handling the first leaves most photos blank:
//   - MediaID + DecryptionKey: the full-size media, when it has been uploaded
//   - ThumbnailMediaID + ThumbnailDecryptionKey: a low-res stand-in
//   - MediaData: small media inlined directly in the message
//
// An MMS photo that nobody has opened yet has only a thumbnail, or nothing at
// all. GetFullSizeImage asks the phone to upload the real thing, which then
// arrives as a message update carrying a proper MediaID.
type mediaSecret struct {
	mediaID   string
	key       []byte
	thumbID   string
	thumbKey  []byte
	inline    []byte
	mimeType  string
	name      string
	messageID string
	partID    string
	// size is what the message claims the attachment is. A claim, not a fact.
	size int64
}

// mediaCache stores decrypted attachments on disk so re-opening a thread does
// not re-download every image.
type mediaCache struct {
	dir string

	mu      sync.Mutex
	secrets map[string]mediaSecret
	// order records insertion order so the oldest secret can be dropped once
	// the map hits its cap. Without it, every attachment ever seen is kept
	// alive for the life of the process -- along with its decryption keys and
	// any inline bytes -- and a long-lived daemon grows without bound.
	order []string
	// inflight collapses concurrent requests for the same attachment.
	inflight map[string]chan struct{}
	// requested tracks full-size requests already sent to the phone.
	requested map[string]bool
}

func newMediaCache(dir string) *mediaCache {
	return &mediaCache{
		dir:       dir,
		secrets:   make(map[string]mediaSecret),
		inflight:  make(map[string]chan struct{}),
		requested: make(map[string]bool),
	}
}

// attachmentKey is a stable handle for a media part. Media IDs are frequently
// empty, so fall back to the part id and finally to the message id.
func attachmentKey(messageID, partID, mediaID string) string {
	if mediaID != "" {
		return mediaID
	}
	if partID != "" {
		return "part:" + partID
	}
	return "msg:" + messageID
}

// record harvests every media route from a message's parts.
func (m *mediaCache) record(msg *gmproto.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, part := range msg.GetMessageInfo() {
		md := part.GetMediaContent()
		if md == nil {
			continue
		}
		key := attachmentKey(msg.GetMessageID(), part.GetActionMessageID(), md.GetMediaID())
		if key == "" {
			continue
		}
		if _, seen := m.secrets[key]; !seen {
			m.order = append(m.order, key)
		}
		m.secrets[key] = mediaSecret{
			mediaID:   md.GetMediaID(),
			key:       md.GetDecryptionKey(),
			thumbID:   md.GetThumbnailMediaID(),
			thumbKey:  md.GetThumbnailDecryptionKey(),
			inline:    md.GetMediaData(),
			mimeType:  md.GetMimeType(),
			name:      md.GetMediaName(),
			messageID: msg.GetMessageID(),
			partID:    part.GetActionMessageID(),
			size:      md.GetSize(),
		}
	}
	m.trimLocked()
}

// maxSecrets caps how many attachments are remembered. Scrolling back further
// than this simply re-reads the message, which the daemon already does; the
// cost of forgetting is a round trip, and the cost of not forgetting is
// unbounded memory holding key material.
const maxSecrets = 4096

func (m *mediaCache) trimLocked() {
	for len(m.order) > maxSecrets {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.secrets, oldest)
		delete(m.requested, oldest)
	}
	// The backing array keeps every key ever appended alive otherwise.
	if cap(m.order) > 4*maxSecrets {
		m.order = append(make([]string, 0, len(m.order)), m.order...)
	}
}

func (m *mediaCache) secret(key string) (mediaSecret, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.secrets[key]
	return s, ok
}

// claimFullSizeRequest reports whether a full-size request should be sent,
// ensuring the phone is asked only once per part.
func (m *mediaCache) claimFullSizeRequest(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.requested[key] {
		return false
	}
	m.requested[key] = true
	return true
}

// path is a stable, filesystem-safe location for an attachment. Keys can
// contain characters that are not valid in filenames, so hash them.
func (m *mediaCache) path(key, mimeType string) string {
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:16]) + extForMime(mimeType)
	return filepath.Join(m.dir, name)
}

func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "video/mp4":
		return ".mp4"
	case "audio/mp4", "audio/aac":
		return ".m4a"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/amr":
		return ".amr"
	case "audio/3gpp":
		return ".3ga"
	default:
		return ".bin"
	}
}

// Media resolves one attachment to a local file, trying each route Google
// offers and, when only a thumbnail exists, asking the phone to upload the
// full-size original for next time.
func (d *Daemon) Media(ctx context.Context, p wire.MediaParams) (*wire.MediaResult, error) {
	key := p.Key
	if key == "" {
		key = p.MediaID // legacy callers
	}
	if key == "" {
		return nil, errors.New("missing attachment key")
	}
	secret, ok := d.media.secret(key)
	if !ok {
		return nil, fmt.Errorf("unknown attachment %q", key)
	}

	// A thumbnail and the full-size original are cached under different names.
	// Sharing one path meant the low-res stand-in that arrived first shadowed
	// the real image forever, since the cache hit short-circuits the download.
	haveFull := secret.mediaID != ""
	dest := d.media.path(key, secret.mimeType)
	if !haveFull {
		dest = d.media.path(key+"#thumb", secret.mimeType)
	}
	if _, err := os.Stat(dest); err == nil {
		return &wire.MediaResult{Key: key, Path: dest, Thumbnail: !haveFull}, nil
	}

	// Fail early when there is no session; the download path checks again.
	if _, err := d.requireClient(); err != nil {
		return nil, err
	}

	// Collapse duplicate concurrent requests for the same attachment.
	d.media.mu.Lock()
	if wait, busy := d.media.inflight[key]; busy {
		d.media.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if _, err := os.Stat(dest); err == nil {
			return &wire.MediaResult{Key: key, Path: dest, Thumbnail: !haveFull}, nil
		}
		return nil, errors.New("download failed")
	}
	done := make(chan struct{})
	d.media.inflight[key] = done
	d.media.mu.Unlock()

	defer func() {
		d.media.mu.Lock()
		delete(d.media.inflight, key)
		d.media.mu.Unlock()
		close(done)
	}()

	// The declared size is not trusted -- downloadMediaBounded holds the real
	// limit -- but refusing here avoids opening a connection at all for an
	// attachment that is already known to be too big.
	if secret.size > maxDownloadBytes {
		return nil, fmt.Errorf("%w (%d MB declared)", errAttachmentTooLarge, secret.size>>20)
	}

	// Route order matters and is the same one the Matrix bridge uses: the
	// full-size original first, then a server-side thumbnail, and only then
	// the bytes inlined in the message. Inline data is a preview a few hundred
	// bytes long — checking it first served that in place of a real photo even
	// when the original was available.
	var (
		data []byte
		err  error
	)
	isThumb := false

	switch {
	case secret.mediaID != "":
		data, err = withAuthRetry(d, func() ([]byte, error) {
			return d.downloadMediaBounded(ctx, secret.mediaID, secret.key)
		})
		if err != nil {
			return nil, fmt.Errorf("download media: %w", err)
		}

	case secret.thumbID != "":
		data, err = withAuthRetry(d, func() ([]byte, error) {
			return d.downloadMediaBounded(ctx, secret.thumbID, secret.thumbKey)
		})
		if err != nil {
			return nil, fmt.Errorf("download thumbnail: %w", err)
		}
		isThumb = true

	case len(secret.inline) > 0:
		// Inline bytes arrive inside a message rather than over a download, so
		// they are bounded here instead.
		if len(secret.inline) > maxDownloadBytes {
			return nil, errAttachmentTooLarge
		}
		data = secret.inline
		isThumb = true

	default:
		// Nothing downloadable yet; ask the phone to upload the original.
		d.requestFullSize(ctx, key, secret)
		return &wire.MediaResult{Key: key, Pending: true}, nil
	}

	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return nil, fmt.Errorf("write media: %w", err)
	}
	// Trim the cache after each write rather than on a timer, so the bound
	// holds even if the daemon never idles.
	if freed, pruneErr := d.pruneCaches(); pruneErr != nil {
		d.log.Warn().Err(pruneErr).Msg("Could not trim the attachment cache")
	} else if freed > 0 {
		d.log.Debug().Int64("freed_bytes", freed).Msg("Trimmed the attachment cache")
	}

	d.log.Debug().
		Str("key", key).
		Bool("thumbnail", isThumb).
		Int("bytes", len(data)).
		Msg("Attachment ready")

	if isThumb {
		// Show the preview now, and ask for the original for next time.
		d.requestFullSize(ctx, key, secret)
	}
	return &wire.MediaResult{Key: key, Path: dest, Thumbnail: isThumb}, nil
}

// requestFullSize asks the phone to upload the full-size original. The reply
// arrives later as a message update carrying a real media ID, so this only
// improves the next request.
func (d *Daemon) requestFullSize(ctx context.Context, key string, secret mediaSecret) {
	if secret.partID == "" || secret.messageID == "" {
		return
	}
	if !d.media.claimFullSizeRequest(key) {
		return
	}
	c, err := d.requireClient()
	if err != nil {
		return
	}
	go func() {
		if _, err := c.GetFullSizeImage(context.WithoutCancel(ctx), secret.messageID, secret.partID); err != nil {
			d.log.Debug().Err(err).Str("key", key).Msg("Full-size media request failed")
			return
		}
		d.log.Debug().Str("key", key).Msg("Requested full-size media from phone")
	}()
}
