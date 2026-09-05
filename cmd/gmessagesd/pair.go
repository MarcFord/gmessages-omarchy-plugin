package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/browser"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/store"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

const pairUsage = `Usage: gmessagesd pair [--from-browser | --curl <file>]

Pairs this machine with your Google account.

Newer Google Messages builds have removed the QR scanner; the phone only
offers "sign in to the same Google Account". That flow needs the cookies from
a signed-in browser session. Most of them are HttpOnly, so they cannot be read
by page script — they have to be copied off a real request:

The easy way — read them straight out of your browser profile:

       gmessagesd pair --from-browser

  You must have loaded https://messages.google.com/web and signed in at least
  once in that browser: the OSID cookie is issued by messages.google.com
  itself, and is absent until you do. Your login keyring must be unlocked.

The manual way, if the above cannot reach your profile:

  1. Open https://messages.google.com/web in your browser and sign in.
  2. Open devtools (F12) and select the Network tab.
  3. Reload, right-click any request to messages.google.com,
     and choose "Copy" -> "Copy as cURL".
  4. Paste it here:

       gmessagesd pair            (paste, then press Ctrl-D)

     or from a file:

       gmessagesd pair --curl saved-curl.txt

Your phone will then show an emoji to confirm. It must match the one printed
here. The cookies are used once to pair and are stored with your session.
`

// runPair reads a cURL command (or JSON, or a bare Cookie header), extracts
// the Google cookies, and hands them to the running daemon.
func runPair(args []string) error {
	var input string

	switch {
	case len(args) > 0 && args[0] == "--from-browser":
		cookies, err := cookiesFromBrowser()
		if err != nil {
			return err
		}
		return submitCookies(cookies)

	case len(args) == 0:
		if term := isTerminal(os.Stdin); term {
			fmt.Fprint(os.Stderr, "Paste the 'Copy as cURL' command, then press Ctrl-D:\n\n")
		}
		data, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		input = string(data)

	case args[0] == "--curl" && len(args) == 2:
		data, err := os.ReadFile(args[1])
		if err != nil {
			return fmt.Errorf("read %s: %w", args[1], err)
		}
		input = string(data)

	case args[0] == "-h", args[0] == "--help":
		fmt.Print(pairUsage)
		return nil

	default:
		fmt.Print(pairUsage)
		return fmt.Errorf("unrecognised arguments")
	}

	cookies, err := wire.ParseCookies(input)
	if err != nil {
		return err
	}
	return submitCookies(cookies)
}

// cookiesFromBrowser tries each installed browser profile in turn, reporting
// precisely what was found so a partial result is actionable.
func cookiesFromBrowser() (map[string]string, error) {
	profiles := browser.DiscoverProfiles()
	if len(profiles) == 0 {
		return nil, browser.ErrNoProfile
	}

	var best map[string]string
	var bestName string
	for _, p := range profiles {
		cookies, err := browser.ExtractGoogleCookies(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %-9s %v\n", p.Name+":", err)
			continue
		}
		missing := wire.MissingGaiaCookies(cookies)
		fmt.Fprintf(os.Stderr, "  %-9s found %d cookies", p.Name+":", len(cookies))
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, ", missing %s", strings.Join(missing, ", "))
		}
		fmt.Fprintln(os.Stderr)

		if len(missing) == 0 {
			return cookies, nil
		}
		if len(cookies) > len(best) {
			best, bestName = cookies, p.Name
		}
	}

	if best == nil {
		return nil, fmt.Errorf("could not read cookies from any browser profile")
	}
	missing := wire.MissingGaiaCookies(best)
	hint := ""
	if len(missing) == 1 && missing[0] == "OSID" {
		hint = "\n\nOSID is issued by messages.google.com itself. Open" +
			"\n  https://messages.google.com/web\nin " + bestName +
			", sign in, let it load, then run this again."
	}
	return nil, fmt.Errorf("missing required cookie(s): %s%s", strings.Join(missing, ", "), hint)
}

// submitCookies hands a validated cookie set to the running daemon and follows
// the pairing exchange to completion.
func submitCookies(cookies map[string]string) error {
	if missing := wire.MissingGaiaCookies(cookies); len(missing) > 0 {
		return fmt.Errorf("missing required cookie(s): %s\n\nMake sure you copied a request to messages.google.com while signed in",
			strings.Join(missing, ", "))
	}
	fmt.Fprintf(os.Stderr, "Found %d cookies, all required ones present.\n", len(cookies))

	paths, err := store.NewPaths()
	if err != nil {
		return err
	}
	conn, err := net.Dial("unix", paths.SocketPath())
	if err != nil {
		return fmt.Errorf("connect to gmessagesd (is it running?): %w", err)
	}
	defer conn.Close()

	req := wire.Request{
		ID:     "pair",
		Method: wire.MethodGaiaPairing,
		Params: wire.GaiaPairingParams{Cookies: cookies},
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send pairing request: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Pairing started. Watch your phone for the confirmation prompt.")
	fmt.Fprintln(os.Stderr)

	// Follow the daemon's events until pairing resolves either way.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 8192), 1<<20)
	for scanner.Scan() {
		var frame struct {
			ID    string `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
			Event string `json:"event"`
			Data  json.RawMessage
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			continue
		}

		if frame.ID == "pair" && !frame.OK {
			return fmt.Errorf("%s", frame.Error)
		}

		switch frame.Event {
		case wire.EventEmoji:
			var d struct {
				Emoji string `json:"emoji"`
			}
			_ = json.Unmarshal(frame.Data, &d)
			fmt.Printf("\n  Confirm this emoji on your phone:  %s\n\n", d.Emoji)

		case wire.EventStatus:
			var st wire.Status
			_ = json.Unmarshal(frame.Data, &st)
			switch st.State {
			case wire.StateConnected:
				fmt.Fprintln(os.Stderr, "Paired and connected.")
				return nil
			case wire.StateError:
				return fmt.Errorf("%s", st.Error)
			}
		}
	}
	return fmt.Errorf("daemon closed the connection before pairing completed")
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
