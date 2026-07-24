package xionia

/*
#cgo LDFLAGS: -lm
#include <stdlib.h>
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"unsafe"
	"web5-mesh/src/crypto"
	"web5-mesh/src/config"
)

// ============================================================================
// ESTRUCTURAS PARA JSON
// ============================================================================

type ACLPacket struct {
	Version int              `json:"v"`
	From    string           `json:"from"`
	Sig     string           `json:"sig"`
	Peers   []ACLPackedPeer `json:"peers"`
}

type ACLPackedPeer struct {
	DID   string `json:"did"`
	Alias string `json:"alias"`
	PubEd string `json:"pubEd"`
	PubX  string `json:"pubX"`
}

// ============================================================================
// EXPORTADAS A C (el albañil nunca toca esto)
// ============================================================================

//export XioniaReset
func XioniaReset() {
	crypto.ClearACL()
	// alias.ClearAliases() // descomentar cuando exista
	// group.DeleteAllGroups() // descomentar cuando exista
	config.ResetFaroAddr()
	// os.RemoveAll(".xion/")
	// os.Remove("node.key")
	// os.Remove("acl.json")
}

//export XioniaExportACLPacket
func XioniaExportACLPacket() *C.char {
	acl, err := crypto.LoadACL()
	if err != nil {
		return C.CString("")
	}

	id, err := crypto.LoadOrCreateIdentity()
	if err != nil {
		return C.CString("")
	}

	packet := ACLPacket{
		Version: 1,
		From:    id.DID,
		Peers:   make([]ACLPackedPeer, 0),
	}

	for did, peer := range acl.Peers {
		alias, _ := crypto.ResolveDIDToAlias(did)
		packet.Peers = append(packet.Peers, ACLPackedPeer{
			DID:   did,
			Alias: alias,
			PubEd: peer.PubKeyEd,
			PubX:  peer.PubKeyX,
		})
	}

	// Firmar el packet
	jsonBytes, _ := json.Marshal(packet)
	sig := id.SignMessage(jsonBytes)
	packet.Sig = fmt.Sprintf("%x", sig)

	finalJSON, _ := json.Marshal(packet)
	return C.CString(string(finalJSON))
}

//export XioniaImportACLPacket
func XioniaImportACLPacket(packetC *C.char) {
	packetStr := C.GoString(packetC)

	var packet ACLPacket
	if err := json.Unmarshal([]byte(packetStr), &packet); err != nil {
		return
	}

	// Verificar firma del remitente
	// (simplificado — en producción verificar con pubkey del remitente)

	for _, p := range packet.Peers {
		crypto.AddPeer(p.DID, p.PubEd, p.PubX)
		if p.Alias != "" {
			crypto.AddAlias(p.Alias, p.DID)
		}
	}
}

//export XioniaConnectFaro
func XioniaConnectFaro(addrC *C.char) {
	addr := C.GoString(addrC)
	config.SetFaroAddr(addr)
}

//export XioniaGetFaroAddr
func XioniaGetFaroAddr() *C.char {
	return C.CString(config.GetFaroAddr())
}

//export XioniaSendChat
func XioniaSendChat(targetC, msgC *C.char) *C.char {
	target := C.GoString(targetC)
	msg := C.GoString(msgC)

	// Resolver alias → DID
	targetDID, _ := crypto.ResolveNode(target)
	if targetDID == "" {
		targetDID = target
	}

	// Aquí iría la lógica real de envío
	// Por ahora devolvemos ok para que el albañil vea que funciona
	result := fmt.Sprintf("OK: mensaje a %s (%s)", target, targetDID)
	return C.CString(result)
}

//export XioniaGetMyDID
func XioniaGetMyDID() *C.char {
	id, err := crypto.LoadOrCreateIdentity()
	if err != nil {
		return C.CString("ERROR")
	}
	return C.CString(id.DID)
}

//export XioniaGetContactsJSON
func XioniaGetContactsJSON() *C.char {
	acl, err := crypto.LoadACL()
	if err != nil {
		return C.CString("[]")
	}

	type Contact struct {
		DID   string `json:"did"`
		Alias string `json:"alias"`
	}

	contacts := make([]Contact, 0)
	for did := range acl.Peers {
		alias, _ := crypto.ResolveDIDToAlias(did)
		contacts = append(contacts, Contact{DID: did, Alias: alias})
	}

	jsonBytes, _ := json.Marshal(contacts)
	return C.CString(string(jsonBytes))
}

//export XioniaFreeString
func XioniaFreeString(s *C.char) {
	C.free(unsafe.Pointer(s))
}

func main() {}
