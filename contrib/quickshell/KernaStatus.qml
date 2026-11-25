import QtQuick
import Quickshell
import Quickshell.Io

// A small window with kerna's verdict, refreshed every 10 seconds.
// Try it:  qs -p contrib/quickshell/KernaStatus.qml
// To put it in your own shell, copy the Process and Timer below into any component
// and show win.text, win.cls and win.detail wherever you like.
FloatingWindow {
    id: win
    implicitWidth: 420
    implicitHeight: 150
    color: "#18181b"

    property string text: "…"       // "● ok", "● 2" ...
    property string cls: "ok"       // ok, note, warn, bad
    property string detail: ""      // the findings, one per line

    Process {
        id: proc
        // Change the path if kerna is not in ~/.local/bin
        command: ["sh", "-c", "$HOME/.local/bin/kerna waybar"]
        stdout: StdioCollector {
            onStreamFinished: {
                try {
                    const o = JSON.parse(this.text)
                    win.text = o.text
                    win.cls = o.class
                    win.detail = o.tooltip.replace(/<[^>]+>/g, "").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&")
                } catch (e) {
                    win.text = "kerna?"
                    win.detail = String(e)
                }
            }
        }
    }

    Timer {
        interval: 10000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: proc.running = true
    }

    Column {
        anchors.fill: parent
        anchors.margins: 16
        spacing: 8
        Text {
            text: win.text
            font.pixelSize: 26
            font.bold: true
            color: win.cls === "bad" ? "#f87171" : win.cls === "warn" ? "#fbbf24" : win.cls === "note" ? "#38bdf8" : "#4ade80"
        }
        Text {
            width: parent.width
            text: win.detail
            color: "#e4e4e7"
            font.pixelSize: 12
            wrapMode: Text.Wrap
            elide: Text.ElideRight
            maximumLineCount: 6
        }
    }
}
