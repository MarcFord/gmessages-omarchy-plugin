package daemon

import (
	"errors"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"
)

func TestInitials(t *testing.T) {
	cases := map[string]string{
		"":                "#",
		"Ada Lovelace":    "AL",
		"ada":             "A",
		"+1 555 010 1234": "#",
		"555-0100":        "#",
		"Ada B. Lovelace": "AB",
		"  Ada  ":         "A",
	}
	for in, want := range cases {
		if got := initials(in); got != want {
			t.Errorf("initials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusClassification(t *testing.T) {
	// Direction is derived from the enum name, so an unrecognised OUTGOING_*
	// value must still classify as outgoing.
	if !isOutgoingStatus(gmproto.MessageStatusType_OUTGOING_DELIVERED) {
		t.Error("OUTGOING_DELIVERED should be outgoing")
	}
	if isOutgoingStatus(gmproto.MessageStatusType_INCOMING_COMPLETE) {
		t.Error("INCOMING_COMPLETE should not be outgoing")
	}
	if !isFailedStatus(gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC) {
		t.Error("OUTGOING_FAILED_GENERIC should be failed")
	}
	if isFailedStatus(gmproto.MessageStatusType_OUTGOING_COMPLETE) {
		t.Error("OUTGOING_COMPLETE should not be failed")
	}
	if !isPendingStatus(gmproto.MessageStatusType_OUTGOING_SENDING) {
		t.Error("OUTGOING_SENDING should be pending")
	}
}

func TestConvertMessageJoinsPartsAndMedia(t *testing.T) {
	msg := &gmproto.Message{
		MessageID:      "m1",
		ConversationID: "c1",
		Timestamp:      1700000000000000,
		MessageStatus:  &gmproto.MessageStatus{Status: gmproto.MessageStatusType_OUTGOING_COMPLETE},
		MessageInfo: []*gmproto.MessageInfo{
			{Data: &gmproto.MessageInfo_MessageContent{
				MessageContent: &gmproto.MessageContent{Content: "hello"},
			}},
			{Data: &gmproto.MessageInfo_MediaContent{
				MediaContent: &gmproto.MediaContent{
					MediaID: "med1", MimeType: "image/jpeg", MediaName: "pic.jpg", Size: 2048,
				},
			}},
			{Data: &gmproto.MessageInfo_MessageContent{
				MessageContent: &gmproto.MessageContent{Content: "world"},
			}},
		},
	}

	got := convertMessage(msg, "Ada")
	if got.Text != "hello\nworld" {
		t.Errorf("text = %q, want %q", got.Text, "hello\nworld")
	}
	if !got.FromMe {
		t.Error("expected FromMe for OUTGOING_COMPLETE")
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(got.Attachments))
	}
	att := got.Attachments[0]
	if att.MediaID != "med1" || !att.IsImage || att.Size != 2048 {
		t.Errorf("unexpected attachment: %+v", att)
	}
	if got.SenderName != "Ada" {
		t.Errorf("senderName = %q, want Ada", got.SenderName)
	}
}

func TestConvertConversationFallsBackToParticipantNumber(t *testing.T) {
	conv := &gmproto.Conversation{
		ConversationID: "c1",
		Participants: []*gmproto.Participant{
			{IsMe: true, FormattedNumber: "+15550000000"},
			{IsMe: false, FormattedNumber: "+15551111111"},
		},
		LatestMessage: &gmproto.LatestMessage{DisplayContent: "hi", FromMe: 1},
	}
	got := convertConversation(conv)
	if got.Name != "+15551111111" {
		t.Errorf("name = %q, want the other participant's number", got.Name)
	}
	if !got.PreviewMine {
		t.Error("expected PreviewMine when latest message is FromMe")
	}
}

func TestMediaCacheRecordsKeys(t *testing.T) {
	mc := newMediaCache(t.TempDir())
	mc.record(&gmproto.Message{
		MessageInfo: []*gmproto.MessageInfo{
			{Data: &gmproto.MessageInfo_MediaContent{
				MediaContent: &gmproto.MediaContent{
					MediaID: "med1", DecryptionKey: []byte("key"), MimeType: "image/png",
				},
			}},
		},
	})
	s, ok := mc.secret("med1")
	if !ok {
		t.Fatal("expected secret to be recorded")
	}
	if string(s.key) != "key" || s.mimeType != "image/png" {
		t.Errorf("unexpected secret: %+v", s)
	}
	if _, ok := mc.secret("nope"); ok {
		t.Error("unknown media should not resolve")
	}
	// Paths must be stable and filesystem-safe even for awkward media IDs.
	p1 := mc.path("a/b:c", "image/png")
	p2 := mc.path("a/b:c", "image/png")
	if p1 != p2 {
		t.Error("path should be stable for the same media ID")
	}
}

var _ = proto.Marshal

func TestAvatarSourceSelection(t *testing.T) {
	// A group with a picture uses its URL.
	src, ok := avatarSourceFor(&gmproto.Conversation{
		IsGroupChat: true, GroupAvatarURL: "https://example.invalid/a.png",
	})
	if !ok || src.groupURL == "" {
		t.Errorf("group with URL should use it, got %+v ok=%v", src, ok)
	}

	// A group without one has nothing to fetch.
	if _, ok := avatarSourceFor(&gmproto.Conversation{IsGroupChat: true}); ok {
		t.Error("group without avatar URL should have no source")
	}

	// A 1:1 chat resolves to the other party, but only when they are a saved
	// contact — a bare number has no server-side thumbnail.
	src, ok = avatarSourceFor(&gmproto.Conversation{
		Participants: []*gmproto.Participant{
			{IsMe: true, ContactID: "me", ID: &gmproto.SmallInfo{ParticipantID: "p0"}},
			{IsMe: false, ContactID: "c1", ID: &gmproto.SmallInfo{ParticipantID: "p1"}},
		},
	})
	if !ok || src.participantID != "p1" {
		t.Errorf("expected participant p1, got %+v ok=%v", src, ok)
	}

	if _, ok := avatarSourceFor(&gmproto.Conversation{
		Participants: []*gmproto.Participant{
			{IsMe: false, ID: &gmproto.SmallInfo{ParticipantID: "p1"}},
		},
	}); ok {
		t.Error("participant without contact ID should have no source")
	}
}

func TestAvatarStoreClaimsOnce(t *testing.T) {
	a := newAvatarStore(t.TempDir())
	if !a.claim("c1") {
		t.Error("first claim should succeed")
	}
	if a.claim("c1") {
		t.Error("second claim should be refused so avatars are not refetched")
	}
	if _, ok := a.cached("c1"); ok {
		t.Error("claiming should not imply a cached path")
	}
	a.store("c1", "/tmp/x.png")
	if p, ok := a.cached("c1"); !ok || p != "/tmp/x.png" {
		t.Errorf("cached = %q, %v", p, ok)
	}
}

func TestDeletedMessagesAreLabelled(t *testing.T) {
	// A deleted message has no parts at all; without the flag the UI would
	// render an empty bubble.
	msg := &gmproto.Message{
		MessageID:     "m1",
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_DELETED},
	}
	got := convertMessage(msg, "")
	if !got.Deleted {
		t.Error("INCOMING_DELETED should set Deleted")
	}
	if got.Text != "" || len(got.Attachments) != 0 {
		t.Error("a deleted message should carry no content")
	}
	if convertMessage(&gmproto.Message{
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
	}, "").Deleted {
		t.Error("a normal message must not be marked deleted")
	}
}

func TestIsAuthError(t *testing.T) {
	// The exact shape Google returns when session cookies have gone stale.
	real := errors.New("HTTP 401: 16: Request had invalid authentication credentials. " +
		"Expected OAuth 2 access token, login cookie or other valid authentication credential.")
	if !isAuthError(real) {
		t.Error("the real 401 body should be recognised as an auth error")
	}
	if !isAuthError(errors.New("unauthenticated")) {
		t.Error("UNAUTHENTICATED should count")
	}
	if isAuthError(nil) {
		t.Error("nil is not an auth error")
	}
	if isAuthError(errors.New("phone did not respond to request in 1m0s")) {
		t.Error("a phone timeout must not trigger a cookie refresh")
	}
	if isAuthError(errors.New("connection reset by peer")) {
		t.Error("a transport error must not trigger a cookie refresh")
	}
}

func TestDetectMimeSniffsContent(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	// A wrong extension must not win over the actual bytes, or the phone
	// rejects the upload.
	if got := detectMime(png, "photo.jpg"); got != "image/png" {
		t.Errorf("content should beat extension: got %q", got)
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0}
	if got := detectMime(jpeg, "x.bin"); got != "image/jpeg" {
		t.Errorf("jpeg sniff failed: got %q", got)
	}
	// Unsniffable content falls back to the extension.
	if got := detectMime([]byte{0, 0, 0, 0}, "clip.mp4"); got != "video/mp4" {
		t.Errorf("extension fallback failed: got %q", got)
	}
}

func TestURIToPath(t *testing.T) {
	p, err := uriToPath("file:///home/u/My%20Pictures/a%20b.png")
	if err != nil || p != "/home/u/My Pictures/a b.png" {
		t.Errorf("got %q, %v", p, err)
	}
	if _, err := uriToPath("https://example.invalid/a.png"); err == nil {
		t.Error("non-file URIs should be rejected")
	}
}

func TestSessionInvalidDetection(t *testing.T) {
	// The exact marker Google returns when the web session is gone. Fresh
	// cookies cannot fix this one, so it must be told apart from an ordinary
	// 401 or the daemon retries forever with credentials that cannot work.
	dead := errors.New(`HTTP 401: [["type.googleapis.com/google.rpc.ErrorInfo",` +
		`["SESSION_COOKIE_INVALID","googleapis.com"]]]`)
	if !isSessionInvalid(dead) {
		t.Error("SESSION_COOKIE_INVALID should be recognised")
	}
	if !isAuthError(dead) {
		t.Error("it is still an auth error")
	}
	plain := errors.New("HTTP 401: invalid authentication credentials")
	if isSessionInvalid(plain) {
		t.Error("a plain 401 is recoverable with fresh cookies, not a dead session")
	}
	if isSessionInvalid(nil) {
		t.Error("nil is not a dead session")
	}
}

func TestChangedCookiesNamesOnly(t *testing.T) {
	auth := libgm.NewAuthData()
	auth.SetCookies(map[string]string{"SID": "old", "HSID": "same", "OSID": "gone"})

	changed := changedCookies(auth, map[string]string{
		"SID":              "new",  // rotated
		"HSID":             "same", // unchanged
		"__Secure-1PSIDTS": "brand-new",
	})

	want := []string{"SID", "__Secure-1PSIDTS"}
	if len(changed) != len(want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	for i := range want {
		if changed[i] != want[i] {
			t.Fatalf("changed = %v, want %v (sorted)", changed, want)
		}
	}

	// Values are credentials; only names may be reported.
	for _, name := range changed {
		if name == "new" || name == "brand-new" {
			t.Error("a cookie value leaked into the changed list")
		}
	}

	// Identical sets must produce no work.
	auth.SetCookies(map[string]string{"SID": "x"})
	if got := changedCookies(auth, map[string]string{"SID": "x"}); len(got) != 0 {
		t.Errorf("identical cookies should report no change, got %v", got)
	}
}

func TestMyReactionOnIdentifiesOwnReaction(t *testing.T) {
	d := &Daemon{
		convs:     map[string]wire.Conversation{},
		reactions: map[string][]reactionRecord{},
	}
	d.convs["c1"] = wire.Conversation{
		ID:         "c1",
		OutgoingID: "me",
		Participants: []wire.Participant{
			{ID: "me", IsMe: true},
			{ID: "them", IsMe: false},
		},
	}
	d.reactions["m1"] = []reactionRecord{
		{emoji: "\U0001F44D", participants: []string{"them"}},
		{emoji: "❤️", participants: []string{"them", "me"}},
	}

	// The heart is the user's; the thumbs up belongs to someone else.
	if got := d.myReactionOn("c1", "m1"); got != "❤️" {
		t.Errorf("myReactionOn = %q, want the heart", got)
	}
	// A message with no reactions must not report one.
	if got := d.myReactionOn("c1", "m2"); got != "" {
		t.Errorf("expected no reaction, got %q", got)
	}
	// An unknown conversation must not panic or guess.
	if got := d.myReactionOn("nope", "m1"); got != "" {
		t.Errorf("expected no reaction for unknown conversation, got %q", got)
	}
}

func TestMarkMyReactionsFlagsOnlyMine(t *testing.T) {
	d := &Daemon{
		convs:     map[string]wire.Conversation{},
		reactions: map[string][]reactionRecord{},
	}
	d.convs["c1"] = wire.Conversation{ID: "c1", OutgoingID: "me"}
	d.reactions["m1"] = []reactionRecord{
		{emoji: "\U0001F602", participants: []string{"me"}},
	}

	m := wire.Message{ID: "m1", ConversationID: "c1", Reactions: []wire.Reaction{
		{Emoji: "\U0001F602", Count: 2},
		{Emoji: "\U0001F44D", Count: 1},
	}}
	d.markMyReactions("c1", &m)

	if !m.Reactions[0].Mine {
		t.Error("the laugh is the user's own and should be flagged")
	}
	if m.Reactions[1].Mine {
		t.Error("the thumbs up is not the user's and must not be flagged")
	}
}

func TestRecordReactionsClearsWhenRemoved(t *testing.T) {
	d := &Daemon{reactions: map[string][]reactionRecord{}}
	d.reactions["m1"] = []reactionRecord{{emoji: "x", participants: []string{"me"}}}

	// A message update with no reactions means they were all removed; a stale
	// entry here would make a later tap send REMOVE for something gone.
	d.recordReactions(&gmproto.Message{MessageID: "m1"})
	if _, ok := d.reactions["m1"]; ok {
		t.Error("reactions should be cleared when a message reports none")
	}
}

func TestDeliveryState(t *testing.T) {
	cases := map[gmproto.MessageStatusType]string{
		gmproto.MessageStatusType_OUTGOING_DISPLAYED:         wire.DeliveryRead,
		gmproto.MessageStatusType_OUTGOING_DELIVERED:         wire.DeliveryDelivered,
		gmproto.MessageStatusType_OUTGOING_COMPLETE:          wire.DeliverySent,
		gmproto.MessageStatusType_OUTGOING_NOT_DELIVERED_YET: wire.DeliverySent,
		gmproto.MessageStatusType_OUTGOING_SENDING:           wire.DeliverySending,
		gmproto.MessageStatusType_OUTGOING_YET_TO_SEND:       wire.DeliverySending,
		gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC:    wire.DeliveryFailed,
		// Incoming messages have no receipt of their own.
		gmproto.MessageStatusType_INCOMING_COMPLETE: "",
	}
	for status, want := range cases {
		if got := deliveryState(status); got != want {
			t.Errorf("deliveryState(%s) = %q, want %q", status, got, want)
		}
	}
}

func TestConvertMessageSetsDeliveryOnlyForOutgoing(t *testing.T) {
	out := convertMessage(&gmproto.Message{
		MessageID:     "m1",
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_OUTGOING_DISPLAYED},
	}, "")
	if out.Delivery != wire.DeliveryRead {
		t.Errorf("outgoing delivery = %q, want read", out.Delivery)
	}

	in := convertMessage(&gmproto.Message{
		MessageID:     "m2",
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
	}, "")
	if in.Delivery != "" {
		t.Errorf("incoming delivery = %q, want empty: a receipt on someone else's message is meaningless", in.Delivery)
	}
}
