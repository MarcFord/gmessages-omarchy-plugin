package daemon

import (
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// Every attachment the daemon sees used to be remembered for the life of the
// process, including its decryption keys and any inline bytes.
func TestMediaSecretsAreBounded(t *testing.T) {
	m := newMediaCache(t.TempDir())
	for i := 0; i < maxSecrets+500; i++ {
		m.record(&gmproto.Message{
			MessageID: fmt.Sprintf("msg-%d", i),
			MessageInfo: []*gmproto.MessageInfo{{
				ActionMessageID: proto.String(fmt.Sprintf("part-%d", i)),
				Data: &gmproto.MessageInfo_MediaContent{
					MediaContent: &gmproto.MediaContent{
						MediaID:       fmt.Sprintf("media-%d", i),
						DecryptionKey: []byte("0123456789abcdef"),
					},
				},
			}},
		})
	}
	if len(m.secrets) > maxSecrets {
		t.Errorf("secrets grew to %d, past the %d cap", len(m.secrets), maxSecrets)
	}
	if len(m.order) > maxSecrets {
		t.Errorf("order grew to %d, past the %d cap", len(m.order), maxSecrets)
	}
	// The most recent must survive; that is the one the user is looking at.
	last := attachmentKey(fmt.Sprintf("msg-%d", maxSecrets+499), fmt.Sprintf("part-%d", maxSecrets+499), fmt.Sprintf("media-%d", maxSecrets+499))
	if _, ok := m.secrets[last]; !ok {
		t.Error("the newest secret should have been kept")
	}
}

func TestReactionRecordsAreBounded(t *testing.T) {
	d := &Daemon{reactions: make(map[string][]reactionRecord)}
	for i := 0; i < maxReactionRecords+500; i++ {
		d.recordReactions(&gmproto.Message{
			MessageID: fmt.Sprintf("msg-%d", i),
			Reactions: []*gmproto.ReactionEntry{{
				Data: &gmproto.ReactionData{Unicode: "\U0001F44D"},
			}},
		})
	}
	if len(d.reactions) > maxReactionRecords {
		t.Errorf("reactions grew to %d, past the %d cap", len(d.reactions), maxReactionRecords)
	}
	if len(d.reactionOrder) > maxReactionRecords {
		t.Errorf("reactionOrder grew to %d, past the %d cap", len(d.reactionOrder), maxReactionRecords)
	}
}
