import QtQuick
import qs.Commons
import qs.Ui

// Bar entry point. Owns the pill and its unread badge; Panel.qml owns the
// conversation UI. Mirrors the first-party weather widget's panel plumbing so
// shell summon/hide/toggle routing works the same way.
BarWidget {
  id: root
  moduleName: "marcford.gmessages"

  function injectPanel() {
    var target = panelLoader.item
    if (!target) return
    if ("bar" in target) target.bar = root.bar
    if ("settings" in target) target.settings = root.settings
    if ("anchorItem" in target) target.anchorItem = button
    if ("hostWidget" in target) target.hostWidget = root
  }

  function togglePanel() {
    if (panelLoader.item && panelLoader.item.toggle) panelLoader.item.toggle()
  }

  function refresh() {
    if (panelLoader.item && panelLoader.item.refresh) panelLoader.item.refresh()
  }

  readonly property bool opened: panelLoader.item ? panelLoader.item.opened === true : false
  readonly property int unread: panelLoader.item ? (panelLoader.item.unread || 0) : 0
  readonly property string connState: panelLoader.item ? (panelLoader.item.connState || "") : ""

  function open() { if (panelLoader.item && panelLoader.item.open) panelLoader.item.open() }
  function close() { if (panelLoader.item && panelLoader.item.close) panelLoader.item.close() }

  readonly property bool popoutSwitchClosing: panelLoader.item ? panelLoader.item.popoutSwitchClosing === true : false
  function closeForPopoutSwitch() { if (panelLoader.item) panelLoader.item.closeForPopoutSwitch() }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  onBarChanged: injectPanel()
  onSettingsChanged: injectPanel()

  Loader {
    id: panelLoader
    active: true
    source: Qt.resolvedUrl("Panel.qml")
    visible: false
    onLoaded: {
      root.injectPanel()
      Qt.callLater(root.injectPanel)
    }
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    // Nerd Font message glyph; dimmed when the daemon is not usable so the
    // bar reflects reachability without needing the panel open.
    text: "󰭹"
    slotSize: Style.bar.statusSlot
    tooltipText: root.unread > 0
      ? root.unread + (root.unread === 1 ? " unread conversation" : " unread conversations")
      : "Messages"

    opacity: root.connState === "connected" ? 1.0 : 0.55

    onPressed: function(b) {
      if (b === Qt.MiddleButton) root.refresh()
      else root.togglePanel()
    }

    // Unread badge, pinned to the glyph's top-right corner.
    Rectangle {
      visible: root.unread > 0
      anchors.right: parent.right
      anchors.top: parent.top
      anchors.rightMargin: Style.space(2)
      anchors.topMargin: Style.space(2)
      width: Math.max(Style.space(14), badgeText.implicitWidth + Style.space(6))
      height: Style.space(14)
      radius: height / 2
      color: Color.urgent

      Text {
        id: badgeText
        anchors.centerIn: parent
        text: root.unread > 9 ? "9+" : String(root.unread)
        color: Color.background
        font.family: root.bar ? root.bar.fontFamily : Style.font.family
        font.pixelSize: Style.space(9)
        font.bold: true
      }
    }
  }
}
