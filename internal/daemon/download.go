package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/crypto"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/util"
	"google.golang.org/protobuf/proto"
)

// maxDownloadBytes is a hard ceiling on one incoming attachment.
//
// Carriers cap RCS well below this and MMS is smaller again, so a real
// attachment does not come close. The point is that nothing decides how much
// memory this process allocates except this constant: the size a message
// claims is a claim, and the bytes on the wire are whatever the far side
// sends.
const maxDownloadBytes = 32 << 20

// errAttachmentTooLarge is returned rather than silently truncating, because a
// truncated image written to the cache would be indistinguishable from a real
// one on the next open.
var errAttachmentTooLarge = errors.New("attachment is larger than the download limit")

// mediaHTTP is separate from libgm's own client so the timeout can be tuned
// for a single bounded download.
var mediaHTTP = &http.Client{Timeout: 2 * time.Minute}

// downloadMediaBounded mirrors libgm's Client.DownloadMedia with one
// difference: the response body is read through an io.LimitReader, so an
// oversized attachment is refused before it is allocated.
//
// Upstream reads the whole body with io.ReadAll and its http client is not
// reachable from outside the package, so there is no way to bound this by
// configuration -- see the note in README's security section. The request
// shape, headers and AES-GCM decryption below are upstream's, called through
// its exported helpers, so this stays in step with the protocol rather than
// reimplementing it.
func (d *Daemon) downloadMediaBounded(ctx context.Context, mediaID string, key []byte) ([]byte, error) {
	c, err := d.requireClient()
	if err != nil {
		return nil, err
	}

	meta := &gmproto.DownloadAttachmentRequest{
		Info: &gmproto.AttachmentInfo{
			AttachmentID: mediaID,
			Encrypted:    true,
		},
		AuthData: &gmproto.AuthMessage{
			RequestID:        uuid.NewString(),
			TachyonAuthToken: c.AuthData.TachyonAuthToken,
			Network:          c.AuthData.AuthNetwork(),
			ConfigVersion:    util.ConfigMessage,
		},
	}
	metaBytes, err := proto.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal download request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, util.UploadMediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare request: %w", err)
	}
	util.BuildUploadHeaders(req, base64.StdEncoding.EncodeToString(metaBytes))

	res, err := mediaHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", res.StatusCode)
	}
	body, err := readBounded(res.ContentLength, res.Body)
	if err != nil {
		return nil, err
	}

	cryptor, err := crypto.NewAESGCMHelper(key)
	if err != nil {
		return nil, err
	}
	plain, err := cryptor.DecryptData(body)
	if err != nil {
		return nil, fmt.Errorf("decrypt media: %w", err)
	}
	// Ciphertext is bounded above, and GCM does not expand on decryption, so
	// this cannot trip -- but the cache write downstream trusts this length.
	if len(plain) > maxDownloadBytes {
		return nil, errAttachmentTooLarge
	}
	return plain, nil
}

// readBounded reads at most maxDownloadBytes from body.
//
// Content-Length is checked first so an honestly-declared oversized response
// costs nothing, but it is only a hint: a response may declare a small length
// and then send far more, or declare nothing at all. The limited read is what
// actually holds, and it reads one byte past the cap so that hitting the cap
// is distinguishable from landing exactly on it.
func readBounded(contentLength int64, body io.Reader) ([]byte, error) {
	if contentLength > maxDownloadBytes {
		return nil, fmt.Errorf("%w (%d MB declared)", errAttachmentTooLarge, contentLength>>20)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxDownloadBytes {
		return nil, errAttachmentTooLarge
	}
	return data, nil
}
