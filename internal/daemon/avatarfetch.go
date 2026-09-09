package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// maxAvatarBytes caps one avatar. They are small thumbnails; this is generous
// and exists only so the size is ours to decide rather than the sender's.
const maxAvatarBytes = 4 << 20

var errAvatarRefused = errors.New("refusing to fetch avatar")

// avatarHTTP refuses to connect to anything that is not a public address.
//
// A group's avatar URL arrives in conversation data, which means whoever
// controls the group controls the URL this daemon is asked to fetch. Without
// this, that is a request generator pointed at the machine's own network:
// localhost services, link-local metadata endpoints, the LAN.
//
// The check is in Control, so it sees the address actually being connected to
// after DNS resolution. Validating the hostname instead would leave a name
// that resolves to 127.0.0.1 -- or re-resolves between check and dial -- to
// slip straight through.
var avatarHTTP = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				return avatarDialAllowed(address)
			},
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
}

// isPublicIP reports whether ip is routable on the public internet.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10, carrier-grade NAT, which IsPrivate does not cover.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

// fetchGroupAvatar downloads a group avatar with the size bounded and the
// destination constrained, rather than through libgm's DownloadAvatar, which
// reads the whole body with io.ReadAll and follows whatever URL it is given.
func (d *Daemon) fetchGroupAvatar(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: unparseable URL", errAvatarRefused)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: %q is not https", errAvatarRefused, parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/*")

	res, err := avatarHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("avatar download: HTTP %d", res.StatusCode)
	}
	return readBoundedTo(maxAvatarBytes, res.ContentLength, res.Body)
}

// avatarDialAllowed is the connect-time check, split out so it can be tested
// without opening a socket.
func avatarDialAllowed(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", errAvatarRefused, address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an IP", errAvatarRefused, host)
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("%w: %s is not a public address", errAvatarRefused, ip)
	}
	return nil
}
