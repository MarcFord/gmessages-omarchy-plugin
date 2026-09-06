// Package wire defines the newline-delimited JSON protocol spoken between
// gmessagesd and the Quickshell plugin over a Unix socket.
//
// Every client frame is a Request. Every daemon frame is either a Response
// (correlated by ID) or an Event (ID empty). Keeping both directions on one
// stream lets the QML side hold a single Socket with a SplitParser.
package wire

// Request is a call from the plugin to the daemon.
type Request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// Response answers exactly one Request.
type Response struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Event is an unsolicited push from the daemon.
type Event struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

// Event names.
const (
	EventStatus       = "status"
	EventConversation = "conversation"
	EventMessage      = "message"
	EventQR           = "qr"
	EventEmoji        = "emoji"
	EventPaired       = "paired"
	EventTyping       = "typing"
)

// Method names.
const (
	MethodStatus        = "status"
	MethodConversations = "conversations"
	MethodMessages      = "messages"
	MethodSend          = "send"
	MethodMarkRead      = "markRead"
	MethodStartPairing  = "startPairing"
	MethodGaiaPairing   = "gaiaPairing"
	// MethodPairFromBrowser lets the widget pair on its own: the daemon finds
	// the browser profile and reads the cookies itself, so pairing never
	// requires dropping to a terminal.
	MethodPairFromBrowser = "pairFromBrowser"
	MethodSendMedia       = "sendMedia"
	MethodPickImage       = "pickImage"
	MethodListProfiles    = "listProfiles"
	MethodSetProfile      = "setProfile"
	MethodReact           = "react"
	MethodDiscardCapture  = "discardCapture"
	MethodUnpair          = "unpair"
	MethodMedia           = "media"
	MethodAvatar          = "avatar"
	MethodSetTyping       = "setTyping"
	MethodRefresh         = "refresh"
)

// ConnState describes where the daemon is in its lifecycle. The plugin keys
// its entire UI off this, so it must always be one of these.
type ConnState string

const (
	StateUnpaired ConnState = "unpaired"
	StatePairing  ConnState = "pairing"
	// StateGaiaPairing is the Google-account flow: no QR, an emoji the user
	// confirms on the phone. Newer Messages builds only offer this one.
	StateGaiaPairing  ConnState = "gaiaPairing"
	StateConnecting   ConnState = "connecting"
	StateConnected    ConnState = "connected"
	StateDisconnected ConnState = "disconnected"
	StateError        ConnState = "error"
)

// Status is both the reply to MethodStatus and the payload of EventStatus.
type Status struct {
	State   ConnState `json:"state"`
	Unread  int       `json:"unread"`
	PhoneOK bool      `json:"phoneOK"`
	QRURL   string    `json:"qrURL,omitempty"`
	Emoji   string    `json:"emoji,omitempty"`
	// Hint is a human-readable next step shown under an error, e.g. which
	// browser profile to sign in to. Kept separate from Error so the UI can
	// style the two differently.
	Hint        string `json:"hint,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Error       string `json:"error,omitempty"`
	SelfPhone   string `json:"selfPhone,omitempty"`
	LastSyncSec int64  `json:"lastSyncSec,omitempty"`
}

// Conversation is a flattened gmproto.Conversation, carrying only what the
// widget renders. Avatar is a local file path (or empty), already downloaded.
type Conversation struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Preview      string        `json:"preview"`
	PreviewMine  bool          `json:"previewMine"`
	Timestamp    int64         `json:"timestamp"`
	Unread       bool          `json:"unread"`
	IsGroup      bool          `json:"isGroup"`
	ReadOnly     bool          `json:"readOnly"`
	Pinned       bool          `json:"pinned"`
	AvatarColor  string        `json:"avatarColor"`
	AvatarPath   string        `json:"avatarPath,omitempty"`
	Initials     string        `json:"initials"`
	OutgoingID   string        `json:"outgoingID,omitempty"`
	Participants []Participant `json:"participants,omitempty"`
}

// Participant is one member of a conversation.
type Participant struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Number      string `json:"number,omitempty"`
	IsMe        bool   `json:"isMe"`
	AvatarColor string `json:"avatarColor,omitempty"`
	Initials    string `json:"initials"`
}

// Attachment is one media part of a message. Path is empty until the plugin
// asks for it with MethodMedia; the daemon downloads lazily so opening a
// conversation does not pull every image ever sent.
type Attachment struct {
	// Key is the handle the media method takes. Media IDs are often empty on
	// undownloaded MMS, so this falls back to the part's action-message ID and
	// is guaranteed non-empty.
	Key      string `json:"key"`
	MediaID  string `json:"mediaID"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Width    int64  `json:"width,omitempty"`
	Height   int64  `json:"height,omitempty"`
	IsImage  bool   `json:"isImage"`
	Path     string `json:"path,omitempty"`
}

// Message is a flattened gmproto.Message.
type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationID"`
	Text           string `json:"text"`
	Timestamp      int64  `json:"timestamp"`
	FromMe         bool   `json:"fromMe"`
	SenderID       string `json:"senderID,omitempty"`
	SenderName     string `json:"senderName,omitempty"`
	Status         string `json:"status,omitempty"`
	Failed         bool   `json:"failed"`
	Pending        bool   `json:"pending"`
	// Deleted messages arrive with no text and no media; without this the UI
	// would draw an empty bubble.
	Deleted     bool         `json:"deleted"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Reactions   []Reaction   `json:"reactions,omitempty"`
	ReplyToID   string       `json:"replyToID,omitempty"`
}

// Reaction is an emoji reaction with its count. Mine drives the toggle: the
// same emoji tapped twice removes it, a different one switches.
type Reaction struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine"`
}

// ReactParams toggles a reaction on a message. An empty Emoji removes whatever
// reaction the user currently has.
type ReactParams struct {
	ConversationID string `json:"conversationID"`
	MessageID      string `json:"messageID"`
	Emoji          string `json:"emoji"`
}

// SupportedReactions is the set Google Messages accepts. Anything else is sent
// as a custom emoji, which not every recipient can render, so the UI offers
// only these.
var SupportedReactions = []string{
	"\U0001F44D",   // thumbs up
	"\u2764\uFE0F", // red heart
	"\U0001F602",   // laugh
	"\U0001F62E",   // surprised
	"\U0001F625",   // sad
	"\U0001F620",   // angry
	"\U0001F44E",   // thumbs down
}

// --- Request params ---

type ConversationsParams struct {
	Count int `json:"count"`
}

type MessagesParams struct {
	ConversationID string `json:"conversationID"`
	Count          int64  `json:"count"`
	CursorID       string `json:"cursorID,omitempty"`
	CursorTime     int64  `json:"cursorTime,omitempty"`
}

type MessagesResult struct {
	ConversationID string    `json:"conversationID"`
	Messages       []Message `json:"messages"`
	CursorID       string    `json:"cursorID,omitempty"`
	CursorTime     int64     `json:"cursorTime,omitempty"`
	HasMore        bool      `json:"hasMore"`
}

type SendParams struct {
	ConversationID string `json:"conversationID"`
	Text           string `json:"text"`
	ReplyToID      string `json:"replyToID,omitempty"`
}

// GaiaPairingParams carries the Google cookies scraped from a signed-in
// messages.google.com session. SID, HSID, OSID, SSID, APISID and SAPISID are
// all required; most are HttpOnly, so they can only come from a real browser
// session, not from page script.
type GaiaPairingParams struct {
	Cookies map[string]string `json:"cookies"`
}

// RequiredGaiaCookies is what StartGaiaPairing refuses to proceed without.
var RequiredGaiaCookies = []string{"SID", "HSID", "OSID", "SSID", "APISID", "SAPISID"}

// SendMediaParams sends a local file to a conversation.
type SendMediaParams struct {
	ConversationID string `json:"conversationID"`
	Path           string `json:"path"`
	Caption        string `json:"caption,omitempty"`
}

// BrowserProfile describes one browser profile the daemon can read Google
// cookies from, and whether it is actually usable for pairing.
type BrowserProfile struct {
	Name     string   `json:"name"`
	Browser  string   `json:"browser"`
	Cookies  int      `json:"cookies"`
	Missing  []string `json:"missing,omitempty"`
	Usable   bool     `json:"usable"`
	Selected bool     `json:"selected"`
	// Reason explains an unusable profile in words the user can act on.
	Reason string `json:"reason,omitempty"`
}

// SetProfileParams chooses a profile by name; empty means automatic.
type SetProfileParams struct {
	Name string `json:"name"`
}

// DiscardCaptureParams removes a webcam capture the user rejected.
type DiscardCaptureParams struct {
	Path string `json:"path"`
}

// PickImageResult carries the chosen path, or empty when the user cancelled.
type PickImageResult struct {
	Path string `json:"path"`
}

type MarkReadParams struct {
	ConversationID string `json:"conversationID"`
	MessageID      string `json:"messageID"`
}

type MediaParams struct {
	Key string `json:"key"`
	// MediaID is accepted as a legacy alias for Key.
	MediaID string `json:"mediaID,omitempty"`
}

type MediaResult struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	// Pending means the full-size image had to be requested from the phone and
	// is not here yet; the client should retry shortly.
	Pending bool `json:"pending,omitempty"`
	// Thumbnail means Path is a low-resolution stand-in.
	Thumbnail bool `json:"thumbnail,omitempty"`
}

type SetTypingParams struct {
	ConversationID string `json:"conversationID"`
	Typing         bool   `json:"typing"`
}
