package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// maxUploadBytes guards against sending something the carrier will reject
// outright. MMS limits are far lower than this, but RCS tolerates more, and
// the phone gives a clearer error than a truncated upload would.
const maxUploadBytes = 25 << 20

// SendMedia uploads a local file and sends it to a conversation, optionally
// with a caption.
func (d *Daemon) SendMedia(ctx context.Context, p wire.SendMediaParams) (*wire.Message, error) {
	c, err := d.requireClient()
	if err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, errors.New("no file given")
	}

	d.mu.RLock()
	conv, ok := d.convs[p.ConversationID]
	d.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown conversation %q", p.ConversationID)
	}
	if conv.ReadOnly {
		return nil, errors.New("conversation is read-only")
	}

	info, err := os.Stat(p.Path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if info.IsDir() {
		return nil, errors.New("that is a directory, not a file")
	}
	if info.Size() > maxUploadBytes {
		return nil, fmt.Errorf("file is too large (%d MB); the limit is %d MB",
			info.Size()>>20, maxUploadBytes>>20)
	}

	// Read through a bounded reader rather than trusting the Stat above: the
	// size check and the read are two separate observations of the file, and
	// the limit is what decides how much memory this allocates.
	f, err := os.Open(p.Path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxUploadBytes {
		return nil, fmt.Errorf("file is too large; the limit is %d MB", maxUploadBytes>>20)
	}
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}

	fileName := filepath.Base(p.Path)
	mime := detectMime(data, fileName)

	media, err := withAuthRetry(d, func() (*gmproto.MediaContent, error) {
		return c.UploadMedia(data, fileName, mime)
	})
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}

	tmpID := uuid.NewString()
	parts := []*gmproto.MessageInfo{{
		Data: &gmproto.MessageInfo_MediaContent{MediaContent: media},
	}}
	caption := strings.TrimSpace(p.Caption)
	if caption != "" {
		parts = append(parts, &gmproto.MessageInfo{
			Data: &gmproto.MessageInfo_MessageContent{
				MessageContent: &gmproto.MessageContent{Content: caption},
			},
		})
	}

	req := &gmproto.SendMessageRequest{
		ConversationID: p.ConversationID,
		TmpID:          tmpID,
		MessagePayload: &gmproto.MessagePayload{
			TmpID:          tmpID,
			TmpID2:         tmpID,
			ConversationID: p.ConversationID,
			ParticipantID:  conv.OutgoingID,
			MessageInfo:    parts,
		},
	}

	resp, err := withAuthRetry(d, func() (*gmproto.SendMessageResponse, error) {
		return c.SendMessage(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	if status := resp.GetStatus(); status != gmproto.SendMessageResponse_SUCCESS {
		return nil, fmt.Errorf("send rejected: %s", status)
	}

	// A capture that has been sent has done its job. Without this every voice
	// note and webcam photo ever sent stays in the cache for good -- the panel
	// only deletes the ones the user cancels.
	if err := d.DiscardCapture(p.Path); err != nil {
		d.log.Debug().Err(err).Msg("Sent file was not one of ours to delete")
	}

	d.log.Info().
		Str("conversation", p.ConversationID).
		Str("mime", mime).
		Int("bytes", len(data)).
		Msg("Sent media")

	return &wire.Message{
		ID:             tmpID,
		ConversationID: p.ConversationID,
		Text:           caption,
		FromMe:         true,
		Pending:        true,
		Attachments: []wire.Attachment{{
			Key:      media.GetMediaID(),
			MediaID:  media.GetMediaID(),
			Name:     fileName,
			MimeType: mime,
			Size:     int64(len(data)),
			IsImage:  strings.HasPrefix(mime, "image/"),
			IsAudio:  strings.HasPrefix(mime, "audio/"),
		}},
	}, nil
}

// audioMimeByExt covers the container formats libgm knows how to type. The
// extension decides these, not the sniffer: an .m4a is an MP4 container, so
// content sniffing calls it video/mp4 (or fails to identify it at all), and
// uploading a voice note as video would show the recipient a video message.
var audioMimeByExt = map[string]string{
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/ogg",
	".amr":  "audio/amr",
	".3ga":  "audio/3gpp",
}

// detectMime prefers sniffing the content, since a wrong extension would make
// the phone reject the upload. Audio is the exception -- see audioMimeByExt.
func detectMime(data []byte, fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if mime, ok := audioMimeByExt[ext]; ok {
		return mime
	}
	if mime := http.DetectContentType(data); mime != "" && mime != "application/octet-stream" {
		// DetectContentType appends charset for text types.
		return strings.TrimSpace(strings.Split(mime, ";")[0])
	}
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}
