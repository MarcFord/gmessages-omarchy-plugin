.pragma library

// Formatting helpers for the Google Messages panel. Kept out of QML so the
// panel file stays layout, and so these stay unit-testable by eye.

// Google Messages timestamps are microseconds since the epoch.
function toDate(micros) {
  return new Date(Math.floor((Number(micros) || 0) / 1000))
}

function sameDay(a, b) {
  return a.getFullYear() === b.getFullYear()
    && a.getMonth() === b.getMonth()
    && a.getDate() === b.getDate()
}

// relativeTime renders a conversation-list timestamp the way a messaging app
// does: clock time today, weekday this week, date beyond that.
function relativeTime(micros) {
  var n = Number(micros) || 0
  if (n === 0) return ""
  var d = toDate(n)
  var now = new Date()
  if (sameDay(d, now)) {
    return Qt.formatDateTime(d, "h:mm AP")
  }
  var yesterday = new Date(now.getTime() - 86400000)
  if (sameDay(d, yesterday)) return "Yesterday"
  if (now.getTime() - d.getTime() < 7 * 86400000) return Qt.formatDateTime(d, "ddd")
  if (d.getFullYear() === now.getFullYear()) return Qt.formatDateTime(d, "MMM d")
  return Qt.formatDateTime(d, "MMM d, yyyy")
}

// bubbleTime is the precise timestamp under a message bubble.
function bubbleTime(micros) {
  var n = Number(micros) || 0
  if (n === 0) return ""
  return Qt.formatDateTime(toDate(n), "h:mm AP")
}

// dayLabel heads a group of messages sent on the same day.
function dayLabel(micros) {
  var d = toDate(micros)
  var now = new Date()
  if (sameDay(d, now)) return "Today"
  var yesterday = new Date(now.getTime() - 86400000)
  if (sameDay(d, yesterday)) return "Yesterday"
  if (d.getFullYear() === now.getFullYear()) return Qt.formatDateTime(d, "dddd, MMMM d")
  return Qt.formatDateTime(d, "MMMM d, yyyy")
}

function dayKey(micros) {
  var d = toDate(micros)
  return d.getFullYear() + "-" + d.getMonth() + "-" + d.getDate()
}

// previewText collapses newlines so the conversation list stays single-line.
function previewText(conv) {
  if (!conv) return ""
  var text = (conv.preview || "").replace(/\s+/g, " ").trim()
  if (text === "") text = "No messages"
  return conv.previewMine ? "You: " + text : text
}

function humanSize(bytes) {
  var n = Number(bytes) || 0
  if (n <= 0) return ""
  var units = ["B", "KB", "MB", "GB"]
  var i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return (i === 0 ? n : n.toFixed(1)) + " " + units[i]
}

// avatarColor falls back to a stable hue derived from the name when the
// server sends no colour, so contacts stay visually distinct either way.
function avatarColor(hex, seed, fallback) {
  if (hex && hex.length > 0) {
    return hex.charAt(0) === "#" ? hex : "#" + hex
  }
  var s = String(seed || "")
  var h = 0
  for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) & 0xffffff
  if (s.length === 0) return fallback
  return Qt.hsla((h % 360) / 360, 0.45, 0.5, 1)
}

// statusLine describes the connection for the panel header.
function statusLine(status) {
  if (!status) return "Disconnected"
  switch (status.state) {
  case "connected":   return status.phoneOK === false ? "Phone not responding" : "Connected"
  case "connecting":  return "Connecting…"
  case "pairing":     return "Waiting for pairing"
  case "unpaired":    return "Not paired"
  case "disconnected":return "Reconnecting…"
  case "error":       return status.error ? status.error : "Error"
  }
  return String(status.state || "")
}

// groupMessages inserts day separators into a flat message list. Returns
// entries of {kind:"day"|"msg", ...} for a single ListView to render.
function groupMessages(messages) {
  var out = []
  var lastKey = ""
  for (var i = 0; i < messages.length; i++) {
    var m = messages[i]
    var k = dayKey(m.timestamp)
    if (k !== lastKey) {
      out.push({ kind: "day", key: "day-" + k, label: dayLabel(m.timestamp) })
      lastKey = k
    }
    // Consecutive messages from the same sender are visually grouped, so the
    // avatar and name only draw on the first of a run.
    var prev = i > 0 ? messages[i - 1] : null
    var startsRun = !prev || prev.fromMe !== m.fromMe || prev.senderID !== m.senderID
      || dayKey(prev.timestamp) !== k
    out.push({ kind: "msg", key: m.id || ("m" + i), message: m, startsRun: startsRun })
  }
  return out
}
