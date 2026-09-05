// Package daemon runs the Google Messages client and serves its state to the
// Quickshell plugin over a Unix socket.
package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/store"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// convListLimit caps how many conversations we hold and serve. The panel is a
// bar popup, not an archive browser.
const convListLimit = 50

// Daemon owns the libgm client and the cached view of the account.
type Daemon struct {
	log   zerolog.Logger
	paths *store.Paths

	mu      sync.RWMutex
	client  *libgm.Client
	auth    *libgm.AuthData
	status  wire.Status
	convs   map[string]wire.Conversation
	order   []string // conversation IDs, newest first
	selfIDs map[string]bool

	media   *mediaCache
	avatars *avatarStore
	config  *store.ConfigStore
	cookies cookieRefresher

	// Maintenance starts either after a stored session connects or after a
	// fresh pairing, whichever happens first, and must run exactly once. It is
	// tied to the daemon's own context so it stops with the daemon rather than
	// leaking past shutdown.
	maintOnce sync.Once
	maintCtx  context.Context

	// pairCancel stops the QR refresh loop when pairing ends, however it ends.
	pairCancel context.CancelFunc

	// paired gates session persistence. Signing in to Google fills in device
	// identity and refreshes the auth token BEFORE the phone has confirmed
	// anything, so persisting on every token refresh writes a session that
	// looks complete but is not. On the next start the daemon connects with
	// it, the server answers "logged out", and the session is discarded —
	// which looks exactly like a pairing that worked and then broke. Only a
	// PairSuccessful event flips this.
	paired bool

	subMu sync.Mutex
	subs  map[chan wire.Event]struct{}
}

// New builds a daemon around already-resolved paths.
func New(log zerolog.Logger, paths *store.Paths) *Daemon {
	return &Daemon{
		log:     log,
		paths:   paths,
		convs:   make(map[string]wire.Conversation),
		selfIDs: make(map[string]bool),
		subs:    make(map[chan wire.Event]struct{}),
		media:   newMediaCache(paths.MediaDir()),
		avatars: newAvatarStore(paths.MediaDir()),
		config:  store.NewConfigStore(paths.ConfigFile()),
		// PhoneOK starts true: it is only ever falsified by an explicit
		// PhoneNotResponding event. Starting false meant the bar read
		// "Phone not responding" forever, because the event that clears it
		// (PhoneRespondingAgain) only fires after a failure that never
		// happened.
		status: wire.Status{State: wire.StateUnpaired, PhoneOK: true},
	}
}

// Start loads any stored session and connects, or parks in the unpaired state
// waiting for the plugin to ask for a QR code.
func (d *Daemon) Start(ctx context.Context) error {
	// Held so maintenance started later (after pairing) still stops with the
	// daemon instead of outliving it.
	d.maintCtx = ctx

	auth, paired, err := d.paths.LoadSession()
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	d.mu.Lock()
	d.auth = auth
	d.client = libgm.NewClient(auth, nil, d.log.With().Str("component", "libgm").Logger())
	d.client.SetEventHandler(d.handleEvent)
	d.mu.Unlock()

	if !paired {
		d.setState(wire.StateUnpaired, "")
		d.log.Info().Msg("No stored session; waiting for pairing")
		return nil
	}
	// Only completed pairings are ever written, so a session on disk means
	// this daemon may persist refreshed tokens from here on.
	d.mu.Lock()
	d.paired = true
	d.mu.Unlock()

	d.setState(wire.StateConnecting, "")
	if err := d.client.Connect(); err != nil {
		// A failed connect is recoverable (phone offline, token expired), so
		// surface it rather than exiting; the plugin shows a retry affordance.
		d.setState(wire.StateError, err.Error())
		d.log.Warn().Err(err).Msg("Initial connect failed")
		return nil
	}
	go d.initialSync()
	d.startMaintenance()
	return nil
}

// startMaintenance launches the credential upkeep loop at most once.
func (d *Daemon) startMaintenance() {
	d.maintOnce.Do(func() {
		ctx := d.maintCtx
		if ctx == nil {
			ctx = context.Background()
		}
		go d.runMaintenance(ctx)
	})
}

// initialSync brings a reconnected session up to date.
//
// libgm does not emit ClientReady when resuming a stored session — readiness
// only shows up as ListenRecovered — so nothing else pulls the conversation
// list on start. Without this the panel shows "No conversations yet" until the
// user hits refresh, which reads exactly like a broken pairing.
func (d *Daemon) initialSync() {
	// Give the long poll a moment to establish; a fetch issued before the
	// phone is reachable just burns one of the attempts below.
	time.Sleep(3 * time.Second)
	d.setState(wire.StateConnected, "")

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		err := d.Refresh(ctx)
		cancel()
		if err == nil {
			d.mu.Lock()
			// The phone just answered a request, so it is demonstrably alive.
			d.status.PhoneOK = true
			n := len(d.convs)
			d.mu.Unlock()
			d.publishStatus()
			d.log.Info().Int("conversations", n).Msg("Initial sync complete")
			return
		}
		d.log.Warn().Err(err).Int("attempt", attempt).Msg("Initial conversation fetch failed")
		time.Sleep(time.Duration(attempt*5) * time.Second)
	}
}

// Stop disconnects cleanly and persists the session.
func (d *Daemon) Stop() {
	d.mu.RLock()
	c := d.client
	d.mu.RUnlock()
	if c != nil {
		c.Disconnect()
	}
	d.saveSession()
}

func (d *Daemon) saveSession() {
	d.mu.RLock()
	auth := d.auth
	paired := d.paired
	d.mu.RUnlock()
	if auth == nil {
		return
	}
	if !paired {
		d.log.Debug().Msg("Not persisting session: pairing has not completed")
		return
	}
	if err := d.paths.SaveSession(auth); err != nil {
		d.log.Error().Err(err).Msg("Failed to save session")
	}
}

// --- event fan-out ---

// Subscribe returns a channel of daemon events plus a cancel func. The channel
// is buffered and lossy on overflow: a wedged UI must not stall the client.
func (d *Daemon) Subscribe() (<-chan wire.Event, func()) {
	ch := make(chan wire.Event, 64)
	d.subMu.Lock()
	d.subs[ch] = struct{}{}
	d.subMu.Unlock()
	return ch, func() {
		d.subMu.Lock()
		if _, ok := d.subs[ch]; ok {
			delete(d.subs, ch)
			close(ch)
		}
		d.subMu.Unlock()
	}
}

func (d *Daemon) publish(name string, data any) {
	evt := wire.Event{Event: name, Data: data}
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for ch := range d.subs {
		select {
		case ch <- evt:
		default:
			d.log.Warn().Str("event", name).Msg("Subscriber lagging, dropping event")
		}
	}
}

func (d *Daemon) setState(state wire.ConnState, errMsg string) {
	d.mu.Lock()
	d.status.State = state
	d.status.Error = errMsg
	// Hint belongs to the error it was set with; a new state invalidates it.
	d.status.Hint = ""
	if state != wire.StatePairing {
		d.status.QRURL = ""
	}
	if state != wire.StateGaiaPairing {
		d.status.Emoji = ""
	}
	d.status.Unread = d.unreadCountLocked()
	st := d.status
	d.mu.Unlock()
	d.publish(wire.EventStatus, st)
}

func (d *Daemon) unreadCountLocked() int {
	n := 0
	for _, c := range d.convs {
		if c.Unread {
			n++
		}
	}
	return n
}

// publishStatus recomputes and pushes status without changing state.
func (d *Daemon) publishStatus() {
	d.mu.Lock()
	d.status.Unread = d.unreadCountLocked()
	st := d.status
	d.mu.Unlock()
	d.publish(wire.EventStatus, st)
}

// --- libgm event handling ---

func (d *Daemon) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *events.ClientReady:
		d.log.Info().Int("conversations", len(evt.Conversations)).Msg("Client ready")
		d.replaceConversations(evt.Conversations)
		d.mu.Lock()
		d.status.LastSyncSec = time.Now().Unix()
		d.mu.Unlock()
		d.setState(wire.StateConnected, "")

	case *events.PairSuccessful:
		d.log.Info().Msg("Pairing successful")
		d.stopPairRefresh()
		d.mu.Lock()
		d.paired = true
		d.mu.Unlock()
		d.startMaintenance()
		d.saveSession()
		d.publish(wire.EventPaired, nil)
		d.setState(wire.StateConnected, "")

	case *events.AuthTokenRefreshed:
		d.saveSession()

	case *gmproto.Conversation:
		d.upsertConversation(evt)

	case *libgm.WrappedMessage:
		if evt.IsOld {
			return
		}
		d.handleMessage(evt.Message)

	case *gmproto.Message:
		d.handleMessage(evt)

	case *events.BrowserActive:
		d.log.Debug().Msg("Browser active elsewhere")

	case *events.PhoneNotResponding:
		d.mu.Lock()
		d.status.PhoneOK = false
		d.mu.Unlock()
		d.publishStatus()

	case *events.PhoneRespondingAgain:
		d.mu.Lock()
		d.status.PhoneOK = true
		d.mu.Unlock()
		d.publishStatus()

	case *events.ListenTemporaryError:
		d.setState(wire.StateDisconnected, "connection interrupted")

	case *events.ListenRecovered:
		d.setState(wire.StateConnected, "")
		// Anything that arrived during the outage was missed, so re-pull.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if err := d.Refresh(ctx); err != nil {
				d.log.Debug().Err(err).Msg("Re-sync after recovery failed")
			}
		}()

	case *events.ListenFatalError:
		// The long poll runs inside libgm, outside withAuthRetry, so an
		// invalidated session shows up here rather than as a failed request.
		if isAuthError(evt.Error) {
			go func() {
				if d.repairSession() {
					return
				}
				d.setState(wire.StateError, "Google sign-in expired. Open messages.google.com/web, then reopen this panel.")
			}()
			return
		}
		d.setState(wire.StateError, fmt.Sprintf("%v", evt.Error))

	case *events.GaiaLoggedOut:
		d.log.Warn().Msg("Logged out by server")
		d.mu.Lock()
		d.paired = false
		d.mu.Unlock()
		_ = d.paths.ClearSession()
		d.setState(wire.StateUnpaired, "Google signed this device out — pair again")

	default:
		d.log.Trace().Type("type", raw).Msg("Unhandled libgm event")
	}
}

func (d *Daemon) handleMessage(msg *gmproto.Message) {
	// Attachment decryption keys only ever arrive attached to a message, so
	// harvest them on the way past; Media() needs them later, on demand.
	d.media.record(msg)
	m := convertMessage(msg, d.senderName(msg))
	d.publish(wire.EventMessage, m)
	// The conversation list preview and unread dot both derive from
	// conversation events, but those can lag behind the message itself.
	d.touchConversation(m)
}

func (d *Daemon) senderName(msg *gmproto.Message) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	conv, ok := d.convs[msg.GetConversationID()]
	if !ok {
		return ""
	}
	for _, p := range conv.Participants {
		if p.ID == msg.GetParticipantID() {
			return p.Name
		}
	}
	return ""
}

// touchConversation moves a conversation to the top of the list when a message
// lands, so the UI reorders immediately instead of waiting for a sync.
func (d *Daemon) touchConversation(m wire.Message) {
	d.mu.Lock()
	conv, ok := d.convs[m.ConversationID]
	if !ok {
		d.mu.Unlock()
		return
	}
	if m.Timestamp >= conv.Timestamp {
		conv.Timestamp = m.Timestamp
		conv.PreviewMine = m.FromMe
		if m.Text != "" {
			conv.Preview = m.Text
		} else if len(m.Attachments) > 0 {
			conv.Preview = "Attachment"
		}
		if !m.FromMe {
			conv.Unread = true
		}
		d.convs[m.ConversationID] = conv
		d.reorderLocked()
	}
	out := d.convs[m.ConversationID]
	d.mu.Unlock()
	d.publish(wire.EventConversation, out)
	d.publishStatus()
}

func (d *Daemon) replaceConversations(convs []*gmproto.Conversation) {
	d.mu.Lock()
	d.convs = make(map[string]wire.Conversation, len(convs))
	for _, c := range convs {
		w := convertConversation(c)
		if p, ok := d.avatars.cached(w.ID); ok {
			w.AvatarPath = p
		}
		d.convs[w.ID] = w
		for _, p := range w.Participants {
			if p.IsMe {
				d.selfIDs[p.ID] = true
			}
		}
	}
	d.reorderLocked()
	list := d.listLocked()
	d.mu.Unlock()

	for _, c := range list {
		d.publish(wire.EventConversation, c)
	}

	go d.fetchAvatars(context.Background(), convs)
}

func (d *Daemon) upsertConversation(c *gmproto.Conversation) {
	w := convertConversation(c)
	d.mu.Lock()
	if p, ok := d.avatars.cached(w.ID); ok {
		w.AvatarPath = p
	}
	d.convs[w.ID] = w
	d.reorderLocked()
	d.mu.Unlock()
	d.publish(wire.EventConversation, w)
	d.publishStatus()
}

// reorderLocked rebuilds the ordering: pinned first, then newest-first.
func (d *Daemon) reorderLocked() {
	ids := make([]string, 0, len(d.convs))
	for id := range d.convs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := d.convs[ids[i]], d.convs[ids[j]]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Timestamp != b.Timestamp {
			return a.Timestamp > b.Timestamp
		}
		return a.ID < b.ID
	})
	if len(ids) > convListLimit {
		ids = ids[:convListLimit]
	}
	d.order = ids
}

func (d *Daemon) listLocked() []wire.Conversation {
	out := make([]wire.Conversation, 0, len(d.order))
	for _, id := range d.order {
		out = append(out, d.convs[id])
	}
	return out
}
