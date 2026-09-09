package daemon

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A group avatar URL comes from conversation data, so whoever controls the
// group controls where this daemon is asked to connect.
func TestIsPublicIPRejectsInternalTargets(t *testing.T) {
	refuse := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.5", "192.168.1.10", "172.16.0.1", // private
		"169.254.169.254", // link-local: cloud metadata
		"100.64.0.1",      // carrier-grade NAT
		"0.0.0.0", "::",   // unspecified
		"224.0.0.1", "ff02::1", // multicast
		"fd00::1", // unique local
	}
	for _, s := range refuse {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q is not an IP", s)
		}
		if isPublicIP(ip) {
			t.Errorf("%s must not be treated as public", s)
		}
	}

	allow := []string{"8.8.8.8", "1.1.1.1", "142.250.72.14", "2001:4860:4860::8888"}
	for _, s := range allow {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be reachable", s)
		}
	}
}

func TestFetchGroupAvatarRejectsNonHTTPS(t *testing.T) {
	d := &Daemon{}
	for _, raw := range []string{
		"http://example.com/a.png",
		"file:///etc/passwd",
		"ftp://example.com/a.png",
		"://nonsense",
	} {
		if _, err := d.fetchGroupAvatar(t.Context(), raw); err == nil {
			t.Errorf("%q should have been refused", raw)
		}
	}
}

// The guard has to hold at connect time, not just on the URL string: a
// hostname that resolves to a loopback address is the case a name-based check
// would miss.
func TestAvatarClientRefusesLoopbackConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:PORT — exactly what a hostile avatar URL
	// would resolve to when aimed at a local service.
	resp, err := avatarHTTP.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a connection to loopback must be refused")
	}
	if !strings.Contains(err.Error(), errAvatarRefused.Error()) {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// A public address must still be dialable, or avatars simply stop working.
func TestAvatarClientAllowsPublicAddress(t *testing.T) {
	if err := avatarDialAllowed("8.8.8.8:443"); err != nil {
		t.Errorf("a public address should be allowed: %v", err)
	}
	if err := avatarDialAllowed("127.0.0.1:8080"); err == nil {
		t.Error("loopback should be refused")
	}
}
