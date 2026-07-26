package crypto

import (
    "encoding/hex"
    "encoding/json"
    "fmt"
    "os"
)

var ACLFile = "acl.json"

type ACL struct {
    Peers map[string]PeerInfo `json:"peers"`
}

type PeerInfo struct {
    DID      string `json:"did"`
    PubKeyEd string `json:"pubkey_ed"`
    PubKeyX  string `json:"pubkey_x"`
    AddedAt  int64  `json:"added_at"`
    AddedBy  string `json:"added_by,omitempty"`
}

func LoadACL() (*ACL, error) {
    data, err := os.ReadFile(ACLFile)
    if err != nil {
        if os.IsNotExist(err) {
            return &ACL{Peers: make(map[string]PeerInfo)}, nil
        }
        return nil, err
    }

    // ✅ Forzar permisos 0600 AL LEER (archivos existentes)
    os.Chmod(ACLFile, 0600)

    var acl ACL
    if err := json.Unmarshal(data, &acl); err != nil {
        return nil, err
    }

    if acl.Peers == nil {
        acl.Peers = make(map[string]PeerInfo)
    }

    return &acl, nil
}

func (a *ACL) Save() error {
    data, err := json.MarshalIndent(a, "", "  ")
    if err != nil {
        return err
    }
    if err := os.WriteFile(ACLFile, data, 0600); err != nil {
        return err
    }
    // ✅ Asegurar permisos DESPUÉS de escribir
    return os.Chmod(ACLFile, 0600)
}

func (a *ACL) Add(peer PeerInfo) {
    a.Peers[peer.DID] = peer
}

func (a *ACL) Remove(did string) {
    delete(a.Peers, did)
}

func (a *ACL) Get(did string) (PeerInfo, bool) {
    peer, ok := a.Peers[did]
    return peer, ok
}

func (a *ACL) List() []PeerInfo {
    list := make([]PeerInfo, 0, len(a.Peers))
    for _, peer := range a.Peers {
        list = append(list, peer)
    }
    return list
}

func (a *ACL) Clear() {
    a.Peers = make(map[string]PeerInfo)
}

// ============================================================================
// FUNCIONES DE COMPATIBILIDAD
// ============================================================================

func (a *ACL) IsAllowed(did string) bool {
    _, ok := a.Peers[did]
    return ok
}

func (a *ACL) GetPeerKeys(did string) ([]byte, []byte, error) {
    peer, ok := a.Peers[did]
    if !ok {
        return nil, nil, os.ErrNotExist
    }

    pubEd, err := hex.DecodeString(peer.PubKeyEd)
    if err != nil {
        return nil, nil, err
    }

    pubX, err := hex.DecodeString(peer.PubKeyX)
    if err != nil {
        return nil, nil, err
    }

    return pubEd, pubX, nil
}

func AddPeer(did, pubKeyEd, pubKeyX string) error {
    acl, err := LoadACL()
    if err != nil {
        return err
    }
    acl.Peers[did] = PeerInfo{
        DID:      did,
        PubKeyEd: pubKeyEd,
        PubKeyX:  pubKeyX,
    }
    return acl.Save()
}

func RemovePeer(did string) error {
    acl, err := LoadACL()
    if err != nil {
        return err
    }
    delete(acl.Peers, did)
    return acl.Save()
}

func ClearACL() error {
    acl := &ACL{Peers: make(map[string]PeerInfo)}
    return acl.Save()
}

func (id *Identity) GetACLSnippet() string {
    pubEdHex := fmt.Sprintf("%x", id.PubKeyEd)
    pubXHex := fmt.Sprintf("%x", id.PubKeyX[:])
    return fmt.Sprintf("acl import %s %s %s", id.DID, pubEdHex, pubXHex)
}
