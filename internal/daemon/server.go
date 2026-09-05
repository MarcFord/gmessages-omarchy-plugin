package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/wire"
)

// maxFrame bounds a single request line so a runaway client cannot exhaust
// memory. Requests are small; replies can be large.
const maxFrame = 1 << 20

// Serve accepts plugin connections on the Unix socket until ctx is cancelled.
func (d *Daemon) Serve(ctx context.Context, socketPath string) error {
	// A stale socket from an unclean shutdown would block Listen.
	if err := removeStaleSocket(socketPath); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	// The socket carries message content; keep it owner-only.
	if err := os.Chmod(socketPath, 0o600); err != nil {
		d.log.Warn().Err(err).Msg("Could not restrict socket permissions")
	}
	defer func() {
		ln.Close()
		os.Remove(socketPath)
	}()

	d.log.Info().Str("socket", socketPath).Msg("Listening")

	// Live connections have to be closed explicitly on shutdown. The bar keeps
	// a socket open per monitor and those goroutines sit blocked in Scan();
	// closing only the listener leaves them there, wg.Wait() never returns,
	// and systemd ends up SIGKILLing the daemon after its stop timeout.
	var (
		connMu sync.Mutex
		conns  = make(map[net.Conn]struct{})
	)
	closeAllConns := func() {
		connMu.Lock()
		for c := range conns {
			c.Close()
		}
		clear(conns)
		connMu.Unlock()
	}

	go func() {
		<-ctx.Done()
		ln.Close()
		closeAllConns()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		connMu.Lock()
		conns[conn] = struct{}{}
		connMu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				connMu.Lock()
				delete(conns, conn)
				connMu.Unlock()
			}()
			d.handleConn(ctx, conn)
		}()
	}
}

// removeStaleSocket deletes a socket file left behind by a crashed daemon,
// but refuses to touch one a live daemon is still listening on.
func removeStaleSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("another gmessagesd is already listening on %s", path)
	}
	return os.Remove(path)
}

// connWriter serialises writes from the request path and the event pump.
type connWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *connWriter) send(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(v)
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := &connWriter{enc: json.NewEncoder(conn)}

	events, unsubscribe := d.Subscribe()
	defer unsubscribe()

	// Push current status immediately so a freshly-connected plugin renders
	// without having to ask.
	_ = w.send(wire.Event{Event: wire.EventStatus, Data: d.Status()})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-events:
				if !ok {
					return
				}
				if err := w.send(evt); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 8192), maxFrame)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req wire.Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = w.send(wire.Response{OK: false, Error: "malformed request: " + err.Error()})
			continue
		}
		// Each request gets its own goroutine so a slow fetch does not block
		// the rest of the UI's calls on the same connection.
		go func(req wire.Request) {
			resp := d.dispatch(ctx, req)
			if err := w.send(resp); err != nil {
				cancel()
			}
		}(req)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		d.log.Debug().Err(err).Msg("Connection read ended")
	}
}

// decodeParams re-marshals the loosely-typed params into a concrete struct.
func decodeParams[T any](raw any) (T, error) {
	var out T
	if raw == nil {
		return out, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func (d *Daemon) dispatch(ctx context.Context, req wire.Request) wire.Response {
	fail := func(err error) wire.Response {
		return wire.Response{ID: req.ID, OK: false, Error: err.Error()}
	}
	ok := func(result any) wire.Response {
		return wire.Response{ID: req.ID, OK: true, Result: result}
	}

	// Network calls must not hang the UI forever.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	switch req.Method {
	case wire.MethodStatus:
		return ok(d.Status())

	case wire.MethodConversations:
		p, err := decodeParams[wire.ConversationsParams](req.Params)
		if err != nil {
			return fail(err)
		}
		return ok(d.Conversations(p.Count))

	case wire.MethodMessages:
		p, err := decodeParams[wire.MessagesParams](req.Params)
		if err != nil {
			return fail(err)
		}
		res, err := d.Messages(ctx, p)
		if err != nil {
			return fail(err)
		}
		return ok(res)

	case wire.MethodSend:
		p, err := decodeParams[wire.SendParams](req.Params)
		if err != nil {
			return fail(err)
		}
		msg, err := d.Send(ctx, p)
		if err != nil {
			return fail(err)
		}
		return ok(msg)

	case wire.MethodMarkRead:
		p, err := decodeParams[wire.MarkReadParams](req.Params)
		if err != nil {
			return fail(err)
		}
		if err := d.MarkRead(ctx, p); err != nil {
			return fail(err)
		}
		return ok(nil)

	case wire.MethodMedia:
		p, err := decodeParams[wire.MediaParams](req.Params)
		if err != nil {
			return fail(err)
		}
		res, err := d.Media(ctx, p)
		if err != nil {
			return fail(err)
		}
		return ok(res)

	case wire.MethodSetTyping:
		p, err := decodeParams[wire.SetTypingParams](req.Params)
		if err != nil {
			return fail(err)
		}
		if err := d.SetTyping(ctx, p); err != nil {
			return fail(err)
		}
		return ok(nil)

	case wire.MethodRefresh:
		if err := d.Refresh(ctx); err != nil {
			return fail(err)
		}
		return ok(nil)

	case wire.MethodStartPairing:
		qr, err := d.StartPairing()
		if err != nil {
			return fail(err)
		}
		return ok(map[string]string{"url": qr})

	case wire.MethodGaiaPairing:
		p, err := decodeParams[wire.GaiaPairingParams](req.Params)
		if err != nil {
			return fail(err)
		}
		if err := d.StartGaiaPairing(p.Cookies); err != nil {
			return fail(err)
		}
		return ok(nil)

	case wire.MethodPairFromBrowser:
		if err := d.PairFromBrowser(); err != nil {
			return fail(err)
		}
		return ok(nil)

	case wire.MethodSendMedia:
		p, err := decodeParams[wire.SendMediaParams](req.Params)
		if err != nil {
			return fail(err)
		}
		msg, err := d.SendMedia(ctx, p)
		if err != nil {
			return fail(err)
		}
		return ok(msg)

	case wire.MethodPickImage:
		path, err := d.PickImage(ctx)
		if err != nil {
			return fail(err)
		}
		return ok(wire.PickImageResult{Path: path})

	case wire.MethodListProfiles:
		return ok(d.ListProfiles())

	case wire.MethodSetProfile:
		p, err := decodeParams[wire.SetProfileParams](req.Params)
		if err != nil {
			return fail(err)
		}
		if err := d.SetProfile(p.Name); err != nil {
			return fail(err)
		}
		return ok(d.ListProfiles())

	case wire.MethodUnpair:
		if err := d.Unpair(ctx); err != nil {
			return fail(err)
		}
		return ok(nil)

	default:
		return fail(fmt.Errorf("unknown method %q", req.Method))
	}
}
