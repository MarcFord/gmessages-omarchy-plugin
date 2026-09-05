package daemon

import (
	"strings"
	"unicode"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// initials derives up to two letters for the avatar fallback circle.
// Contacts that are only a phone number have no meaningful initials — digits
// would render as an arbitrary pair like "15" — so those get a neutral glyph.
func initials(name string) string {
	var out []rune
	for _, f := range strings.Fields(name) {
		for _, r := range f {
			if unicode.IsLetter(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "#"
	}
	return string(out)
}

func convParticipants(conv *gmproto.Conversation) []wire.Participant {
	src := conv.GetParticipants()
	if len(src) == 0 {
		return nil
	}
	out := make([]wire.Participant, 0, len(src))
	for _, p := range src {
		name := p.GetFullName()
		if name == "" {
			name = p.GetFirstName()
		}
		if name == "" {
			name = p.GetFormattedNumber()
		}
		out = append(out, wire.Participant{
			ID:          p.GetID().GetParticipantID(),
			Name:        name,
			Number:      p.GetFormattedNumber(),
			IsMe:        p.GetIsMe(),
			AvatarColor: p.GetAvatarHexColor(),
			Initials:    initials(name),
		})
	}
	return out
}

// convertConversation flattens a protobuf conversation for the plugin.
func convertConversation(conv *gmproto.Conversation) wire.Conversation {
	name := conv.GetName()
	if name == "" {
		// Unnamed 1:1 chats fall back to the other participant's number.
		for _, p := range conv.GetParticipants() {
			if !p.GetIsMe() {
				name = p.GetFormattedNumber()
				break
			}
		}
	}
	latest := conv.GetLatestMessage()
	return wire.Conversation{
		ID:           conv.GetConversationID(),
		Name:         name,
		Preview:      latest.GetDisplayContent(),
		PreviewMine:  latest.GetFromMe() == 1,
		Timestamp:    conv.GetLastMessageTimestamp(),
		Unread:       conv.GetUnread(),
		IsGroup:      conv.GetIsGroupChat(),
		ReadOnly:     conv.GetReadOnly(),
		Pinned:       conv.GetPinned(),
		AvatarColor:  conv.GetAvatarHexColor(),
		Initials:     initials(name),
		OutgoingID:   conv.GetDefaultOutgoingID(),
		Participants: convParticipants(conv),
	}
}

// imageMimes are rendered inline in the message view; everything else shows
// as a generic attachment chip.
func isImageMime(mime string) bool {
	return strings.HasPrefix(mime, "image/")
}

// convertMessage flattens a protobuf message, joining its text parts and
// listing its media parts. Media is referenced by ID only — the plugin asks
// for bytes separately so opening a thread stays cheap.
func convertMessage(msg *gmproto.Message, senderName string) wire.Message {
	var texts []string
	var atts []wire.Attachment
	for _, part := range msg.GetMessageInfo() {
		if mc := part.GetMessageContent(); mc != nil {
			if c := mc.GetContent(); c != "" {
				texts = append(texts, c)
			}
			continue
		}
		if md := part.GetMediaContent(); md != nil {
			atts = append(atts, wire.Attachment{
				Key:      attachmentKey(msg.GetMessageID(), part.GetActionMessageID(), md.GetMediaID()),
				MediaID:  md.GetMediaID(),
				Name:     md.GetMediaName(),
				MimeType: md.GetMimeType(),
				Size:     md.GetSize(),
				Width:    md.GetDimensions().GetWidth(),
				Height:   md.GetDimensions().GetHeight(),
				IsImage:  isImageMime(md.GetMimeType()),
			})
		}
	}

	status := msg.GetMessageStatus().GetStatus()
	out := wire.Message{
		ID:             msg.GetMessageID(),
		ConversationID: msg.GetConversationID(),
		Text:           strings.Join(texts, "\n"),
		Timestamp:      msg.GetTimestamp(),
		SenderID:       msg.GetParticipantID(),
		SenderName:     senderName,
		Status:         status.String(),
		Attachments:    atts,
		ReplyToID:      msg.GetReplyMessage().GetMessageID(),
	}
	out.FromMe = isOutgoingStatus(status)
	out.Failed = isFailedStatus(status)
	out.Pending = isPendingStatus(status)
	out.Deleted = isDeletedStatus(status)

	if sp := msg.GetSenderParticipant(); sp != nil && out.SenderName == "" {
		if n := sp.GetFullName(); n != "" {
			out.SenderName = n
		} else {
			out.SenderName = sp.GetFormattedNumber()
		}
	}

	for _, r := range msg.GetReactions() {
		out.Reactions = append(out.Reactions, wire.Reaction{
			Emoji: reactionEmoji(r),
			Count: len(r.GetParticipantIDs()),
		})
	}
	return out
}

func reactionEmoji(r *gmproto.ReactionEntry) string {
	d := r.GetData()
	if u := d.GetUnicode(); u != "" {
		return u
	}
	return d.GetType().String()
}

// The status enum mixes direction and delivery state into one field. Classify
// by name prefix rather than enumerating variants, so newly-added statuses
// (Google adds them regularly) still land in the right bucket.
func isOutgoingStatus(s gmproto.MessageStatusType) bool {
	return strings.HasPrefix(s.String(), "OUTGOING")
}

func isFailedStatus(s gmproto.MessageStatusType) bool {
	n := s.String()
	return strings.Contains(n, "FAILED") ||
		s == gmproto.MessageStatusType_OUTGOING_CANCELED ||
		s == gmproto.MessageStatusType_OUTGOING_RESTRICTED
}

// isDeletedStatus catches both directions; a deleted message carries no parts
// at all, so the UI has nothing to render unless it is labelled.
func isDeletedStatus(s gmproto.MessageStatusType) bool {
	return strings.Contains(s.String(), "DELETED")
}

func isPendingStatus(s gmproto.MessageStatusType) bool {
	switch s {
	case gmproto.MessageStatusType_OUTGOING_YET_TO_SEND,
		gmproto.MessageStatusType_OUTGOING_SENDING,
		gmproto.MessageStatusType_OUTGOING_SEND_AFTER_PROCESSING,
		gmproto.MessageStatusType_OUTGOING_RESENDING,
		gmproto.MessageStatusType_OUTGOING_AWAITING_RETRY,
		gmproto.MessageStatusType_OUTGOING_VALIDATING:
		return true
	}
	return false
}
