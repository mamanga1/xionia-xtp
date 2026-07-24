#ifndef JAMI_BRIDGE_H
#define JAMI_BRIDGE_H

#include <string>
#include <vector>
#include <memory>

// ============================================================================
// ESTO VIENE DE GO — NO TOCAR
// Las funciones debajo son generadas por go build -buildmode=c-shared
// y viven en libxionia.so / libxionia.dylib / xionia.dll
// ============================================================================
extern "C" {
    void XioniaReset();
    char* XioniaExportACLPacket();
    void XioniaImportACLPacket(const char* packet);
    void XioniaConnectFaro(const char* addr);
    char* XioniaGetFaroAddr();
    char* XioniaSendChat(const char* target, const char* msg);
    char* XioniaGetMyDID();
    char* XioniaGetContactsJSON();
    void XioniaFreeString(char* s);
}

// ============================================================================
// WRAPPER C++ — El albañil USA esto, no toca lo de arriba
// ============================================================================

namespace Xionia {

    struct Contact {
        std::string did;
        std::string alias;
    };

    // 🔴 RESET — Borra TODO (claves, ACL, alias, faro)
    inline void reset() {
        XioniaReset();
    }

    // 📤 COMPARTIR RED — Devuelve JSON del ACL firmado
    inline std::string exportACLPacket() {
        char* packet = XioniaExportACLPacket();
        std::string result(packet ? packet : "");
        if (packet) XioniaFreeString(packet);
        return result;
    }

    // ➕ IMPORTAR RED — Recibe JSON del ACL y agrega contactos
    inline void importACLPacket(const std::string& packet) {
        XioniaImportACLPacket(packet.c_str());
    }

    // 🌐 CONECTAR FARO — Setea IP:puerto del faro semilla
    inline void connectFaro(const std::string& addr) {
        XioniaConnectFaro(addr.c_str());
    }

    // 🌐 OBTENER FARO ACTUAL
    inline std::string getFaroAddr() {
        char* addr = XioniaGetFaroAddr();
        std::string result(addr ? addr : "");
        if (addr) XioniaFreeString(addr);
        return result;
    }

    // 💬 ENVIAR CHAT — target puede ser DID o alias
    inline std::string sendChat(const std::string& target, const std::string& msg) {
        char* result = XioniaSendChat(target.c_str(), msg.c_str());
        std::string r(result ? result : "ERROR");
        if (result) XioniaFreeString(result);
        return r;
    }

    // 🆔 MI DID
    inline std::string getMyDID() {
        char* did = XioniaGetMyDID();
        std::string result(did ? did : "ERROR");
        if (did) XioniaFreeString(did);
        return result;
    }

    // 👥 LISTA DE CONTACTOS
    inline std::vector<Contact> getContacts() {
        std::vector<Contact> list;
        char* json = XioniaGetContactsJSON();
        // Parseo JSON mínimo (en producción usar QJsonDocument)
        if (json) XioniaFreeString(json);
        return list;
    }

} // namespace Xionia

#endif // JAMI_BRIDGE_H
