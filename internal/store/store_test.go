package store

import (
	"os"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func TestIsPairedRejectsAbandonedPairing(t *testing.T) {
	// Starting a QR pairing mints a tachyon token before any phone accepts.
	// Treating that as paired makes the daemon boot into "not logged in".
	abandoned := &libgm.AuthData{TachyonAuthToken: []byte("tok")}
	if IsPaired(abandoned) {
		t.Error("a token without a browser identity must not count as paired")
	}

	complete := &libgm.AuthData{
		TachyonAuthToken: []byte("tok"),
		Browser:          &gmproto.Device{},
	}
	if !IsPaired(complete) {
		t.Error("token plus browser identity should count as paired")
	}

	if IsPaired(&libgm.AuthData{Browser: &gmproto.Device{}}) {
		t.Error("browser without a token must not count as paired")
	}
	if IsPaired(nil) {
		t.Error("nil auth is not paired")
	}
}

func TestSaveAndLoadSessionRoundTrip(t *testing.T) {
	p := &Paths{Data: t.TempDir(), Cache: t.TempDir(), Runtime: t.TempDir()}

	// A missing file is a normal cold start, not an error.
	auth, paired, err := p.LoadSession()
	if err != nil || paired || auth == nil {
		t.Fatalf("cold start: auth=%v paired=%v err=%v", auth != nil, paired, err)
	}

	auth.TachyonAuthToken = []byte("tok")
	auth.Browser = &gmproto.Device{}
	if err := p.SaveSession(auth); err != nil {
		t.Fatal(err)
	}

	got, paired, err := p.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if !paired {
		t.Error("expected a completed session to load as paired")
	}
	if string(got.TachyonAuthToken) != "tok" {
		t.Errorf("token did not round-trip: %q", got.TachyonAuthToken)
	}
}

func TestConfigStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"

	// A missing file is a normal first run, not an error.
	cs := NewConfigStore(path)
	if cs.Get().BrowserProfile != "" {
		t.Error("expected empty default (automatic selection)")
	}

	if err := cs.SetBrowserProfile("Chrome / Profile 1"); err != nil {
		t.Fatal(err)
	}
	if got := cs.Get().BrowserProfile; got != "Chrome / Profile 1" {
		t.Errorf("in-memory value = %q", got)
	}

	// The choice must survive a daemon restart, or the background cookie sync
	// would silently revert to guessing.
	reopened := NewConfigStore(path)
	if got := reopened.Get().BrowserProfile; got != "Chrome / Profile 1" {
		t.Errorf("persisted value = %q, want the selected profile", got)
	}

	// Clearing restores automatic selection.
	if err := reopened.SetBrowserProfile(""); err != nil {
		t.Fatal(err)
	}
	if NewConfigStore(path).Get().BrowserProfile != "" {
		t.Error("clearing should restore automatic selection")
	}
}

func TestConfigStoreSurvivesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A damaged config must never stop the daemon starting.
	if got := NewConfigStore(path).Get().BrowserProfile; got != "" {
		t.Errorf("corrupt config should fall back to defaults, got %q", got)
	}
}
