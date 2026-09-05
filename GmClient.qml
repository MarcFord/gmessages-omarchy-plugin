import QtQuick
import Quickshell
import Quickshell.Io

// Transport to gmessagesd. Holds one Unix socket, correlates request IDs to
// callbacks, and republishes daemon pushes as signals. Everything the panel
// knows about the account comes through here.
Item {
  id: root

  property string socketPath: ""
  // Set from settings; when true a failed connect tries to start the service
  // once before falling back to telling the user.
  property bool autostart: true
  property string serviceName: "gmessagesd.service"

  readonly property bool connected: sock.connected

  // Mirrors wire.Status. Kept as a plain object so bindings see whole-object
  // replacement rather than partial mutation.
  property var status: ({ state: "disconnected", unread: 0, phoneOK: false, qrURL: "", error: "" })
  readonly property string state: status && status.state ? status.state : "disconnected"
  readonly property int unread: status && status.unread ? status.unread : 0

  property var conversations: []

  signal messageReceived(var message)
  signal conversationUpdated(var conversation)
  signal paired()
  signal transportError(string message)

  property int _nextId: 1
  property var _pending: ({})
  property bool _triedAutostart: false

  // ---- public API ----

  // call sends a request; callback(ok, resultOrError) fires when the daemon
  // answers. Calls made while disconnected fail fast rather than queue, so
  // the UI never shows a spinner that cannot resolve.
  function call(method, params, callback) {
    if (!sock.connected) {
      if (callback) callback(false, "not connected to gmessagesd")
      return
    }
    var id = String(_nextId++)
    if (callback) _pending[id] = callback
    var frame = { id: id, method: method }
    if (params !== undefined && params !== null) frame.params = params
    sock.write(JSON.stringify(frame) + "\n")
    sock.flush()
  }

  function refreshConversations() {
    call("conversations", { count: 50 }, function(ok, res) {
      if (ok && res) root.conversations = res
    })
  }

  function reconnect() {
    sock.connected = false
    sock.connected = true
  }

  // ---- transport ----

  Socket {
    id: sock
    path: root.socketPath
    connected: root.socketPath !== ""

    parser: SplitParser {
      splitMarker: "\n"
      onRead: function(line) { root._handleLine(line) }
    }

    onConnectionStateChanged: {
      if (connected) {
        root._triedAutostart = false
        root.call("status", null, function(ok, res) { if (ok && res) root.status = res })
        root.refreshConversations()
      } else {
        // Drop callbacks that can never be answered now.
        root._pending = ({})
        root.status = { state: "disconnected", unread: 0, phoneOK: false, qrURL: "", error: "" }
        reconnectTimer.start()
      }
    }

    onError: function(err) {
      root.transportError("socket error: " + err)
      if (root.autostart && !root._triedAutostart) {
        root._triedAutostart = true
        startService.running = true
      }
    }
  }

  Process {
    id: startService
    command: ["systemctl", "--user", "start", root.serviceName]
    onExited: reconnectTimer.start()
  }

  Timer {
    id: reconnectTimer
    interval: 2000
    repeat: false
    onTriggered: if (!sock.connected && root.socketPath !== "") sock.connected = true
  }

  function _handleLine(line) {
    if (!line || line.length === 0) return
    var frame
    try {
      frame = JSON.parse(line)
    } catch (e) {
      root.transportError("bad frame from daemon")
      return
    }

    if (frame.event !== undefined) {
      root._handleEvent(frame)
      return
    }

    var cb = _pending[frame.id]
    if (cb) {
      delete _pending[frame.id]
      cb(frame.ok === true, frame.ok === true ? frame.result : (frame.error || "unknown error"))
    }
  }

  function _handleEvent(frame) {
    switch (frame.event) {
    case "status":
      root.status = frame.data
      break
    case "conversation":
      root._mergeConversation(frame.data)
      break
    case "message":
      root.messageReceived(frame.data)
      break
    case "qr":
      // The status event carries the same URL; this is just a nudge for a
      // panel already sitting on the pairing screen.
      break
    case "paired":
      root.paired()
      root.refreshConversations()
      break
    }
  }

  // Splice an updated conversation into the list, preserving pinned-then-
  // newest ordering so the view matches what the daemon would send.
  function _mergeConversation(conv) {
    if (!conv || !conv.id) return
    var list = root.conversations.slice()
    var found = false
    for (var i = 0; i < list.length; i++) {
      if (list[i].id === conv.id) { list[i] = conv; found = true; break }
    }
    if (!found) list.push(conv)
    list.sort(function(a, b) {
      if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1
      return (b.timestamp || 0) - (a.timestamp || 0)
    })
    root.conversations = list
    root.conversationUpdated(conv)
  }
}
