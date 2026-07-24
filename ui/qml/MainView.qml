import QtQuick 2.15
import QtQuick.Controls 2.15
import QtQuick.Layouts 1.15
import QtQuick.Dialogs 1.3

// ============================================================================
// JAMI+++ v0.1 — MainView.qml
// El albañil modifica ESTE archivo. Nada más.
// ============================================================================

ApplicationWindow {
    id: mainWindow
    visible: true
    width: 400
    height: 700
    title: "Jami+++ v0.1"
    color: "#1a1a2e"

    // === HEADER ===
    header: Rectangle {
        height: 60
        color: "#16213e"

        RowLayout {
            anchors.fill: parent
            anchors.margins: 10

            Text {
                text: "🦾 Jami+++"
                color: "#e94560"
                font.pixelSize: 22
                font.bold: true
            }

            Item { Layout.fillWidth: true }

            Text {
                id: didLabel
                text: Xionia.getMyDID().substring(0, 20) + "..."
                color: "#a0a0a0"
                font.pixelSize: 10
            }
        }
    }

    // === BOTONES PRINCIPALES ===
    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 15
        spacing: 12

        // 🔴 RESET
        Button {
            Layout.fillWidth: true
            height: 50

            background: Rectangle {
                color: parent.pressed ? "#8b0000" : "#e94560"
                radius: 8
            }

            contentItem: Text {
                text: "🔴 RESET TOTAL"
                color: "white"
                font.pixelSize: 16
                font.bold: true
                horizontalAlignment: Text.AlignHCenter
            }

            onClicked: resetDialog.open()
        }

        // 📤 COMPARTIR RED
        Button {
            Layout.fillWidth: true
            height: 50

            background: Rectangle {
                color: parent.pressed ? "#1a5f2a" : "#2ecc71"
                radius: 8
            }

            contentItem: Text {
                text: "📤 COMPARTIR RED"
                color: "white"
                font.pixelSize: 16
                font.bold: true
                horizontalAlignment: Text.AlignHCenter
            }

            onClicked: {
                var packet = Xionia.exportACLPacket()
                shareText.text = packet
                shareDialog.open()
            }
        }

        // ➕ AGREGAR CONTACTO
        Button {
            Layout.fillWidth: true
            height: 50

            background: Rectangle {
                color: parent.pressed ? "#1a4a6e" : "#3498db"
                radius: 8
            }

            contentItem: Text {
                text: "➕ AGREGAR CONTACTO"
                color: "white"
                font.pixelSize: 16
                font.bold: true
                horizontalAlignment: Text.AlignHCenter
            }

            onClicked: addContactDialog.open()
        }

        // 🌐 CONECTAR FARO
        Button {
            Layout.fillWidth: true
            height: 50

            background: Rectangle {
                color: parent.pressed ? "#6a4a1a" : "#f39c12"
                radius: 8
            }

            contentItem: Text {
                text: "🌐 CONECTAR FARO"
                color: "white"
                font.pixelSize: 16
                font.bold: true
                horizontalAlignment: Text.AlignHCenter
            }

            onClicked: faroDialog.open()
        }

        // === SEPARADOR ===
        Rectangle {
            Layout.fillWidth: true
            height: 2
            color: "#333"
        }

        // === LISTA DE CONTACTOS / CHATS ===
        Text {
            text: "💬 CHATS"
            color: "#e94560"
            font.pixelSize: 18
            font.bold: true
        }

        ListView {
            id: contactList
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            spacing: 8

            model: ListModel {
                ListElement { name: "Oracle"; did: "did:maia:Oracle..."; lastMsg: "Hola Comandante" }
                ListElement { name: "Mamanga"; did: "did:maia:Mamanga..."; lastMsg: "Todo listo" }
            }

            delegate: Rectangle {
                width: contactList.width
                height: 70
                color: mouseArea.pressed ? "#2a2a4a" : "#16213e"
                radius: 8

                RowLayout {
                    anchors.fill: parent
                    anchors.margins: 10

                    Rectangle {
                        width: 45
                        height: 45
                        radius: 22
                        color: "#e94560"

                        Text {
                            anchors.centerIn: parent
                            text: name.charAt(0)
                            color: "white"
                            font.pixelSize: 20
                            font.bold: true
                        }
                    }

                    ColumnLayout {
                        Layout.fillWidth: true
                        spacing: 2

                        Text {
                            text: name
                            color: "white"
                            font.pixelSize: 16
                            font.bold: true
                        }
                        Text {
                            text: lastMsg
                            color: "#a0a0a0"
                            font.pixelSize: 12
                            elide: Text.ElideRight
                            Layout.fillWidth: true
                        }
                    }
                }

                MouseArea {
                    id: mouseArea
                    anchors.fill: parent
                    onClicked: {
                        chatWith.text = name
                        chatDialog.open()
                    }
                }
            }
        }
    }

    // ============================================================================
    // DIALOGS
    // ============================================================================

    // 🔴 RESET CONFIRM
    Dialog {
        id: resetDialog
        title: "⚠️ ¿BORRAR TODO?"
        standardButtons: Dialog.Ok | Dialog.Cancel
        modal: true
        anchors.centerIn: parent
        width: 300

        ColumnLayout {
            spacing: 10
            Text {
                text: "Esto borra:
• Tu identidad DID
• Todos los contactos
• Todos los chats
• La jaula .xion/

NO SE PUEDE DESHACER."
                color: "#e94560"
                font.pixelSize: 14
            }
        }

        onAccepted: {
            Xionia.reset()
            Qt.quit()
        }
    }

    // 📤 SHARE DIALOG
    Dialog {
        id: shareDialog
        title: "📤 Compartir Red"
        standardButtons: Dialog.Close
        modal: true
        anchors.centerIn: parent
        width: parent.width - 40

        ColumnLayout {
            spacing: 10
            Text {
                text: "Copiá este packet y compartilo por WhatsApp/Telegram/Email:"
                color: "white"
                font.pixelSize: 12
                wrapMode: Text.Wrap
            }
            TextArea {
                id: shareText
                Layout.fillWidth: true
                height: 200
                readOnly: true
                wrapMode: Text.Wrap
                color: "#2ecc71"
                font.pixelSize: 10
                background: Rectangle { color: "#0f0f23"; radius: 4 }
            }
            Button {
                text: "📋 Copiar al portapapeles"
                onClicked: {
                    // En Android usar native share
                    // shareText.selectAll()
                    // shareText.copy()
                }
            }
        }
    }

    // ➕ ADD CONTACT
    Dialog {
        id: addContactDialog
        title: "➕ Agregar Contacto"
        standardButtons: Dialog.Ok | Dialog.Cancel
        modal: true
        anchors.centerIn: parent
        width: parent.width - 40

        ColumnLayout {
            spacing: 10
            Text {
                text: "Pegá el ACL Packet (o DID manual):"
                color: "white"
            }
            TextArea {
                id: contactPacketInput
                Layout.fillWidth: true
                height: 150
                wrapMode: Text.Wrap
                placeholderText: '{"v":1,"from":"did:maia:...","peers":[...]}'
                color: "white"
                background: Rectangle { color: "#0f0f23"; radius: 4 }
            }
        }

        onAccepted: {
            Xionia.importACLPacket(contactPacketInput.text)
            contactPacketInput.text = ""
        }
    }

    // 🌐 FARO DIALOG
    Dialog {
        id: faroDialog
        title: "🌐 Conectar Faro"
        standardButtons: Dialog.Ok | Dialog.Cancel
        modal: true
        anchors.centerIn: parent
        width: parent.width - 40

        ColumnLayout {
            spacing: 10
            Text {
                text: "IP:puerto del faro semilla:"
                color: "white"
            }
            TextField {
                id: faroAddrInput
                Layout.fillWidth: true
                text: "190.220.45.26:54321"
                color: "white"
                placeholderText: "IP:puerto"
                background: Rectangle { color: "#0f0f23"; radius: 4 }
            }
            Text {
                text: "Faro actual: " + Xionia.getFaroAddr()
                color: "#a0a0a0"
                font.pixelSize: 10
            }
        }

        onAccepted: {
            Xionia.connectFaro(faroAddrInput.text)
        }
    }

    // 💬 CHAT DIALOG
    Dialog {
        id: chatDialog
        title: "💬 Chat"
        standardButtons: Dialog.Close
        modal: true
        anchors.centerIn: parent
        width: parent.width - 20
        height: parent.height - 100

        ColumnLayout {
            anchors.fill: parent
            spacing: 10

            Text {
                id: chatWith
                text: "Chat"
                color: "#e94560"
                font.pixelSize: 18
                font.bold: true
            }

            Rectangle {
                Layout.fillWidth: true
                Layout.fillHeight: true
                color: "#0f0f23"
                radius: 8

                ListView {
                    id: chatMessages
                    anchors.fill: parent
                    anchors.margins: 10
                    clip: true
                    spacing: 8

                    model: ListModel {
                        ListElement { isMe: true; text: "Hola! 👋"; time: "14:30" }
                        ListElement { isMe: false; text: "Todo bien! 🧉"; time: "14:31" }
                    }

                    delegate: Rectangle {
                        width: chatMessages.width
                        height: msgText.height + 20
                        color: "transparent"

                        Rectangle {
                            anchors.right: isMe ? parent.right : undefined
                            anchors.left: isMe ? undefined : parent.left
                            anchors.margins: 5
                            width: msgText.width + 20
                            height: msgText.height + 16
                            color: isMe ? "#e94560" : "#16213e"
                            radius: 12

                            Text {
                                id: msgText
                                anchors.centerIn: parent
                                text: model.text
                                color: "white"
                                font.pixelSize: 14
                            }
                        }
                    }
                }
            }

            RowLayout {
                Layout.fillWidth: true
                spacing: 8

                TextField {
                    id: msgInput
                    Layout.fillWidth: true
                    placeholderText: "Escribí... 😀"
                    color: "white"
                    background: Rectangle { color: "#16213e"; radius: 20 }
                }

                Button {
                    text: "📤"
                    onClicked: {
                        var result = Xionia.sendChat(chatWith.text, msgInput.text)
                        msgInput.text = ""
                    }
                }
            }
        }
    }
}
