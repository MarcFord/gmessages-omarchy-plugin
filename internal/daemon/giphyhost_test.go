package daemon

import "testing"

// The URL being fetched comes from a search response, so the allowlist is the
// only thing keeping this pointed at Giphy. A plain suffix check on
// "giphy.com" also accepts evilgiphy.com.
func TestIsGiphyHost(t *testing.T) {
	allow := []string{"giphy.com", "media.giphy.com", "i.giphy.com", "MEDIA.GIPHY.COM", "giphy.com."}
	for _, h := range allow {
		if !isGiphyHost(h) {
			t.Errorf("%q should be allowed", h)
		}
	}
	refuse := []string{
		"evilgiphy.com", "not-giphy.com", "giphy.com.attacker.net",
		"giphy.co", "notgiphy.com", "", "attacker.net",
	}
	for _, h := range refuse {
		if isGiphyHost(h) {
			t.Errorf("%q must be refused", h)
		}
	}
}
