package crypto

import (
"crypto/ed25519"
"crypto/rand"
"encoding/json"
"errors"
"fmt"
"os"

"github.com/mr-tron/base58"
"golang.org/x/crypto/curve25519"
)

const KeyFile = "node.key"

type Identity struct {
PrivKeyEd ed25519.PrivateKey
PubKeyEd  ed25519.PublicKey
PrivKeyX  *[32]byte
PubKeyX   *[32]byte
DID       string
}

type savedKey struct {
PrivEd []byte `json:"priv_ed"`
PrivX  []byte `json:"priv_x"`
}

func LoadOrCreateIdentity() (*Identity, error) {
var id Identity

if _, err := os.Stat(KeyFile); os.IsNotExist(err) {
pubEd, privEd, err := ed25519.GenerateKey(rand.Reader)
if err != nil {
return nil, err
}
var privX, pubX [32]byte
if _, err := rand.Read(privX[:]); err != nil {
return nil, err
}
curve25519.ScalarBaseMult(&pubX, &privX)

data, _ := json.Marshal(savedKey{PrivEd: privEd, PrivX: privX[:]})
if err := os.WriteFile(KeyFile, data, 0600); err != nil {
return nil, err
}

id.PrivKeyEd = privEd
id.PubKeyEd = pubEd
id.PrivKeyX = &privX
id.PubKeyX = &pubX
} else {
data, err := os.ReadFile(KeyFile)
if err != nil {
return nil, err
}
var sk savedKey
if err := json.Unmarshal(data, &sk); err != nil {
return nil, errors.New("clave corrupta")
}
if len(sk.PrivEd) != ed25519.PrivateKeySize || len(sk.PrivX) != 32 {
return nil, errors.New("tamaño de clave inválido")
}

id.PrivKeyEd = ed25519.PrivateKey(sk.PrivEd)
id.PubKeyEd = id.PrivKeyEd.Public().(ed25519.PublicKey)

var px [32]byte
copy(px[:], sk.PrivX)
id.PrivKeyX = &px
var pubx [32]byte
curve25519.ScalarBaseMult(&pubx, &px)
id.PubKeyX = &pubx
}

id.DID = fmt.Sprintf("did:maia:%s", base58.Encode(id.PubKeyEd))
return &id, nil
}

func (id *Identity) SignMessage(msg []byte) []byte {
return ed25519.Sign(id.PrivKeyEd, msg)
}

func VerifyMessage(pubKeyEd []byte, msg, sig []byte) bool {
if len(pubKeyEd) != ed25519.PublicKeySize {
return false
}
return ed25519.Verify(pubKeyEd, msg, sig)
}

// ============================================================
// 🔍 LOGS DE DEPURACIÓN EN DeriveSharedKey
// ============================================================
func DeriveSharedKey(myPrivX *[32]byte, theirPubX []byte) ([]byte, error) {
// 🔍 LOG: Ver qué llega
fmt.Printf("🔍 DEBUG DeriveSharedKey: theirPubX len=%d\n", len(theirPubX))
fmt.Printf("🔍 DEBUG DeriveSharedKey: theirPubX hex=%x\n", theirPubX)

if len(theirPubX) != 32 {
return nil, errors.New("pubkey x25519 inválida")
}
var theirPubArray [32]byte
copy(theirPubArray[:], theirPubX)

var shared [32]byte
curve25519.ScalarMult(&shared, myPrivX, &theirPubArray)
return shared[:], nil
}
