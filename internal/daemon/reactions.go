package daemon

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// reactionRecord remembers who left one reaction on a message.
type reactionRecord struct {
	emoji        string
	participants []string
}

// recordReactions captures the participant IDs behind each reaction as
// messages flow past, so React can tell the user's own reaction from everyone
// else's without another round trip.
func (d *Daemon) recordReactions(msg *gmproto.Message) {
	entries := msg.GetReactions()
	id := msg.GetMessageID()
	if id == "" {
		return
	}

	d.reactMu.Lock()
	defer d.reactMu.Unlock()
	if len(entries) == 0 {
		delete(d.reactions, id)
		return
	}
	records := make([]reactionRecord, 0, len(entries))
	for _, e := range entries {
		records = append(records, reactionRecord{
			emoji:        reactionEmoji(e),
			participants: e.GetParticipantIDs(),
		})
	}
	d.reactions[id] = records
}

// markMyReactions flags which reactions on a message are the user's own.
func (d *Daemon) markMyReactions(conversationID string, m *wire.Message) {
	if len(m.Reactions) == 0 {
		return
	}
	mine := d.myReactionOn(conversationID, m.ID)
	if mine == "" {
		return
	}
	for i := range m.Reactions {
		if m.Reactions[i].Emoji == mine {
			m.Reactions[i].Mine = true
		}
	}
}

// React toggles the user's reaction on a message.
//
// Google models this as three actions rather than one. Picking the wrong one
// fails silently or duplicates a reaction, so the action is derived from what
// the user already has on that message:
//
//	nothing yet          -> ADD
//	the same emoji again -> REMOVE  (tapping twice takes it back)
//	a different emoji    -> SWITCH
func (d *Daemon) React(ctx context.Context, p wire.ReactParams) error {
	c, err := d.requireClient()
	if err != nil {
		return err
	}
	if p.MessageID == "" {
		return errors.New("no message given")
	}

	existing := d.myReactionOn(p.ConversationID, p.MessageID)

	action := gmproto.SendReactionRequest_ADD
	emoji := p.Emoji
	switch {
	case existing == "" && emoji == "":
		return nil // nothing to remove
	case emoji == "" || emoji == existing:
		action = gmproto.SendReactionRequest_REMOVE
		emoji = existing
	case existing != "":
		action = gmproto.SendReactionRequest_SWITCH
	}

	req := &gmproto.SendReactionRequest{
		MessageID:    p.MessageID,
		ReactionData: gmproto.MakeReactionData(emoji),
		Action:       action,
	}

	resp, err := withAuthRetry(d, func() (*gmproto.SendReactionResponse, error) {
		return c.SendReaction(ctx, req)
	})
	if err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}
	if !resp.GetSuccess() {
		return errors.New("the phone rejected the reaction")
	}

	d.log.Debug().
		Str("message", p.MessageID).
		Str("emoji", emoji).
		Str("action", action.String()).
		Msg("Reaction sent")
	return nil
}

// myReactionOn reports which emoji the user currently has on a message, by
// matching the conversation's own participant ID against each reaction's
// participants.
func (d *Daemon) myReactionOn(conversationID, messageID string) string {
	d.mu.RLock()
	conv, ok := d.convs[conversationID]
	d.mu.RUnlock()
	if !ok {
		return ""
	}

	var mine []string
	for _, p := range conv.Participants {
		if p.IsMe && p.ID != "" {
			mine = append(mine, p.ID)
		}
	}
	if conv.OutgoingID != "" {
		mine = append(mine, conv.OutgoingID)
	}
	if len(mine) == 0 {
		return ""
	}

	d.reactMu.RLock()
	defer d.reactMu.RUnlock()
	for _, r := range d.reactions[messageID] {
		for _, participant := range r.participants {
			for _, self := range mine {
				if participant == self {
					return r.emoji
				}
			}
		}
	}
	return ""
}
