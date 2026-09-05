package daemon

import (
	"fmt"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/browser"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// ListProfiles reports every browser profile that could supply Google cookies,
// with enough detail for the user to tell why one will not work.
//
// Automatic selection guesses the most recently used profile with a complete
// cookie set, which is right for a single-profile machine and wrong the moment
// someone keeps work and personal accounts apart.
func (d *Daemon) ListProfiles() []wire.BrowserProfile {
	selected := d.config.Get().BrowserProfile

	var out []wire.BrowserProfile
	for _, p := range browser.DiscoverProfiles() {
		entry := wire.BrowserProfile{
			Name:     p.Name,
			Browser:  p.BrowserName,
			Selected: p.Name == selected,
		}

		cookies, err := browser.ExtractGoogleCookies(p)
		switch {
		case err != nil:
			entry.Reason = err.Error()
		default:
			entry.Cookies = len(cookies)
			entry.Missing = wire.MissingGaiaCookies(cookies)
			entry.Usable = len(entry.Missing) == 0
			if !entry.Usable {
				if len(entry.Missing) == 1 && entry.Missing[0] == "OSID" {
					entry.Reason = "Signed in to Google, but not to Messages. Open messages.google.com/web in this profile."
				} else {
					entry.Reason = fmt.Sprintf("Missing %v", entry.Missing)
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// SetProfile pins the profile cookies are read from. An empty name restores
// automatic selection.
func (d *Daemon) SetProfile(name string) error {
	if name != "" {
		found := false
		for _, p := range browser.DiscoverProfiles() {
			if p.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no browser profile named %q", name)
		}
	}
	if err := d.config.SetBrowserProfile(name); err != nil {
		return fmt.Errorf("save profile choice: %w", err)
	}
	d.log.Info().Str("profile", name).Msg("Browser profile selection changed")

	// Adopt the new profile's cookies straight away rather than waiting for
	// the next sync tick.
	d.syncCookiesFromBrowser()
	return nil
}

// candidateProfiles returns the profiles to try, honouring an explicit choice.
// A pinned profile that has since disappeared falls back to automatic rather
// than leaving the daemon with nothing to read.
func (d *Daemon) candidateProfiles() []browser.Profile {
	all := browser.DiscoverProfiles()
	selected := d.config.Get().BrowserProfile
	if selected == "" {
		return all
	}
	for _, p := range all {
		if p.Name == selected {
			return []browser.Profile{p}
		}
	}
	d.log.Warn().Str("profile", selected).Msg("Selected browser profile not found; falling back to automatic")
	return all
}
