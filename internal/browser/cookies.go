// Package browser extracts Google session cookies from a local Chromium-family
// browser profile.
//
// The cookies Gaia pairing needs (SID, HSID, OSID, SSID, APISID, SAPISID) are
// HttpOnly, so no page script can read them. The alternative to this is asking
// the user to copy a cURL command out of devtools every time the cookies
// expire, which is a poor thing to require repeatedly.
package browser

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

// Profile is one browser installation we know how to read.
type Profile struct {
	Name        string // display name, e.g. "Chrome / Profile 1"
	BrowserName string // e.g. "Chrome"
	CookieDB    string // path to the Cookies sqlite file
	KeyringApp  string // "application" attribute used by secret-tool
	FallbackPwd string // used when the value is v10 (no keyring)
}

// wantedHosts maps each required cookie to the host that issues it. OSID is
// per-service: the one that matters is issued by messages.google.com itself,
// and is only set once you have actually loaded the Messages web app.
var wantedHosts = map[string]string{
	"SID":              ".google.com",
	"HSID":             ".google.com",
	"SSID":             ".google.com",
	"APISID":           ".google.com",
	"SAPISID":          ".google.com",
	"__Secure-1PSIDTS": ".google.com",
	"OSID":             "messages.google.com",
}

// ErrNoProfile means no supported browser profile was found on disk.
var ErrNoProfile = errors.New("no Chrome, Chromium, or Brave profile found")

// DiscoverProfiles lists every browser profile on this machine that has a
// cookie database.
//
// Enumerating profiles matters: Chrome only calls the first one "Default", and
// additional signed-in accounts land in "Profile 1", "Profile 2", and so on.
// Looking only at Default silently misses the account the user actually uses.
func DiscoverProfiles() []Profile {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	browsers := []struct {
		name       string
		root       string
		keyringApp string
	}{
		{"Chrome", ".config/google-chrome", "chrome"},
		{"Chromium", ".config/chromium", "chromium"},
		{"Brave", ".config/BraveSoftware/Brave-Browser", "brave"},
	}

	var found []Profile
	for _, b := range browsers {
		root := filepath.Join(home, b.root)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			// Chrome scatters non-profile directories through its config root,
			// so identify a profile by the presence of a cookie database
			// rather than by directory name.
			db := filepath.Join(root, entry.Name(), "Cookies")
			st, err := os.Stat(db)
			if err != nil || st.Size() == 0 {
				continue
			}
			found = append(found, Profile{
				Name:        b.name + " / " + entry.Name(),
				BrowserName: b.name,
				CookieDB:    db,
				KeyringApp:  b.keyringApp,
				FallbackPwd: "peanuts",
			})
		}
	}

	// Most-recently-written first: that is the profile in active use, and the
	// one most likely to hold a current Messages session.
	sort.Slice(found, func(i, j int) bool {
		return modTime(found[i].CookieDB).After(modTime(found[j].CookieDB))
	})
	return found
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// decryptionKey derives the AES key Chromium uses for cookie values. v11 values
// are encrypted with a key from the login keyring; v10 with a fixed fallback.
func decryptionKey(p Profile) (v11, v10 []byte) {
	pw, err := exec.Command("secret-tool", "lookup", "application", p.KeyringApp).Output()
	if err == nil && len(pw) > 0 {
		v11 = pbkdf2.Key(pw, []byte("saltysalt"), 1, 16, sha1.New)
	}
	v10 = pbkdf2.Key([]byte(p.FallbackPwd), []byte("saltysalt"), 1, 16, sha1.New)
	return v11, v10
}

func decryptValue(blob, keyV11, keyV10 []byte) (string, error) {
	if len(blob) < 4 {
		return "", errors.New("value too short")
	}
	var key []byte
	switch string(blob[:3]) {
	case "v11":
		key = keyV11
	case "v10":
		key = keyV10
	default:
		// Unencrypted (older profiles store the value in plaintext).
		return string(blob), nil
	}
	if key == nil {
		return "", errors.New("no decryption key available (is the login keyring unlocked?)")
	}

	ct := blob[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("ciphertext is not block-aligned")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, []byte("                ")).CryptBlocks(out, ct)

	// Strip PKCS#7 padding.
	pad := int(out[len(out)-1])
	if pad <= 0 || pad > aes.BlockSize || pad > len(out) {
		return "", errors.New("bad padding (wrong key?)")
	}
	out = out[:len(out)-pad]

	// Recent Chromium prepends a 32-byte SHA-256 of the domain to the
	// plaintext. Detect it by checking whether the tail is printable.
	if len(out) > 32 && isPrintable(out[32:]) && !isPrintable(out[:32]) {
		out = out[32:]
	}
	if !isPrintable(out) {
		return "", errors.New("decrypted value is not printable (wrong key?)")
	}
	return string(out), nil
}

func isPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// ExtractGoogleCookies reads and decrypts the Google cookies from a profile.
// It returns what it found; the caller decides whether that is enough.
func ExtractGoogleCookies(p Profile) (map[string]string, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 is required to read the browser cookie database: %w", err)
	}

	// The browser holds a lock on the live database, so work from a copy.
	tmp, err := os.CreateTemp("", "gm-cookies-*.sqlite")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	data, err := os.ReadFile(p.CookieDB)
	if err != nil {
		return nil, fmt.Errorf("read cookie database: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return nil, err
	}

	// hex() keeps the binary blob intact across the CLI boundary.
	const query = `SELECT name || '|' || host_key || '|' || hex(encrypted_value) FROM cookies WHERE host_key LIKE '%google.com';`
	out, err := exec.Command("sqlite3", "-readonly", tmpPath, query).Output()
	if err != nil {
		return nil, fmt.Errorf("query cookie database: %w", err)
	}

	keyV11, keyV10 := decryptionKey(p)
	cookies := make(map[string]string)

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		name, host, hexVal := parts[0], parts[1], parts[2]

		wantHost, ok := wantedHosts[name]
		if !ok || host != wantHost {
			continue
		}
		blob, err := hex.DecodeString(hexVal)
		if err != nil {
			continue
		}
		value, err := decryptValue(blob, keyV11, keyV10)
		if err != nil || value == "" {
			continue
		}
		cookies[name] = value
	}

	if len(cookies) == 0 {
		return nil, errors.New("no Google cookies could be read (is the login keyring unlocked, and are you signed in?)")
	}
	return cookies, nil
}
