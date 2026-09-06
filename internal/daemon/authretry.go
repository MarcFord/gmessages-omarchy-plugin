package daemon

import (
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/browser"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// Google rotates session cookies (notably __Secure-1PSIDTS) as the user
// browses, so the copy captured at pairing time goes stale on its own. Every
// authenticated call then fails with HTTP 401 and the UI fills with
// "invalid authentication credentials".
//
// Rather than make the user re-pair, re-read the cookies from the browser
// profile and retry once. The browser is already keeping them fresh.

// cookieRefreshInterval throttles re-reads so a burst of failing calls cannot
// hammer the cookie database.
const cookieRefreshInterval = 30 * time.Second

// repairInterval throttles automatic re-pairing. Re-pairing is cheap and
// silent once the account trusts this device, but it must never become a loop.
const repairInterval = 5 * time.Minute

type cookieRefresher struct {
	mu         sync.Mutex
	last       time.Time
	lastRepair time.Time
}

// isSessionInvalid marks the failure Google returns once it has torn down the
// web session entirely. Fresh cookies do not fix this: the auth token is bound
// to the dead session, so the only way back is to pair again.
// changedCookies names the cookies whose values differ. Names only: the values
// are live credentials and must never reach a log.
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

func isSessionInvalid(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SESSION_COOKIE_INVALID")
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "invalid authentication") ||
		strings.Contains(msg, "unauthenticated") ||
		strings.Contains(msg, "oauth 2 access token")
}

// refreshBrowserCookies re-reads Google cookies from the browser and installs
// them on the live client. Returns true when something was actually updated.
func (d *Daemon) refreshBrowserCookies() bool {
	d.cookies.mu.Lock()
	if time.Since(d.cookies.last) < cookieRefreshInterval {
		d.cookies.mu.Unlock()
		return false
	}
	d.cookies.last = time.Now()
	d.cookies.mu.Unlock()

	for _, p := range d.candidateProfiles() {
		cookies, err := browser.ExtractGoogleCookies(p)
		if err != nil || len(wire.MissingGaiaCookies(cookies)) > 0 {
			continue
		}
		d.mu.RLock()
		auth := d.auth
		d.mu.RUnlock()
		if auth == nil {
			return false
		}
		changed := changedCookies(auth, cookies)
		auth.SetCookies(cookies)
		d.saveSession()
		d.log.Info().
			Str("profile", p.Name).
			Strs("updated", changed).
			Msg("Refreshed Google cookies from browser")
		return true
	}

	d.log.Warn().Msg("Could not refresh cookies from any browser profile")
	d.setState(wire.StateError,
		"Google sign-in expired. Open messages.google.com/web in your browser to refresh it.")
	return false
}

// repairSession re-runs Gaia pairing to replace a session Google has
// invalidated. Once the account already trusts this device the phone does not
// prompt again, so this is usually invisible.
func (d *Daemon) repairSession() bool {
	d.cookies.mu.Lock()
	if time.Since(d.cookies.lastRepair) < repairInterval {
		d.cookies.mu.Unlock()
		return false
	}
	d.cookies.lastRepair = time.Now()
	d.cookies.mu.Unlock()

	d.log.Warn().Msg("Session invalidated by Google; re-pairing automatically")

	d.mu.RLock()
	before := d.pairGeneration
	d.mu.RUnlock()

	if err := d.PairFromBrowser(); err != nil {
		d.log.Error().Err(err).Msg("Automatic re-pair failed")
		return false
	}

	// Wait for the pairing itself to complete, not merely for the connection
	// to look healthy: the long poll recovering on its own would otherwise be
	// mistaken for success while the phone is still being asked to confirm.
	for i := 0; i < 90; i++ {
		time.Sleep(time.Second)

		d.mu.RLock()
		now := d.pairGeneration
		state := d.status.State
		d.mu.RUnlock()

		if now != before {
			d.log.Info().Msg("Re-paired successfully")
			return true
		}
		// Pairing gave up or was rejected; stop waiting.
		if state == wire.StateError || state == wire.StateUnpaired {
			d.log.Warn().Str("state", string(state)).Msg("Re-pair did not complete")
			return false
		}
	}
	d.log.Warn().Msg("Re-pair timed out")
	return false
}

// withAuthRetry runs op and, on an authentication failure, tries to restore
// access before giving up: fresh cookies first, then a full re-pair when the
// session itself has been torn down.
func withAuthRetry[T any](d *Daemon, op func() (T, error)) (T, error) {
	result, err := op()
	if !isAuthError(err) {
		return result, err
	}

	// A dead session cannot be revived with cookies, so skip straight to
	// re-pairing rather than burning a round trip.
	if isSessionInvalid(err) {
		if !d.repairSession() {
			return result, err
		}
		return op()
	}

	d.log.Warn().Err(err).Msg("Request failed authentication; refreshing cookies")
	if !d.refreshBrowserCookies() {
		return result, err
	}

	result, err = op()
	if !isAuthError(err) {
		return result, err
	}

	// Fresh cookies were not enough; the session is gone.
	if !d.repairSession() {
		return result, err
	}
	return op()
}
