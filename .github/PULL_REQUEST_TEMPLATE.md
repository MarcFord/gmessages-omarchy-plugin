## What this changes

<!-- What it does, and why. If it fixes an issue, "Fixes #123". -->

## How it was tested

<!--
CI runs gofmt, vet, build, `go test -race` and a QML syntax check. That is the
ceiling: the interesting behaviour needs a running Quickshell, a paired phone
and a signed-in browser, none of which exist on a runner.

So please say what you actually exercised by hand. "Recorded and sent a voice
note to a real phone and played the reply back" is worth more than a green tick.
-->

- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] Tried by hand:

## Anything worth flagging

<!--
Things a reviewer would want to know: a limit you chose and why, something you
were unsure about, a behaviour change for existing users, a new runtime
dependency.
-->

---

<!--
If this touches QML, remember it does not hot-reload:
`make install-plugin && omarchy-restart-shell`. See CONTRIBUTING.md.
-->
