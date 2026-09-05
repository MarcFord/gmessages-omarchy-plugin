package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// avatarSource records where a conversation's picture can be fetched from.
// Group chats expose a URL; 1:1 chats need a participant thumbnail lookup,
// and only participants with a contact ID have one at all.
type avatarSource struct {
	groupURL      string
	participantID string
}

// avatarStore caches fetched avatars on disk and remembers which conversations
// have already been tried, so a resync does not refetch every picture.
type avatarStore struct {
	dir string

	mu    sync.Mutex
	tried map[string]bool
	paths map[string]string
}

func newAvatarStore(dir string) *avatarStore {
	return &avatarStore{
		dir:   dir,
		tried: make(map[string]bool),
		paths: make(map[string]string),
	}
}

func (a *avatarStore) pathFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(a.dir, "avatar-"+hex.EncodeToString(sum[:12])+".png")
}

func (a *avatarStore) cached(convID string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.paths[convID]
	return p, ok
}

// claim marks a conversation as attempted, returning false if it already was.
func (a *avatarStore) claim(convID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tried[convID] {
		return false
	}
	a.tried[convID] = true
	return true
}

func (a *avatarStore) store(convID, path string) {
	a.mu.Lock()
	a.paths[convID] = path
	a.mu.Unlock()
}

// avatarSourceFor picks the best available avatar source for a conversation,
// or false when there is nothing worth asking for.
func avatarSourceFor(conv *gmproto.Conversation) (avatarSource, bool) {
	if url := conv.GetGroupAvatarURL(); url != "" {
		return avatarSource{groupURL: url}, true
	}
	if conv.GetIsGroupChat() {
		return avatarSource{}, false
	}
	for _, p := range conv.GetParticipants() {
		// Only saved contacts have a server-side thumbnail; asking for a bare
		// phone number is a guaranteed round trip for nothing.
		if !p.GetIsMe() && p.GetContactID() != "" {
			return avatarSource{participantID: p.GetID().GetParticipantID()}, true
		}
	}
	return avatarSource{}, false
}

// fetchAvatars pulls conversation pictures in the background and republishes
// each conversation as its avatar lands. Failures are silent: an avatar is
// decoration, and the initials fallback already renders.
func (d *Daemon) fetchAvatars(ctx context.Context, convs []*gmproto.Conversation) {
	for _, conv := range convs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		convID := conv.GetConversationID()
		src, ok := avatarSourceFor(conv)
		if !ok || !d.avatars.claim(convID) {
			continue
		}

		data, err := d.fetchOneAvatar(ctx, src)
		if err != nil || len(data) == 0 {
			continue
		}

		path := d.avatars.pathFor(convID)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			d.log.Debug().Err(err).Msg("Could not cache avatar")
			continue
		}
		d.avatars.store(convID, path)

		d.mu.Lock()
		w, exists := d.convs[convID]
		if exists {
			w.AvatarPath = path
			d.convs[convID] = w
		}
		d.mu.Unlock()
		if exists {
			d.publish(wire.EventConversation, w)
		}

		// Space the requests out; this is background decoration and must not
		// compete with the user's actual reads and sends.
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (d *Daemon) fetchOneAvatar(ctx context.Context, src avatarSource) ([]byte, error) {
	c, err := d.requireClient()
	if err != nil {
		return nil, err
	}
	if src.groupURL != "" {
		return c.DownloadAvatar(ctx, src.groupURL)
	}
	resp, err := c.GetParticipantThumbnail(ctx, src.participantID)
	if err != nil {
		return nil, err
	}
	thumbs := resp.GetThumbnail()
	if len(thumbs) == 0 {
		return nil, nil
	}
	return thumbs[0].GetData().GetImageBuffer(), nil
}
