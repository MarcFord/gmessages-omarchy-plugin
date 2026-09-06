package daemon

import (
	"context"
	"time"
)

// Why this exists.
//
// Two credentials keep a paired session alive, and only one looks after itself:
//
//   - The tachyon auth token refreshes on its own. libgm signs a refresh with
//     the stored ECDSA key roughly an hour before expiry, needing no cookies.
//
//   - The Google cookies are kept current by libgm itself, which applies the
//     Set-Cookie headers Google returns on every request.
//
// This deliberately does NOT re-sync cookies from the browser on a timer.
// That was tried and it made things worse: __Secure-1PSIDTS is a *rotating*
// token, and the browser and the daemon each rotate their own copy. Copying
// the browser's copy over the daemon's freshly-rotated one hands Google a
// stale token, which it answers with SESSION_COOKIE_INVALID — killing the very
// session the sync was meant to preserve. The observed pattern was a cookie
// sync every 15 minutes followed by an invalidation roughly every 20.
//
// Reading the browser's cookies is therefore reactive only: on an actual
// authentication failure (see authretry.go), where our copy is known bad and
// the browser's is the only other source.

const (
	// sessionSaveInterval captures the token and cookie updates libgm applies
	// in place, so an unclean shutdown loses at most this much.
	sessionSaveInterval = 10 * time.Minute
)

// runMaintenance persists the session periodically so refreshed credentials
// survive a restart.
func (d *Daemon) runMaintenance(ctx context.Context) {
	saveTicker := time.NewTicker(sessionSaveInterval)
	defer saveTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-saveTicker.C:
			d.saveSession()
		}
	}
}
