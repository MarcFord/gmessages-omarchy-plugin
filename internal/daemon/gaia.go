package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// gaiaPairTimeout bounds how long we wait for the phone to confirm. The phone's
// own verification window is shorter than this, so hitting it means the prompt
// was never seen rather than that it needs longer.
const gaiaPairTimeout = 3 * time.Minute

// StartGaiaPairing runs the Google-account pairing flow.
//
// Newer Google Messages builds have dropped the QR scanner entirely — the
// phone's "Device pairing" screen only offers "sign in to the same Google
// Account". That flow authenticates with cookies lifted from a signed-in
// messages.google.com session and confirms with an emoji shown on both ends,
// rather than a scannable code.
//
// FinishGaiaPairing blocks until the phone answers, so the whole exchange runs
// in the background and reports progress through events.
func (d *Daemon) StartGaiaPairing(cookies map[string]string) error {
	if err := validateGaiaCookies(cookies); err != nil {
		return err
	}

	d.mu.RLock()
	c := d.client
	auth := d.auth
	d.mu.RUnlock()
	if c == nil || auth == nil {
		return fmt.Errorf("client not initialised")
	}

	// A QR attempt may still be refreshing from an earlier try.
	d.stopPairRefresh()

	auth.SetCookies(cookies)

	d.mu.Lock()
	d.status.State = wire.StateGaiaPairing
	d.status.Error = ""
	d.status.QRURL = ""
	d.status.Emoji = ""
	st := d.status
	d.mu.Unlock()
	d.publish(wire.EventStatus, st)

	go func() {
		// FinishGaiaPairing blocks on the phone with no timeout of its own, so
		// an unanswered prompt would otherwise wedge the daemon in
		// "gaiaPairing" forever with no way back except a restart.
		ctx, cancel := context.WithTimeout(context.Background(), gaiaPairTimeout)
		defer cancel()

		err := c.DoGaiaPairing(ctx, func(emoji string) {
			d.log.Info().Str("emoji", emoji).Msg("Gaia pairing emoji")
			d.mu.Lock()
			d.status.Emoji = emoji
			st := d.status
			d.mu.Unlock()
			d.publish(wire.EventStatus, st)
			d.publish(wire.EventEmoji, map[string]string{"emoji": emoji})
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				d.log.Warn().Msg("Gaia pairing timed out waiting for the phone")
				d.setState(wire.StateUnpaired,
					"the phone never confirmed. Open Google Messages on the phone "+
						"(Settings > Device pairing) and keep it on screen, then pair again.")
				return
			}
			d.log.Error().Err(err).Msg("Gaia pairing failed")
			// Cookies that fail here are usually stale rather than wrong, so
			// say so plainly instead of leaving a bare API error.
			d.setState(wire.StateError, gaiaErrorHint(err))
			return
		}
		// PairSuccessful from DoGaiaPairing drives the rest through
		// handleEvent, which persists the session and reconnects.
		d.saveSession()
	}()

	return nil
}

// validateGaiaCookies reports the specific cookies that are missing, since a
// generic failure here is very hard to act on.
func validateGaiaCookies(cookies map[string]string) error {
	if len(cookies) == 0 {
		return fmt.Errorf("no cookies supplied")
	}
	var missing []string
	for _, name := range wire.RequiredGaiaCookies {
		if strings.TrimSpace(cookies[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required cookie(s): %s — copy them from a signed-in messages.google.com session", strings.Join(missing, ", "))
	}
	return nil
}

func gaiaErrorHint(err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "401"), strings.Contains(low, "403"), strings.Contains(low, "unauthorized"):
		return "Google rejected the cookies (" + msg + "). They expire quickly — re-copy them from a freshly loaded messages.google.com and try again."
	case strings.Contains(low, "no cookies"):
		return "No cookies were supplied. Run: gmessagesd pair --curl <file>"
	default:
		return msg
	}
}
