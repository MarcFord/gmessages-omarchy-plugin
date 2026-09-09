# Contributing

Thanks for taking an interest. Bug reports and patches are both welcome, and so
is telling me something is wrong without a fix attached.

By contributing you agree that your work is licensed under the same terms as
the project — see [LICENSE](LICENSE).

## Before a large change

Open an issue first. Small fixes can go straight to a pull request, but for
anything substantial it is worth agreeing on the approach before you spend the
evening on it.

## Getting set up

You need Go 1.27+, a working Omarchy install with the Quickshell shell, and the
runtime tools listed under **Requirements** in the [README](README.md).

```bash
git clone https://github.com/MarcFord/gmessages-omarchy-plugin
cd gmessages-omarchy-plugin
make build            # build bin/gmessagesd
make test             # go test ./...
make lint             # go vet + a QML syntax check
make install          # install the daemon, plugin and systemd unit
```

Run the daemon by hand against a scratch socket rather than fighting the
installed one:

```bash
./bin/gmessagesd --log-level debug --socket /tmp/gm.sock
printf '{"id":"1","method":"status"}\n' | socat - UNIX-CONNECT:/tmp/gm.sock
```

The wire protocol is defined in [`internal/wire/wire.go`](internal/wire/wire.go).

## Four things that will waste your time otherwise

These are not obvious, and every one of them cost real hours to work out.

### QML changes do not hot-reload. At all.

Omarchy launches Quickshell with `QS_DISABLE_FILE_WATCHER=1` — see
`/usr/share/omarchy/bin/omarchy-launch-shell`, which says so directly:
*"Quickshell's own reloading is off; Omarchy restarts the shell deliberately."*

So after changing any `.qml` file:

```bash
make install-plugin && omarchy-restart-shell
```

`omarchy plugin disable`/`enable` and `omarchy-shell shell rescanPlugins` both
*look* like they work — the log even says `Local plugin changed, reloading` —
but they only re-instantiate objects from QML that is already compiled in
memory. Your edit is not loaded. If a change appears to do nothing, this is
almost certainly why.

### QtWebEngine and QtMultimedia crash the whole desktop

The Omarchy shell process owns the bar, the notifications, the OSD, the polkit
agent **and the lock screen**. A crash there takes all of it down.

- Creating a `WebEngineView` aborts the process: Quickshell never calls
  `QtWebEngineQuick::initialize()`.
- QtMultimedia's FFmpeg backend segfaults the same way.

That is why webcam capture, voice recording and playback all run as child
`ffmpeg`/`ffplay` processes. A crash in a child kills the child. Keep it that
way.

### ids declared inside a `Component` are not in scope outside it

`Panel.qml` puts most of the UI inside `Component { id: clientView }`. A
function at the root of the file cannot see an id declared inside it —
referencing one throws a `ReferenceError` at the point of use, not at load, so
it looks like the feature silently does nothing. It has broken sending once and
the jump-to-unread chip once.

Pass the value as an argument, or emit a signal the component handles. There
are examples of both in `Panel.qml`.

### A Quickshell `Socket` is single-use

Once a connect attempt fails, the object is inert. Assigning `connected` again,
or clearing and restoring `path`, produces no further attempt and not even an
error. Reconnecting means building a new `Socket` — `GmClient.qml` cycles a
`Loader` to do it.

## What CI can and cannot tell you

CI runs `gofmt`, `go vet`, a build, `go test -race`, and a QML syntax check.

That is the ceiling. The interesting behaviour needs a running Quickshell, a
paired phone and a signed-in browser, none of which exist on a runner. **Treat
a green tick as "it compiles and the pure logic holds", not as "it works."**
Anything touching the panel, pairing, or the phone has to be tried by hand, and
the pull request should say what you tried.

## Style

- Go is formatted with `gofmt` and must pass `go vet`.
- Comments should explain *why*, especially where the code looks odd — most of
  the odd-looking code here is working around something in the list above.
- Commit messages: a short imperative subject, then prose explaining the
  reasoning. Look at `git log` for the shape.
- Tests are expected for anything with logic that can be tested without a
  phone: parsing, bounds, path handling, MIME typing.

## Security

Do not open a public issue for a vulnerability. See
[SECURITY.md](SECURITY.md) for how to report privately.

## One thing to know about releases

This plugin is listed in the Omarchy marketplace, and the listing is bound to
an exact commit. Pushing to `main` moves the repository past the verified
snapshot, and the listing shows **Update unverified** until an update
verification is requested and completed. That is expected and reversible — it
just means releases are made deliberately rather than on every merge.
