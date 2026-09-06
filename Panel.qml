import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "Model.js" as Model

// Google Messages, as a bar popup: conversation list on the left, thread on
// the right, composer under the thread. All state comes from gmessagesd over
// a Unix socket — this file is layout and interaction only.
//
// Widget.qml owns the bar pill and hands this panel the button to anchor to.
Panel {
  id: root
  moduleName: "marcford.gmessages"
  ipcTarget: "marcford.gmessages"
  manageIpc: false

  property var anchorItem: null
  property var hostWidget: null
  readonly property var barIdentity: hostWidget || root

  // ---- state ----
  property string selectedConvID: ""
  property var messages: []
  property var grouped: []
  property bool loadingMessages: false
  property string threadError: ""
  property string searchQuery: ""
  // mediaID -> local path, filled in lazily as attachments scroll into view.
  property var mediaPaths: ({})
  property int qrRevision: 0

  // Emoji catalogue, reused from Omarchy's own picker so the set and its
  // search keywords match the rest of the desktop. Falls back to a small
  // built-in row if that file ever moves.
  property var emojiList: []
  property bool emojiPickerOpen: false
  // Attachment staging: a chosen or captured image waits here for a caption
  // and an explicit send, so nothing leaves the machine on a single click.
  property string pendingAttachment: ""
  property bool cameraOpen: false
  property bool sendingMedia: false
  property var browserProfiles: []
  property bool profilePickerOpen: false
  // Which message currently has its reaction bar open; empty for none.
  property string reactingTo: ""
  // The set Google Messages accepts. Anything else is sent as a custom emoji,
  // which not every recipient can render, so the picker offers only these.
  readonly property var reactionChoices: ["\u{1F44D}", "\u{2764}\u{FE0F}", "\u{1F602}", "\u{1F62E}", "\u{1F625}", "\u{1F620}", "\u{1F44E}"]
  property int countdown: 0
  property bool capturing: false
  property string capturePath: ""
  // Whether what is staged came from the webcam. Retake only makes sense for a
  // capture, and a shot taken blind deserves a bigger look before it is sent.
  property bool pendingFromCamera: false
  property bool textSelected: false
  // Brief confirmation, so a copy is not silent.
  property bool copied: false

  function flashCopied() {
    root.copied = true
    copiedTimer.restart()
  }

  Timer {
    id: copiedTimer
    interval: 1200
    onTriggered: root.copied = false
  }
  readonly property string omarchyPath: {
    var p = Quickshell.env("OMARCHY_PATH")
    return p && p.length > 0 ? p : "/usr/share/omarchy"
  }
  readonly property var fallbackEmoji: [
    { e: "\u{1F600}", k: "grinning" }, { e: "\u{1F602}", k: "laugh tears" },
    { e: "\u{1F642}", k: "smile" },    { e: "\u{1F44D}", k: "thumbs up yes" },
    { e: "\u{1F44E}", k: "thumbs down no" }, { e: "\u{2764}", k: "heart love" },
    { e: "\u{1F389}", k: "party tada" }, { e: "\u{1F62D}", k: "cry sob" },
    { e: "\u{1F614}", k: "sad" },       { e: "\u{1F44C}", k: "ok" },
    { e: "\u{1F525}", k: "fire" },      { e: "\u{1F64F}", k: "thanks pray" }
  ]

  readonly property int unread: gm.unread
  readonly property string connState: gm.state
  readonly property bool ready: gm.state === "connected"

  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color dim: Qt.darker(foreground, 1.5)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family

  readonly property var selectedConv: {
    for (var i = 0; i < gm.conversations.length; i++) {
      if (gm.conversations[i].id === root.selectedConvID) return gm.conversations[i]
    }
    return null
  }

  readonly property var visibleConversations: {
    var q = root.searchQuery.trim().toLowerCase()
    if (q === "") return gm.conversations
    var out = []
    for (var i = 0; i < gm.conversations.length; i++) {
      var c = gm.conversations[i]
      if ((c.name || "").toLowerCase().indexOf(q) >= 0
          || (c.preview || "").toLowerCase().indexOf(q) >= 0) out.push(c)
    }
    return out
  }

  // ---- client ----

  GmClient {
    id: gm
    socketPath: {
      var runtime = Quickshell.env("XDG_RUNTIME_DIR")
      if (runtime && runtime.length > 0) return runtime + "/gmessages-omarchy/daemon.sock"
      var cache = Quickshell.env("XDG_CACHE_HOME")
      if (cache && cache.length > 0) return cache + "/gmessages-omarchy/daemon.sock"
      return Quickshell.env("HOME") + "/.cache/gmessages-omarchy/daemon.sock"
    }
    autostart: root.setting("autostart", true)
    serviceName: root.setting("serviceName", "gmessagesd.service")

    onMessageReceived: function(msg) {
      if (msg.conversationID !== root.selectedConvID) return
      var list = root.messages.slice()
      // A send is echoed back with a real ID; replace the optimistic entry
      // rather than showing the message twice.
      var replaced = false
      for (var i = list.length - 1; i >= 0; i--) {
        if (list[i].pending && list[i].fromMe && list[i].text === msg.text) {
          list[i] = msg
          replaced = true
          break
        }
        if (list[i].id === msg.id) { list[i] = msg; replaced = true; break }
      }
      if (!replaced) list.push(msg)
      root.messages = list
      root.grouped = Model.groupMessages(list)
      if (root.opened && !msg.fromMe) root.markThreadRead()
      Qt.callLater(root.scrollToBottom)
    }

    onPaired: root.selectedConvID = ""
  }

  // ---- actions ----

  function selectConversation(id) {
    if (id === root.selectedConvID) return
    root.scrollRequested()   // re-pin before the new thread loads
    root.emojiPickerOpen = false
    root.cameraOpen = false
    root.reactingTo = ""
    root.pendingAttachment = ""
    root.selectedConvID = id
    root.messages = []
    root.grouped = []
    root.threadError = ""
    root.loadMessages()
  }

  function loadMessages() {
    if (root.selectedConvID === "") return
    root.loadingMessages = true
    var target = root.selectedConvID
    gm.call("messages", { conversationID: target, count: 60 }, function(ok, res) {
      // The user may have switched threads while this was in flight.
      if (target !== root.selectedConvID) return
      root.loadingMessages = false
      if (!ok) { root.threadError = String(res); return }
      root.threadError = ""
      root.messages = res.messages || []
      root.grouped = Model.groupMessages(root.messages)
      Qt.callLater(root.scrollToBottom)
      root.markThreadRead()
    })
  }

  function markThreadRead() {
    if (root.selectedConvID === "" || root.messages.length === 0) return
    var last = root.messages[root.messages.length - 1]
    if (!last || !last.id) return
    gm.call("markRead", { conversationID: root.selectedConvID, messageID: last.id }, null)
  }

  // Takes the text as an argument rather than reaching for the composer:
  // the composer lives inside the clientView Component, and ids declared in a
  // Component are not in scope here. Referencing it threw a ReferenceError,
  // so both Enter and the Send button silently did nothing.
  function sendMessage(rawText) {
    var text = (rawText || "").trim()
    if (text === "" || root.selectedConvID === "") return
    var convID = root.selectedConvID
    // Show it immediately; the daemon's echo replaces this entry.
    var list = root.messages.slice()
    list.push({
      id: "pending-" + Date.now(), conversationID: convID, text: text,
      timestamp: Date.now() * 1000, fromMe: true, pending: true, failed: false
    })
    root.messages = list
    root.grouped = Model.groupMessages(list)
    Qt.callLater(root.scrollToBottom)

    gm.call("send", { conversationID: convID, text: text }, function(ok, res) {
      if (ok || convID !== root.selectedConvID) return
      var l = root.messages.slice()
      for (var i = l.length - 1; i >= 0; i--) {
        if (l[i].pending && l[i].text === text) {
          l[i] = Object.assign({}, l[i], { pending: false, failed: true })
          break
        }
      }
      root.messages = l
      root.grouped = Model.groupMessages(l)
      root.threadError = String(res)
    })
  }

  // requestMedia resolves one attachment to a local file, once.
  // Every update must build a NEW object. Mutating the existing one and
  // assigning it back is invisible to QML: the property still holds the same
  // reference, so no change signal fires and the Image source binding never
  // re-evaluates. The file downloads and nothing ever appears.
  function _withMedia(mediaID, value) {
    var next = {}
    for (var k in root.mediaPaths) next[k] = root.mediaPaths[k]
    next[mediaID] = value
    root.mediaPaths = next
  }

  // Undownloaded MMS has no bytes on the phone's side yet; the daemon asks for
  // them and answers "pending", so retry a few times before giving up.
  function requestMedia(key, attempt) {
    if (!key) return
    var tries = attempt === undefined ? 0 : attempt
    // Only the first request dedupes; retries are deliberate re-checks for a
    // better version of an image already on screen.
    if (tries === 0 && root.mediaPaths[key] !== undefined) return
    if (tries === 0) root._withMedia(key, "")

    gm.call("media", { key: key }, function(ok, res) {
      if (ok && res && res.path) {
        root._withMedia(key, res.path)
        // A thumbnail or inline preview is only a few hundred bytes. Show it
        // straight away, but keep checking: the daemon has asked the phone to
        // upload the original, which arrives asynchronously.
        if (res.thumbnail && tries < 6) mediaRetry.schedule(key, tries + 1)
        return
      }
      if (ok && res && res.pending && tries < 6) {
        mediaRetry.schedule(key, tries + 1)
        return
      }
      if (tries === 0) root._withMedia(key, "")
    })
  }

  // Small queue so several pending attachments can be retried independently.
  Timer {
    id: mediaRetry
    property var queue: []
    interval: 8000
    repeat: false
    function schedule(key, attempt) {
      var q = queue.slice()
      q.push({ key: key, attempt: attempt })
      queue = q
      if (!running) start()
    }
    onTriggered: {
      var q = queue.slice()
      queue = []
      for (var i = 0; i < q.length; i++) root.requestMedia(q[i].key, q[i].attempt)
    }
  }

  // Emitted instead of touching messageList directly, for the same scoping
  // reason as sendMessage. The view inside clientView listens for it.
  signal scrollRequested()

  function scrollToBottom() { root.scrollRequested() }

  // ---- reactions ----

  // Toggling is decided by the daemon: the same emoji again removes it, a
  // different one switches. Passing an empty emoji removes whatever is set.
  function react(messageID, emoji) {
    root.reactingTo = ""
    if (root.selectedConvID === "" || !messageID) return
    gm.call("react", {
      conversationID: root.selectedConvID,
      messageID: messageID,
      emoji: emoji || ""
    }, function(ok, res) {
      if (!ok) root.threadError = String(res)
      // The updated message arrives as a push event, so there is nothing to
      // apply locally; a failure is the only thing worth reporting.
    })
  }

  // ---- attachments ----

  function attachFromDisk() {
    root.threadError = ""
    root.pendingFromCamera = false
    gm.call("pickImage", null, function(ok, res) {
      if (!ok) { root.threadError = String(res); return }
      if (res && res.path) root.pendingAttachment = res.path
    })
  }

  function openCamera() {
    root.emojiPickerOpen = false
    // Reopening the camera discards whatever shot is staged, so clean it up
    // rather than leaving full-resolution rejects in the cache.
    root.discardPendingCapture()
    root.pendingAttachment = ""
    root.countdown = 0
    root.cameraOpen = true
  }

  // discardPendingCapture deletes a staged webcam shot. Files picked from disk
  // belong to the user and are never touched.
  function discardPendingCapture() {
    if (!root.pendingFromCamera || root.pendingAttachment === "") return
    gm.call("discardCapture", { path: root.pendingAttachment }, null)
    root.pendingFromCamera = false
  }

  function startCountdown() {
    root.countdown = 3
    countdownTimer.start()
  }

  function runCapture() {
    root.capturing = true
    root.capturePath = root.captureDir + "/webcam-" + Date.now() + ".jpg"
    // Discard the first frames: most webcams need a moment to auto-expose, and
    // grabbing frame zero yields a black or badly-lit picture.
    captureProc.command = [
      "ffmpeg", "-y", "-f", "v4l2",
      "-i", root.setting("cameraDevice", "/dev/video0"),
      "-vf", "select=eq(n\\,20)", "-frames:v", "1", "-update", "1", "-q:v", "2",
      root.capturePath
    ]
    captureProc.running = true
  }

  Timer {
    id: countdownTimer
    interval: 1000
    repeat: true
    onTriggered: {
      root.countdown -= 1
      if (root.countdown <= 0) {
        stop()
        root.runCapture()
      }
    }
  }

  Process {
    id: captureProc
    onExited: function(code) {
      root.capturing = false
      if (code === 0) {
        root.cameraOpen = false
        root.pendingFromCamera = true
        root.pendingAttachment = root.capturePath
      } else {
        root.cameraOpen = false
        root.threadError = "Camera capture failed (ffmpeg exit " + code
          + "). Check that " + root.setting("cameraDevice", "/dev/video0") + " exists and is not in use."
      }
    }
  }

  function cancelAttachment() {
    root.discardPendingCapture()
    root.pendingAttachment = ""
    root.cameraOpen = false
  }

  function sendAttachment(caption) {
    if (root.pendingAttachment === "" || root.selectedConvID === "") return
    var path = root.pendingAttachment
    var convID = root.selectedConvID
    root.sendingMedia = true
    gm.call("sendMedia", { conversationID: convID, path: path, caption: caption || "" },
      function(ok, res) {
        root.sendingMedia = false
        if (!ok) { root.threadError = String(res); return }
        root.pendingAttachment = ""
        root.pendingFromCamera = false
        if (convID === root.selectedConvID) {
          var list = root.messages.slice()
          list.push(res)
          root.messages = list
          root.grouped = Model.groupMessages(list)
          Qt.callLater(root.scrollToBottom)
        }
      })
  }

  function refresh() {
    gm.call("refresh", null, null)
    if (root.selectedConvID !== "") root.loadMessages()
  }

  // pairFromBrowser is the primary path: the daemon locates a signed-in
  // browser profile and reads the cookies itself, so pairing needs no terminal.
  function loadProfiles() {
    gm.call("listProfiles", null, function(ok, res) {
      if (ok && res) root.browserProfiles = res
    })
  }

  function chooseProfile(name) {
    gm.call("setProfile", { name: name }, function(ok, res) {
      if (!ok) { root.threadError = String(res); return }
      if (res) root.browserProfiles = res
      root.profilePickerOpen = false
    })
  }

  function pairFromBrowser() {
    root.threadError = ""
    gm.call("pairFromBrowser", null, function(ok, res) {
      // Failures arrive as a status event carrying error + hint, which the
      // pairing view renders; nothing extra to do here.
    })
  }

  function startPairing() {
    gm.call("startPairing", null, function(ok, res) {
      if (!ok) root.threadError = String(res)
    })
  }

  function unpair() {
    gm.call("unpair", null, function() { root.selectedConvID = "" })
  }

  onOpenedChanged: {
    if (opened) {
      if (gm.state !== "connected") root.loadProfiles()
      gm.refreshConversations()
      if (root.selectedConvID !== "") root.markThreadRead()
    }
  }

  // ---- QR rendering: qrencode writes a PNG we then load ----

  readonly property string captureDir: {
    var cache = Quickshell.env("XDG_CACHE_HOME")
    var base = cache && cache.length > 0 ? cache : Quickshell.env("HOME") + "/.cache"
    return base + "/gmessages-omarchy"
  }

  readonly property string qrPath: {
    var cache = Quickshell.env("XDG_CACHE_HOME")
    var base = cache && cache.length > 0 ? cache : Quickshell.env("HOME") + "/.cache"
    return base + "/gmessages-omarchy/pairing-qr.png"
  }

  FileView {
    path: root.omarchyPath + "/shell/plugins/emojis/emojis.json"
    onLoaded: {
      try {
        var parsed = JSON.parse(text())
        root.emojiList = Array.isArray(parsed) && parsed.length > 0 ? parsed : root.fallbackEmoji
      } catch (e) {
        root.emojiList = root.fallbackEmoji
      }
    }
    onLoadFailed: root.emojiList = root.fallbackEmoji
  }

  Process {
    id: qrProc
    onExited: root.qrRevision++
  }

  function renderQR(url) {
    if (!url || url === "") return
    qrProc.running = false
    qrProc.command = ["qrencode", "-o", root.qrPath, "-s", "6", "-m", "2", "--", url]
    qrProc.running = true
  }

  Connections {
    target: gm
    function onStatusChanged() {
      if (gm.status && gm.status.qrURL) root.renderQR(gm.status.qrURL)
    }
  }

  // ---- UI ----
  //
  // Layout is anchor-based throughout. An earlier Column-with-arithmetic
  // version risked binding loops, because a positioner assigns y while the
  // child was deriving its height from that same y.

  KeyboardPanel {
    id: panel
    anchorItem: root.anchorItem
    owner: root.barIdentity
    bar: root.bar
    open: root.opened
    centerOnBar: true
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(880))
    contentHeight: panel.cappedContentHeight(Style.space(560))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      // The composer owns the keyboard whenever a thread is open, otherwise
      // typing a reply would be swallowed as panel navigation.
      blocked: composerFocus || root.textSelected
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }

      // ---- header ----
      Item {
        id: headerArea
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: parent.top
        height: Style.space(28)

        Text {
          id: titleText
          anchors.left: parent.left
          anchors.verticalCenter: parent.verticalCenter
          text: "󰭹  Messages"
          color: root.foreground
          font.family: root.fontFamily
          font.pixelSize: Style.font.heading
          font.bold: true
        }

        PanelActionButton {
          id: refreshBtn
          anchors.right: parent.right
          anchors.verticalCenter: parent.verticalCenter
          iconText: "󰑐"
          tooltipText: "Refresh"
          foreground: root.foreground
          fontFamily: root.fontFamily
          onClicked: root.refresh()
        }

        Text {
          id: statusText
          anchors.right: refreshBtn.left
          anchors.rightMargin: Style.space(8)
          anchors.verticalCenter: parent.verticalCenter
          text: Model.statusLine(gm.status)
          color: gm.state === "connected" ? root.dim : Color.urgent
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
        }
      }

      PanelSeparator {
        id: headerSep
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: headerArea.bottom
        anchors.topMargin: Style.space(6)
      }

      // ---- body: pairing, daemon-down, or the client ----
      Loader {
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: headerSep.bottom
        anchors.bottom: parent.bottom
        anchors.topMargin: Style.space(8)
        sourceComponent: {
          if (gm.state === "unpaired" || gm.state === "pairing"
              || gm.state === "gaiaPairing" || gm.state === "error") return pairingView
          if (!gm.connected) return daemonDownView
          return clientView
        }
      }
    }
  }

  // Tracks composer focus without the delegate tree having to reach into the
  // Loader that owns it.
  property bool composerFocus: false

  // ---- pairing ----

  Component {
    id: pairingView

    Item {
      id: pairView
      readonly property bool isGaia: gm.state === "gaiaPairing"
      readonly property bool isQR: gm.state === "pairing"
      readonly property bool isError: gm.state === "error"
      readonly property string emoji: gm.status && gm.status.emoji ? gm.status.emoji : ""

      Column {
        anchors.centerIn: parent
        spacing: Style.space(12)
        width: Math.min(parent.width - Style.space(40), Style.space(460))

        Text {
          width: parent.width
          horizontalAlignment: Text.AlignHCenter
          text: {
            if (pairView.isGaia) return "Tap this emoji on your phone"
            if (pairView.isQR) return "Scan with Google Messages"
            if (pairView.isError) return gm.status && gm.status.error ? gm.status.error : "Pairing failed"
            return "Pair this device"
          }
          color: pairView.isError ? Color.urgent : root.foreground
          wrapMode: Text.WordWrap
          font.family: root.fontFamily
          font.pixelSize: Style.font.heading
          font.bold: true
        }

        // The Gaia confirmation emoji. The phone offers several and the user
        // must pick the matching one, so this needs to be unmistakable.
        Text {
          anchors.horizontalCenter: parent.horizontalCenter
          visible: pairView.isGaia && pairView.emoji !== ""
          text: pairView.emoji
          font.pixelSize: Style.space(72)
          font.family: root.fontFamily
        }

        Text {
          width: parent.width
          horizontalAlignment: Text.AlignHCenter
          wrapMode: Text.WordWrap
          text: {
            var p = pairView
            if (p.isGaia) {
              return p.emoji !== ""
                ? "Google Messages on your phone is showing several emoji. Tap the one above. It expires in about a minute."
                : "Signing in to your Google account…"
            }
            if (p.isQR) return "On your phone: Messages → ⋮ → Device pairing → QR code scanner"
            if (p.isError) return gm.status && gm.status.hint ? gm.status.hint : ""
            return "Links this desktop to your phone using the Google account you are already signed in to in your browser. Your phone must be online."
          }
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.bodySmall
        }

        Rectangle {
          visible: pairView.isQR
          anchors.horizontalCenter: parent.horizontalCenter
          width: Style.space(200)
          height: width
          radius: Style.space(6)
          color: "#ffffff"

          Image {
            anchors.centerIn: parent
            width: parent.width - Style.space(12)
            height: width
            asynchronous: true
            cache: false
            fillMode: Image.PreserveAspectFit
            source: root.qrRevision > 0
              ? "file://" + root.qrPath + "?v=" + root.qrRevision
              : ""
          }
        }

        Row {
          anchors.horizontalCenter: parent.horizontalCenter
          spacing: Style.space(8)
          visible: !pairView.isGaia

          Button {
            text: pairView.isError ? "Try again" : "Pair with Google"
            foreground: root.foreground
            fontFamily: root.fontFamily
            bordered: true
            onClicked: root.pairFromBrowser()
          }

          Button {
            visible: !pairView.isQR
            text: "Use a QR code"
            foreground: root.dim
            fontFamily: root.fontFamily
            onClicked: root.startPairing()
          }
        }

        // Which profile the cookies come from. Automatic selection picks the
        // most recently used profile that has a complete cookie set, which is
        // a guess as soon as someone keeps several Google accounts apart.
        Column {
          width: parent.width
          spacing: Style.space(4)
          visible: !pairView.isGaia

          Row {
            anchors.horizontalCenter: parent.horizontalCenter
            spacing: Style.space(6)

            Text {
              anchors.verticalCenter: parent.verticalCenter
              text: {
                for (var i = 0; i < root.browserProfiles.length; i++) {
                  if (root.browserProfiles[i].selected) return "Using " + root.browserProfiles[i].name
                }
                return gm.status && gm.status.profile
                  ? "Using " + gm.status.profile + " (chosen automatically)"
                  : "Browser profile chosen automatically"
              }
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
            }

            Button {
              anchors.verticalCenter: parent.verticalCenter
              visible: root.browserProfiles.length > 1
              text: root.profilePickerOpen ? "Hide" : "Change"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: {
                root.profilePickerOpen = !root.profilePickerOpen
                if (root.profilePickerOpen) root.loadProfiles()
              }
            }
          }

          Column {
            width: parent.width
            spacing: Style.space(3)
            visible: root.profilePickerOpen

            Repeater {
              model: root.browserProfiles

              delegate: Rectangle {
                required property var modelData
                width: parent.width
                height: Style.space(38)
                radius: Style.space(6)
                color: modelData.selected
                  ? Style.selectedFillFor(root.foreground, Color.accent)
                  : (profileHover.containsMouse ? Style.hoverFillFor(root.foreground, Color.accent) : "transparent")
                opacity: modelData.usable ? 1.0 : 0.65

                MouseArea {
                  id: profileHover
                  anchors.fill: parent
                  hoverEnabled: true
                  // An unusable profile is still selectable: the user may be
                  // about to sign in to Messages there.
                  cursorShape: Qt.PointingHandCursor
                  onClicked: root.chooseProfile(modelData.name)
                }

                Text {
                  anchors.left: parent.left
                  anchors.leftMargin: Style.space(8)
                  anchors.top: parent.top
                  anchors.topMargin: Style.space(5)
                  text: (modelData.usable ? "\u2713  " : "\u2022  ") + modelData.name
                  color: root.foreground
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.bodySmall
                  font.bold: modelData.selected === true
                }

                Text {
                  anchors.left: parent.left
                  anchors.leftMargin: Style.space(8)
                  anchors.right: parent.right
                  anchors.rightMargin: Style.space(8)
                  anchors.bottom: parent.bottom
                  anchors.bottomMargin: Style.space(5)
                  elide: Text.ElideRight
                  text: modelData.usable
                    ? modelData.cookies + " cookies — ready"
                    : (modelData.reason || "unusable")
                  color: modelData.usable ? root.dim : Color.urgent
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.caption
                }
              }
            }

            Button {
              anchors.horizontalCenter: parent.horizontalCenter
              text: "Choose automatically"
              foreground: root.dim
              fontFamily: root.fontFamily
              onClicked: root.chooseProfile("")
            }
          }
        }
      }
    }
  }

  Component {
    id: daemonDownView

    Item {
      Column {
        anchors.centerIn: parent
        spacing: Style.space(10)
        width: Math.min(parent.width - Style.space(40), Style.space(420))

        Text {
          width: parent.width
          horizontalAlignment: Text.AlignHCenter
          text: "gmessagesd is not running"
          color: root.foreground
          font.family: root.fontFamily
          font.pixelSize: Style.font.heading
          font.bold: true
        }

        Text {
          width: parent.width
          horizontalAlignment: Text.AlignHCenter
          wrapMode: Text.WordWrap
          text: "Start it with:  systemctl --user start gmessagesd"
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.bodySmall
        }

        Button {
          anchors.horizontalCenter: parent.horizontalCenter
          text: "Retry"
          foreground: root.foreground
          fontFamily: root.fontFamily
          bordered: true
          onClicked: gm.reconnect()
        }
      }
    }
  }

  // ---- master/detail ----

  Component {
    id: clientView

    Item {
      id: clientRoot

      // ---- conversation list ----
      Item {
        id: listPane
        anchors.left: parent.left
        anchors.top: parent.top
        anchors.bottom: parent.bottom
        width: Math.round(parent.width * 0.34)

        TextField {
          id: searchField
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.top: parent.top
          placeholderText: "Search conversations"
          foreground: root.foreground
          onTextChanged: root.searchQuery = text
        }

        ListView {
          id: convList
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.top: searchField.bottom
          anchors.bottom: parent.bottom
          anchors.topMargin: Style.space(6)
          clip: true
          spacing: Style.space(2)
          model: root.visibleConversations
          boundsBehavior: Flickable.StopAtBounds

          delegate: Rectangle {
            id: convItem
            required property var modelData
            width: convList.width
            height: Style.space(56)
            radius: Style.space(6)
            color: modelData.id === root.selectedConvID
              ? Style.selectedFillFor(root.foreground, Color.accent)
              : (convMouse.containsMouse ? Style.hoverFillFor(root.foreground, Color.accent) : "transparent")

            MouseArea {
              id: convMouse
              anchors.fill: parent
              hoverEnabled: true
              cursorShape: Qt.PointingHandCursor
              onClicked: root.selectConversation(convItem.modelData.id)
            }

            Avatar {
              id: convAvatar
              anchors.left: parent.left
              anchors.leftMargin: Style.space(8)
              anchors.verticalCenter: parent.verticalCenter
              imagePath: convItem.modelData.avatarPath || ""
              initials: convItem.modelData.initials || "#"
              hexColor: convItem.modelData.avatarColor || ""
              seed: convItem.modelData.id || ""
              fontFamily: root.fontFamily
            }

            Text {
              id: convTime
              anchors.right: parent.right
              anchors.rightMargin: Style.space(8)
              anchors.top: parent.top
              anchors.topMargin: Style.space(9)
              text: Model.relativeTime(convItem.modelData.timestamp)
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
            }

            Text {
              anchors.left: convAvatar.right
              anchors.leftMargin: Style.space(8)
              anchors.right: convTime.left
              anchors.rightMargin: Style.space(6)
              anchors.top: parent.top
              anchors.topMargin: Style.space(8)
              elide: Text.ElideRight
              text: convItem.modelData.name || "(no name)"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              font.bold: convItem.modelData.unread === true
            }

            Rectangle {
              id: unreadDot
              anchors.left: convAvatar.right
              anchors.leftMargin: Style.space(8)
              anchors.bottom: parent.bottom
              anchors.bottomMargin: Style.space(12)
              visible: convItem.modelData.unread === true
              width: Style.space(6); height: width; radius: width / 2
              color: Color.urgent
            }

            Text {
              anchors.left: unreadDot.visible ? unreadDot.right : convAvatar.right
              anchors.leftMargin: Style.space(6)
              anchors.right: parent.right
              anchors.rightMargin: Style.space(8)
              anchors.bottom: parent.bottom
              anchors.bottomMargin: Style.space(8)
              elide: Text.ElideRight
              text: Model.previewText(convItem.modelData)
              color: convItem.modelData.unread === true ? root.foreground : root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
            }
          }
        }
      }

      // ---- thread ----
      Item {
        id: threadPane
        anchors.left: listPane.right
        anchors.leftMargin: Style.space(10)
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.bottom: parent.bottom

        // Empty state, shown until a thread is picked.
        Text {
          anchors.centerIn: parent
          visible: root.selectedConvID === ""
          text: gm.conversations.length === 0 ? "No conversations yet" : "Select a conversation"
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.body
        }

        Item {
          id: threadHeader
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.top: parent.top
          height: root.selectedConvID === "" ? 0 : Style.space(30)
          visible: root.selectedConvID !== ""

          Avatar {
            id: threadAvatar
            anchors.left: parent.left
            anchors.verticalCenter: parent.verticalCenter
            implicitWidth: Style.space(26)
            implicitHeight: Style.space(26)
            imagePath: root.selectedConv ? (root.selectedConv.avatarPath || "") : ""
            initials: root.selectedConv ? (root.selectedConv.initials || "#") : "#"
            hexColor: root.selectedConv ? (root.selectedConv.avatarColor || "") : ""
            seed: root.selectedConvID
            fontFamily: root.fontFamily
          }

          Text {
            anchors.left: threadAvatar.right
            anchors.leftMargin: Style.space(8)
            anchors.right: parent.right
            anchors.verticalCenter: parent.verticalCenter
            elide: Text.ElideRight
            text: root.selectedConv ? (root.selectedConv.name || "") : ""
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            font.bold: true
          }
        }

        PanelSeparator {
          id: threadSep
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.top: threadHeader.bottom
          anchors.topMargin: Style.space(6)
          visible: root.selectedConvID !== ""
        }

        // Composer is anchored to the bottom, so the message list can simply
        // fill whatever is left between the header and it.
        Item {
          id: composerRow
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.bottom: parent.bottom
          height: root.selectedConvID === "" ? 0 : sendButton.height
          visible: root.selectedConvID !== ""

          Button {
            id: sendButton
            anchors.right: parent.right
            anchors.verticalCenter: parent.verticalCenter
            text: "Send"
            bordered: true
            foreground: root.foreground
            fontFamily: root.fontFamily
            enabled: composer.enabled && composer.text.trim() !== ""
            onClicked: {
              root.sendMessage(composer.text)
              composer.text = ""
            }
          }

          PanelActionButton {
            id: attachButton
            anchors.left: parent.left
            anchors.verticalCenter: parent.verticalCenter
            iconText: "\u{1F4CE}"
            tooltipText: "Attach an image"
            foreground: root.foreground
            fontFamily: root.fontFamily
            enabled: composer.enabled && !root.sendingMedia
            onClicked: root.attachFromDisk()
          }

          PanelActionButton {
            id: cameraButton
            anchors.left: attachButton.right
            anchors.leftMargin: Style.space(2)
            anchors.verticalCenter: parent.verticalCenter
            iconText: "\u{1F4F7}"
            tooltipText: "Take a photo"
            foreground: root.foreground
            fontFamily: root.fontFamily
            enabled: composer.enabled && !root.sendingMedia
            onClicked: root.openCamera()
          }

          PanelActionButton {
            id: emojiButton
            anchors.left: cameraButton.right
            anchors.leftMargin: Style.space(2)
            anchors.verticalCenter: parent.verticalCenter
            iconText: "\u{1F642}"
            tooltipText: "Insert emoji"
            foreground: root.foreground
            fontFamily: root.fontFamily
            enabled: composer.enabled
            onClicked: root.emojiPickerOpen = !root.emojiPickerOpen
          }

          TextField {
            id: composer
            anchors.left: emojiButton.right
            anchors.leftMargin: Style.space(4)
            anchors.right: sendButton.left
            anchors.rightMargin: Style.space(6)
            anchors.verticalCenter: parent.verticalCenter
            foreground: root.foreground
            placeholderText: root.selectedConv && root.selectedConv.readOnly
              ? "This conversation is read-only"
              : "Message"
            enabled: root.ready && !(root.selectedConv && root.selectedConv.readOnly)
            onAccepted: {
              root.sendMessage(text)
              text = ""
            }
            onActiveFocusChanged: root.composerFocus = activeFocus
          }
        }

        // Staged attachment: what will be sent, with a caption box and an
        // explicit Send. Nothing is transmitted until this is confirmed.
        Rectangle {
          id: attachmentBar
          visible: root.pendingAttachment !== "" && root.selectedConvID !== ""
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.bottom: composerRow.top
          anchors.bottomMargin: Style.space(6)
          height: visible ? (root.pendingFromCamera ? Style.space(210) : Style.space(132)) : 0
          z: 110
          radius: Style.space(8)
          color: Color.popups.background
          border.width: 1
          border.color: Color.popups.border

          MouseArea { anchors.fill: parent; hoverEnabled: true }

          Image {
            id: attachPreview
            anchors.left: parent.left
            anchors.top: parent.top
            anchors.margins: Style.space(8)
            height: parent.height - Style.space(16)
            width: implicitWidth > 0
              ? Math.min(root.pendingFromCamera ? Style.space(260) : Style.space(150),
                         implicitWidth * (height / Math.max(1, implicitHeight)))
              : 0
            fillMode: Image.PreserveAspectFit
            asynchronous: true
            smooth: true
            source: root.pendingAttachment !== "" ? "file://" + root.pendingAttachment : ""
          }

          Text {
            id: attachName
            anchors.left: attachPreview.right
            anchors.leftMargin: Style.space(10)
            anchors.right: parent.right
            anchors.rightMargin: Style.space(10)
            anchors.top: parent.top
            anchors.topMargin: Style.space(10)
            elide: Text.ElideMiddle
            text: {
              if (root.pendingFromCamera) return "Photo from webcam"
              var p = root.pendingAttachment
              var slash = p.lastIndexOf("/")
              return slash >= 0 ? p.substring(slash + 1) : p
            }
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            font.bold: true
          }

          TextField {
            id: attachCaption
            anchors.left: attachPreview.right
            anchors.leftMargin: Style.space(10)
            anchors.right: parent.right
            anchors.rightMargin: Style.space(10)
            anchors.top: attachName.bottom
            anchors.topMargin: Style.space(8)
            placeholderText: "Add a caption (optional)"
            foreground: root.foreground
            enabled: !root.sendingMedia
            onAccepted: root.sendAttachment(text)
          }

          Row {
            anchors.right: parent.right
            anchors.rightMargin: Style.space(10)
            anchors.bottom: parent.bottom
            anchors.bottomMargin: Style.space(10)
            spacing: Style.space(6)

            Button {
              // Straight back to the camera. Without this a bad shot means
              // cancelling and hunting for the camera button again, which is
              // the common case when you cannot see what you photographed.
              visible: root.pendingFromCamera
              text: "Retake"
              foreground: root.foreground
              fontFamily: root.fontFamily
              enabled: !root.sendingMedia
              onClicked: root.openCamera()
            }

            Button {
              text: "Cancel"
              foreground: root.dim
              fontFamily: root.fontFamily
              enabled: !root.sendingMedia
              onClicked: root.cancelAttachment()
            }

            Button {
              text: root.sendingMedia ? "Sending…" : "Send image"
              foreground: root.foreground
              fontFamily: root.fontFamily
              bordered: true
              enabled: !root.sendingMedia
              onClicked: root.sendAttachment(attachCaption.text)
            }
          }
        }

        // Webcam capture.
        //
        // This deliberately does NOT use QtMultimedia. Its FFmpeg backend
        // segfaults inside the shell process, and because the Omarchy shell
        // also owns the bar and the lock screen, a crash there takes the whole
        // desktop down. Capture therefore runs as a separate ffmpeg process:
        // no live preview, but a crash can only kill the child.
        Rectangle {
          id: cameraView
          visible: root.cameraOpen && root.selectedConvID !== ""
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.bottom: composerRow.top
          anchors.bottomMargin: Style.space(6)
          height: visible ? Style.space(150) : 0
          z: 120
          radius: Style.space(8)
          color: Color.popups.background
          border.width: 1
          border.color: Color.popups.border

          MouseArea { anchors.fill: parent; hoverEnabled: true }

          onVisibleChanged: if (!visible) root.countdown = 0

          Column {
            anchors.centerIn: parent
            spacing: Style.space(8)
            width: parent.width - Style.space(24)

            Text {
              width: parent.width
              horizontalAlignment: Text.AlignHCenter
              text: {
                if (root.capturing) return "Capturing…"
                if (root.countdown > 0) return String(root.countdown)
                return "Take a photo"
              }
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: root.countdown > 0 ? Style.space(40) : Style.font.heading
              font.bold: true
            }

            Text {
              width: parent.width
              horizontalAlignment: Text.AlignHCenter
              wrapMode: Text.WordWrap
              visible: !root.capturing && root.countdown === 0
              text: "Your webcam has no preview here — the photo is shown for approval before anything is sent."
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
            }

            Row {
              anchors.horizontalCenter: parent.horizontalCenter
              spacing: Style.space(8)
              visible: !root.capturing && root.countdown === 0

              Button {
                text: "Capture in 3s"
                foreground: root.foreground
                fontFamily: root.fontFamily
                bordered: true
                onClicked: root.startCountdown()
              }

              Button {
                text: "Cancel"
                foreground: root.dim
                fontFamily: root.fontFamily
                onClicked: root.cameraOpen = false
              }
            }
          }
        }

        // Copy confirmation. A clipboard write is otherwise invisible, which
        // leaves the user unsure whether it worked.
        Rectangle {
          visible: root.copied
          anchors.horizontalCenter: parent.horizontalCenter
          anchors.bottom: composerRow.top
          anchors.bottomMargin: Style.space(10)
          z: 200
          width: copiedLabel.implicitWidth + Style.space(20)
          height: Style.space(26)
          radius: height / 2
          color: Color.popups.background
          border.width: 1
          border.color: Color.popups.border

          Text {
            id: copiedLabel
            anchors.centerIn: parent
            text: "Copied"
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
          }
        }

        // Emoji picker. Sits above the composer and inserts at the caret, so
        // it can be used mid-sentence rather than only at the end.
        Rectangle {
          id: emojiPicker
          visible: root.emojiPickerOpen && root.selectedConvID !== ""
          // The message list is declared after this and would otherwise stack
          // on top. It draws no background, so the picker stayed visible
          // through it while the ListView quietly swallowed every click.
          z: 100
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.bottom: composerRow.top
          anchors.bottomMargin: Style.space(6)
          height: Math.min(Style.space(220), parent.height * 0.6)
          radius: Style.space(8)
          // Popup surfaces have their own theme tokens; the top-level Color
          // set has no "border", which silently resolved to undefined.
          color: Color.popups.background
          border.width: 1
          border.color: Color.popups.border

          // Stops clicks on empty picker chrome from reaching whatever is
          // underneath.
          MouseArea {
            anchors.fill: parent
            hoverEnabled: true
            acceptedButtons: Qt.AllButtons
          }

          function insert(emoji) {
            // Insert at the caret and keep typing where the user left off.
            var pos = composer.cursorPosition
            composer.insert(pos, emoji)
            composer.cursorPosition = pos + emoji.length
            composer.forceActiveFocus()
          }

          readonly property var filtered: {
            var all = root.emojiList
            var q = emojiSearch.text.trim().toLowerCase()
            if (q === "") return all.slice(0, 400)
            var out = []
            for (var i = 0; i < all.length && out.length < 400; i++) {
              if (String(all[i].k || "").indexOf(q) >= 0) out.push(all[i])
            }
            return out
          }

          TextField {
            id: emojiSearch
            anchors.left: parent.left
            anchors.right: closeEmoji.left
            anchors.top: parent.top
            anchors.margins: Style.space(6)
            anchors.rightMargin: Style.space(4)
            placeholderText: "Search emoji"
            foreground: root.foreground
          }

          PanelActionButton {
            id: closeEmoji
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: Style.space(6)
            iconText: "\u{00D7}"
            tooltipText: "Close"
            foreground: root.dim
            fontFamily: root.fontFamily
            onClicked: root.emojiPickerOpen = false
          }

          GridView {
            id: emojiGrid
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: emojiSearch.bottom
            anchors.bottom: parent.bottom
            anchors.margins: Style.space(6)
            clip: true
            cellWidth: Style.space(34)
            cellHeight: Style.space(34)
            model: emojiPicker.filtered
            boundsBehavior: Flickable.StopAtBounds

            delegate: Rectangle {
              required property var modelData
              width: emojiGrid.cellWidth - Style.space(2)
              height: emojiGrid.cellHeight - Style.space(2)
              radius: Style.space(5)
              color: emojiHover.containsMouse
                ? Style.hoverFillFor(root.foreground, Color.accent)
                : "transparent"

              Text {
                anchors.centerIn: parent
                text: modelData.e
                font.pixelSize: Style.space(20)
                font.family: root.fontFamily
              }

              MouseArea {
                id: emojiHover
                anchors.fill: parent
                hoverEnabled: true
                cursorShape: Qt.PointingHandCursor
                onClicked: emojiPicker.insert(modelData.e)
              }
            }
          }
        }

        Text {
          id: threadErrorText
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.bottom: composerRow.top
          anchors.bottomMargin: visible ? Style.space(4) : 0
          visible: root.threadError !== "" && root.selectedConvID !== ""
          height: visible ? implicitHeight : 0
          text: root.threadError
          color: Color.urgent
          wrapMode: Text.WordWrap
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
        }

        ListView {
          id: messageList
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.top: threadSep.bottom
          anchors.topMargin: Style.space(6)
          anchors.bottom: cameraView.visible ? cameraView.top
            : (attachmentBar.visible ? attachmentBar.top
            : (emojiPicker.visible ? emojiPicker.top : threadErrorText.top))
          anchors.bottomMargin: Style.space(6)
          visible: root.selectedConvID !== ""
          clip: true
          spacing: Style.space(4)
          model: root.grouped
          boundsBehavior: Flickable.StopAtBounds
          cacheBuffer: 600

          // A thread should open at its newest message and stay there as new
          // ones arrive, the way every messenger behaves — until the user
          // scrolls up, at which point it must stop yanking them back.
          //
          // A single positionViewAtEnd() is not enough: delegate heights keep
          // changing after it runs, because images load asynchronously and
          // resize their bubbles. Each of those resizes shifts the content
          // under the viewport, which is what made a sent message appear to
          // jump upward. So re-pin whenever the content height changes, for as
          // long as the user is still at the bottom.
          property bool stickToBottom: true

          function pinToBottom() {
            if (count > 0) positionViewAtEnd()
          }

          onCountChanged: if (stickToBottom) Qt.callLater(pinToBottom)
          onContentHeightChanged: if (stickToBottom) Qt.callLater(pinToBottom)

          // Only user-driven movement releases the pin; programmatic
          // repositioning must not switch it off.
          onMovementEnded: stickToBottom = atYEnd
          onDraggingChanged: if (!dragging) stickToBottom = atYEnd
          onFlickEnded: stickToBottom = atYEnd

          // The wheel moves contentY without a drag or flick, so catch that too.
          onContentYChanged: if (moving || flicking || dragging) stickToBottom = atYEnd

          delegate: messageRow

          Connections {
            target: root
            function onScrollRequested() {
              messageList.stickToBottom = true
              Qt.callLater(messageList.pinToBottom)
            }
          }

          // Jump back to the newest message after scrolling up.
          Rectangle {
            visible: !messageList.stickToBottom && messageList.count > 0
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            anchors.margins: Style.space(8)
            width: Style.space(26)
            height: Style.space(26)
            radius: width / 2
            color: Color.popups.background
            border.width: 1
            border.color: Color.popups.border

            Text {
              anchors.centerIn: parent
              text: "\u2193"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
            }

            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: {
                messageList.stickToBottom = true
                messageList.pinToBottom()
              }
            }
          }
        }
      }
    }
  }

  // ---- message row ----
  //
  // One delegate handles both entry kinds. Splitting it across two components
  // behind a Loader meant reaching through parent.parent for the model data,
  // which is exactly the kind of thing that breaks silently on a refactor.

  Component {
    id: messageRow

    Item {
      id: rowRoot
      required property var modelData
      readonly property bool isDay: modelData.kind === "day"
      readonly property var msg: isDay ? null : modelData.message
      readonly property bool mine: msg ? msg.fromMe === true : false
      readonly property bool startsRun: isDay ? false : modelData.startsRun === true

      width: ListView.view ? ListView.view.width : 0
      height: isDay ? Style.space(24) : bubbleColumn.implicitHeight + (startsRun ? Style.space(6) : 0)

      // Day separator
      Text {
        anchors.centerIn: parent
        visible: rowRoot.isDay
        text: rowRoot.isDay ? rowRoot.modelData.label : ""
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: Style.font.caption
      }

      Column {
        id: bubbleColumn
        visible: !rowRoot.isDay
        anchors.right: rowRoot.mine ? parent.right : undefined
        anchors.left: rowRoot.mine ? undefined : parent.left
        anchors.bottom: parent.bottom
        width: Math.min(rowRoot.width * 0.78, Style.space(420))
        spacing: Style.space(2)

        // Sender name: group chats only, and only on the first of a run.
        Text {
          width: parent.width
          visible: !rowRoot.mine && rowRoot.startsRun
            && root.selectedConv && root.selectedConv.isGroup === true
            && text !== ""
          height: visible ? implicitHeight : 0
          text: rowRoot.msg ? (rowRoot.msg.senderName || "") : ""
          color: root.dim
          elide: Text.ElideRight
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
        }

        // Reaction picker for this message.
        Rectangle {
          visible: rowRoot.msg && root.reactingTo === rowRoot.msg.id
          width: reactionRow.implicitWidth + Style.space(10)
          height: visible ? Style.space(26) : 0
          radius: height / 2
          color: Color.popups.background
          border.width: 1
          border.color: Color.popups.border

          Row {
            id: reactionRow
            anchors.centerIn: parent
            spacing: Style.space(4)

            Repeater {
              model: root.reactionChoices

              delegate: Rectangle {
                id: choice
                required property var modelData
                width: Style.space(20)
                height: Style.space(20)
                radius: width / 2
                color: choiceHover.containsMouse
                  ? Style.hoverFillFor(root.foreground, Color.accent) : "transparent"

                Text {
                  anchors.centerIn: parent
                  text: choice.modelData
                  font.pixelSize: Style.space(13)
                  font.family: root.fontFamily
                }

                MouseArea {
                  id: choiceHover
                  anchors.fill: parent
                  hoverEnabled: true
                  cursorShape: Qt.PointingHandCursor
                  onClicked: root.react(rowRoot.msg.id, choice.modelData)
                }
              }
            }
          }
        }

        Rectangle {
          width: parent.width
          height: bubbleContent.implicitHeight + Style.space(16)
          radius: Style.space(10)

          // Sits behind the content so it never swallows clicks meant for
          // links, images, or the reaction chips.
          MouseArea {
            id: bubbleHover
            anchors.fill: parent
            hoverEnabled: true
            // Right-click copies the whole message, which is what people
            // usually want and avoids fiddly selection inside a scrolling list.
            acceptedButtons: Qt.RightButton
            onClicked: {
              if (!rowRoot.msg || !rowRoot.msg.text) return
              Quickshell.clipboardText = rowRoot.msg.text
              root.flashCopied()
            }
          }

          // React button, pinned to the bubble's outer corner and drawn above
          // the content. It used to be a glyph tacked onto the timestamp row,
          // which is a small target and, on a tall photo, nowhere near where
          // the pointer actually is. An overlay with its own z is reachable
          // whatever the bubble contains or however large it grows.
          Rectangle {
            id: reactButton
            z: 5
            // Always present rather than hover-gated. Hover over a large
            // image proved unreliable — the button simply never appeared on a
            // photo — and a control you cannot find is worse than a faint one.
            // It stays dim until pointed at.
            visible: rowRoot.msg !== null && !rowRoot.msg.pending && !rowRoot.msg.deleted
            // Guarded: this binding re-evaluates for every row, day separators
            // included, and those carry no message.
            opacity: rowRoot.msg && (reactButtonHover.containsMouse
                                     || root.reactingTo === rowRoot.msg.id)
              ? 1.0
              : (bubbleHover.containsMouse ? 0.85 : 0.25)
            anchors.verticalCenter: parent.top
            anchors.left: rowRoot.mine ? parent.left : undefined
            anchors.right: rowRoot.mine ? undefined : parent.right
            anchors.leftMargin: -Style.space(8)
            anchors.rightMargin: -Style.space(8)
            width: Style.space(22)
            height: Style.space(22)
            radius: width / 2
            color: reactButtonHover.containsMouse
              ? Style.selectedFillFor(root.foreground, Color.accent)
              : Color.popups.background
            border.width: 1
            border.color: Color.popups.border

            Text {
              anchors.centerIn: parent
              text: "\u{1F642}"
              font.pixelSize: Style.space(11)
            }

            MouseArea {
              id: reactButtonHover
              anchors.fill: parent
              anchors.margins: -Style.space(4)
              hoverEnabled: true
              cursorShape: Qt.PointingHandCursor
              onClicked: root.reactingTo =
                rowRoot.msg
                  ? (root.reactingTo === rowRoot.msg.id ? "" : rowRoot.msg.id)
                  : ""
            }
          }
          color: rowRoot.mine
            ? Style.selectedFillFor(root.foreground, Color.accent)
            : Style.normalFillFor(root.foreground, Color.accent)
          border.width: rowRoot.msg && rowRoot.msg.failed ? 1 : 0
          border.color: Color.urgent

          Column {
            id: bubbleContent
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: Style.space(8)
            spacing: Style.space(6)

            Repeater {
              model: rowRoot.msg && rowRoot.msg.attachments ? rowRoot.msg.attachments : []

              delegate: Item {
                id: attachment
                required property var modelData
                readonly property string mediaKey: modelData.key ? modelData.key : modelData.mediaID
                readonly property string mediaPath: {
                  var p = root.mediaPaths[mediaKey]
                  return p ? p : ""
                }
                readonly property bool pending: modelData.isImage && mediaPath === ""
                // A loaded image narrower than the message says it is can only
                // be the low-resolution preview. Blowing that up to fill the
                // bubble looks far worse than showing it at its own size.
                readonly property bool isPreview: img.status === Image.Ready
                  && modelData.width > 0
                  && img.implicitWidth > 0
                  && img.implicitWidth < modelData.width * 0.9

                width: bubbleContent.width
                height: {
                  if (!modelData.isImage) return Style.space(24)
                  if (img.status === Image.Ready) return img.height + (isPreview ? Style.space(18) : 0)
                  return Style.space(120)
                }

                // Bytes are fetched only once the attachment is realised, so
                // opening a long thread does not pull every image in it.
                Component.onCompleted: if (modelData.isImage) root.requestMedia(attachment.mediaKey)

                Image {
                  id: img
                  anchors.left: parent.left
                  anchors.top: parent.top
                  visible: attachment.modelData.isImage && attachment.mediaPath !== ""
                  asynchronous: true
                  smooth: true
                  fillMode: Image.PreserveAspectFit
                  // Never upscale past the source's own resolution.
                  width: implicitWidth > 0 ? Math.min(parent.width, implicitWidth) : 0
                  height: implicitWidth > 0 ? implicitHeight * (width / implicitWidth) : 0
                  source: attachment.mediaPath !== "" ? "file://" + attachment.mediaPath : ""
                }

                Text {
                  id: previewNote
                  anchors.left: parent.left
                  anchors.top: img.bottom
                  anchors.topMargin: Style.space(4)
                  visible: attachment.isPreview
                  // A fixed height: deriving it from implicitHeight fed back
                  // into the parent height that positions this, which Qt
                  // reported as a binding loop.
                  height: Style.space(14)
                  text: "Preview — fetching full image from your phone…"
                  color: root.dim
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.caption
                }

                // Placeholder while the download is in flight, so a slow or
                // failed image reads as "loading" rather than as a blank bubble.
                Rectangle {
                  anchors.fill: parent
                  visible: attachment.pending
                  radius: Style.space(6)
                  color: Style.normalFillFor(root.foreground, Color.accent)

                  Text {
                    anchors.centerIn: parent
                    text: "Loading image…"
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                }
              }
            }

            // A read-only TextEdit rather than a Text: Text cannot be
            // selected at all, so message contents could not be highlighted or
            // copied. Qt keeps the mouse grab once a drag-selection starts, so
            // this does not fight the list's scrolling.
            TextEdit {
              id: bubbleText
              width: parent.width
              visible: text !== ""
              height: visible ? implicitHeight : 0
              text: {
                if (!rowRoot.msg) return ""
                if (rowRoot.msg.deleted) return "Message deleted"
                return rowRoot.msg.text || ""
              }
              color: rowRoot.msg && rowRoot.msg.deleted ? root.dim : root.foreground
              font.italic: rowRoot.msg ? rowRoot.msg.deleted === true : false
              wrapMode: TextEdit.Wrap
              textFormat: TextEdit.PlainText
              font.family: root.fontFamily
              font.pixelSize: Style.font.body

              readOnly: true
              selectByMouse: true
              persistentSelection: false
              selectionColor: Style.selectedFillFor(root.foreground, Color.accent)
              selectedTextColor: root.foreground
              // Keep the caret out of a message the user is only reading.
              activeFocusOnPress: true
              cursorVisible: false

              onSelectedTextChanged: root.textSelected = selectedText !== ""

              // Copy explicitly rather than relying on the default handler:
              // the panel's key catcher sits above this and would otherwise
              // swallow the shortcut.
              Keys.onPressed: function (event) {
                if (event.key === Qt.Key_C && (event.modifiers & Qt.ControlModifier)) {
                  if (selectedText !== "") {
                    Quickshell.clipboardText = selectedText
                    root.flashCopied()
                  }
                  event.accepted = true
                } else if (event.key === Qt.Key_A && (event.modifiers & Qt.ControlModifier)) {
                  selectAll()
                  event.accepted = true
                }
              }
            }

            Row {
              width: parent.width
              spacing: Style.space(6)

              Text {
                text: rowRoot.msg ? Model.bubbleTime(rowRoot.msg.timestamp) : ""
                color: root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
              }

              Text {
                visible: rowRoot.msg && (rowRoot.msg.pending === true || rowRoot.msg.failed === true)
                text: rowRoot.msg && rowRoot.msg.failed ? "Failed" : "Sending…"
                color: rowRoot.msg && rowRoot.msg.failed ? Color.urgent : root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.caption
              }

              // Existing reactions. Clicking your own takes it back; clicking
              // someone else's adds yours alongside.
              Repeater {
                model: rowRoot.msg && rowRoot.msg.reactions ? rowRoot.msg.reactions : []

                delegate: Rectangle {
                  id: reactionChip
                  required property var modelData
                  width: chipText.implicitWidth + Style.space(8)
                  height: Style.space(16)
                  radius: height / 2
                  color: modelData.mine
                    ? Style.selectedFillFor(root.foreground, Color.accent)
                    : (chipHover.containsMouse ? Style.hoverFillFor(root.foreground, Color.accent) : "transparent")
                  border.width: modelData.mine ? 1 : 0
                  border.color: Color.accent

                  Text {
                    id: chipText
                    anchors.centerIn: parent
                    text: reactionChip.modelData.emoji
                      + (reactionChip.modelData.count > 1 ? " " + reactionChip.modelData.count : "")
                    color: root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                  }

                  MouseArea {
                    id: chipHover
                    anchors.fill: parent
                    hoverEnabled: true
                    cursorShape: Qt.PointingHandCursor
                    onClicked: root.react(rowRoot.msg.id, reactionChip.modelData.emoji)
                  }
                }
              }

            }
          }
        }
      }
    }
  }
}
