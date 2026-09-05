package daemon

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// PickImage opens the desktop's own file chooser through xdg-desktop-portal
// and returns the chosen path.
//
// The portal is used rather than a bundled dialog because Quickshell has no
// file chooser of its own, and the portal is what actually works under
// Wayland: it runs out of process, honours the user's real file manager, and
// needs no extra package beyond what a desktop already has.
func (d *Daemon) PickImage(ctx context.Context) (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", fmt.Errorf("connect session bus: %w", err)
	}
	defer conn.Close()

	// The portal replies asynchronously on a Response signal, so subscribe
	// before making the call to avoid losing a fast reply.
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return "", fmt.Errorf("subscribe to portal response: %w", err)
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)

	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")

	options := map[string]dbus.Variant{
		"modal":        dbus.MakeVariant(true),
		"multiple":     dbus.MakeVariant(false),
		"handle_token": dbus.MakeVariant(fmt.Sprintf("gmessages%d", time.Now().UnixNano())),
		"accept_label": dbus.MakeVariant("Attach"),
		// filters: [(name, [(type, pattern)])] where type 1 is a MIME type.
		"filters": dbus.MakeVariant([]struct {
			Name    string
			Filters []struct {
				Type uint32
				Text string
			}
		}{
			{
				Name: "Images",
				Filters: []struct {
					Type uint32
					Text string
				}{
					{1, "image/png"}, {1, "image/jpeg"}, {1, "image/gif"}, {1, "image/webp"},
				},
			},
		}),
	}

	var handle dbus.ObjectPath
	err = obj.CallWithContext(ctx, "org.freedesktop.portal.FileChooser.OpenFile", 0,
		"", "Attach an image", options).Store(&handle)
	if err != nil {
		return "", fmt.Errorf("open file chooser: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case sig := <-signals:
			if sig.Path != handle || len(sig.Body) < 2 {
				continue
			}
			code, _ := sig.Body[0].(uint32)
			if code != 0 {
				// 1 is user cancellation, which is not an error worth shouting about.
				return "", nil
			}
			results, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				return "", fmt.Errorf("unexpected portal response")
			}
			uris, ok := results["uris"].Value().([]string)
			if !ok || len(uris) == 0 {
				return "", nil
			}
			return uriToPath(uris[0])
		}
	}
}

func uriToPath(uri string) (string, error) {
	if !strings.HasPrefix(uri, "file://") {
		return "", fmt.Errorf("unsupported location %q", uri)
	}
	path, err := url.PathUnescape(strings.TrimPrefix(uri, "file://"))
	if err != nil {
		return "", fmt.Errorf("decode path: %w", err)
	}
	return path, nil
}
