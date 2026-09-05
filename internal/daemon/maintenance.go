package daemon

import (
	"context"
	"sort"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/browser"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// Why this exists.
//
// Two credentials keep a paired session alive, and only one looks after itself:
//
//   - The tachyon auth token refreshes on its own. libgm signs a refresh with
//     the stored ECDSA key roughly an hour before expiry, needing no cookies,
//     so the token is not what goes stale.
//
//   - The Google cookies do NOT look after themselves. Every request is
//     authenticated with a SAPISIDHASH derived from them, and Google rotates
//     __Secure-1PSIDTS continuously as the browser is used. A snapshot taken at
//     pairing time drifts from the browser's copy until Google decides the
//     session is bogus and returns SESSION_COOKIE_INVALID — which no amount of
//     retrying fixes, because it needs a whole new pairing.
//
// So: re-sync cookies from the browser on a schedule rather than waiting for a
// failure, and persist the session so a restart keeps the refreshed values.

const (
	// cookieSyncInterval is well inside Google's rotation window, and cheap:
	// reading the cookie database costs a few milliseconds.
	cookieSyncInterval = 15 * time.Minute

	// sessionSaveInterval captures the token and cookie updates libgm applies
	// in place, so an unclean shutdown loses at most this much.
	sessionSaveInterval = 10 * time.Minute
)

// runMaintenance keeps credentials fresh for as long as the daemon runs.
func (d *Daemon) runMaintenance(ctx context.Context) {
	cookieTicker := time.NewTicker(cookieSyncInterval)
	defer cookieTicker.Stop()
	saveTicker := time.NewTicker(sessionSaveInterval)
	defer saveTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-cookieTicker.C:
			d.syncCookiesFromBrowser()
		case <-saveTicker.C:
			d.saveSession()
		}
	}
}

// syncCookiesFromBrowser adopts the browser's current Google cookies when they
// differ from the ones in use. The browser is the authority here: the user is
// signed in there, and it is what Google keeps rotating.
func (d *Daemon) syncCookiesFromBrowser() {
	d.mu.RLock()
	auth := d.auth
	paired := d.paired
	d.mu.RUnlock()
	if auth == nil || !paired {
		return
	}

	for _, p := range d.candidateProfiles() {
		fresh, err := browser.ExtractGoogleCookies(p)
		if err != nil || len(wire.MissingGaiaCookies(fresh)) > 0 {
			continue
		}

		changed := changedCookies(auth, fresh)
		if len(changed) == 0 {
			return // already in step; nothing to do
		}

		auth.SetCookies(fresh)
		d.saveSession()
		d.log.Info().
			Str("profile", p.Name).
			Strs("updated", changed).
			Msg("Re-synced Google cookies from browser")
		return
	}
}

// changedCookies names the cookies whose values differ. It returns names only:
// the values are live credentials and must never reach a log.
func changedCookies(auth *libgm.AuthData, fresh map[string]string) []string {
	auth.CookiesLock.RLock()
	defer auth.CookiesLock.RUnlock()

	var changed []string
	for name, value := range fresh {
		if auth.Cookies[name] != value {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}
