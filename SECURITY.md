# Security Policy

## Reporting a vulnerability

**Please report privately, not in a public issue.**

Use GitHub's private vulnerability reporting:
[**Report a vulnerability**](https://github.com/MarcFord/gmessages-omarchy-plugin/security/advisories/new).
That opens a private advisory visible only to you and the maintainer.

If you cannot use that form, open a public issue saying only that you have a
security report and how to reach you — no details — and a private channel will
be arranged.

### What to include

Whatever you have. A rough report beats none. Ideally:

- what the flaw is, and which file or function it is in
- how to reach it: what an attacker needs to control, and what they get
- the commit or release you looked at

### What to expect

This is maintained by one person in their spare time, so timelines are
best-effort rather than a promise:

| | Target |
|---|---|
| Acknowledgement | within 3 days |
| Initial assessment | within 7 days |
| Fix or a plan for one | depends on severity, discussed in the advisory |

You will be credited by name or handle in the release notes unless you would
rather not be. If a report turns out to be out of scope, you will get a
straight answer about why rather than silence.

## Supported versions

Only the latest release is supported. Fixes go into a new release rather than
being backported.

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Anything older | No |

## What is in scope

- The daemon (`gmessagesd`) and everything under `internal/`
- The QML plugin (`Panel.qml`, `GmClient.qml`, `Widget.qml`, `Avatar.qml`)
- The systemd unit and the `Makefile` install path
- Anything that lets a message, attachment, or conversation from the network
  influence the daemon beyond displaying it

## What is out of scope

- **`libgm` and the Google Messages protocol.** This plugin is a client of
  [mautrix/gmessages](https://github.com/mautrix/gmessages); report issues in
  the library there. Where a `libgm` limitation affects this plugin, it is
  worked around here and noted in the README.
- **Google's own service**, including anything about how pairing or the
  protocol works.
- **Requiring an attacker who already has your user account.** The daemon's
  socket is `0600`, and the design assumes anyone who can talk to it is you.
  The file picker will upload any path it is handed, by design.
- **Widget settings becoming process arguments.** Changing them requires write
  access to your own config.

These are stated with reasoning under **Security notes** in the
[README](README.md).

## What the plugin already does

The README's **Security notes** and **Resource limits** sections describe the
bounds that are enforced: every read of network data is bounded, URLs that
arrive in data are validated before they are followed, child processes are
never given a shell, secrets stay out of logs and errors, and in-memory state
is capped rather than growing with everything the daemon has ever seen.

## A note on credentials

Pairing reads Google cookies from a local Chromium-family browser profile,
because recent Google Messages builds removed QR pairing and account pairing
authenticates with `HttpOnly` cookies. Those credentials are written to
`~/.local/share/gmessages-omarchy/session.json` with mode `0600`. Cookie values
are never logged, and never appear in an error message.

If you believe your session has been exposed, sign the device out from Google
Messages on your phone (**Device pairing** → remove this device), which
invalidates it, then re-pair.

## Marketplace verification is not a security audit

This plugin is listed in the Omarchy plugin marketplace and carries an
`approved-and-verified` record. That records a maintainer review of a specific
commit against a deterministic baseline. It is not a security audit, and it is
not a warranty.
