package crypto

import (
"encoding/json"
"fmt"
"os"
"sync"
)

var aliasesFile = "aliases.json"
var aliasOnce sync.Once
var aliasCache map[string]string

// selfDID guarda el DID del nodo local para mostrarlo como "yo"
var selfDID string
var selfDIDMu sync.RWMutex

// SetSelfDID registra el DID propio del nodo (llamar al iniciar shell)
func SetSelfDID(did string) {
selfDIDMu.Lock()
selfDID = did
selfDIDMu.Unlock()
}

// GetSelfDID retorna el DID propio
func GetSelfDID() string {
selfDIDMu.RLock()
defer selfDIDMu.RUnlock()
return selfDID
}

// ============================================================================
// ALIAS STORE (orientado a objetos + compatibilidad)
// ============================================================================

type AliasStore struct {
Aliases map[string]string `json:"aliases"`
mu      sync.RWMutex
}

// LoadAliases carga o crea el store de aliases
func LoadAliases() (*AliasStore, error) {
data, err := os.ReadFile(aliasesFile)
if err != nil {
if os.IsNotExist(err) {
return &AliasStore{Aliases: make(map[string]string)}, nil
}
return nil, err
}

var store AliasStore
if err := json.Unmarshal(data, &store); err != nil {
return nil, err
}

if store.Aliases == nil {
store.Aliases = make(map[string]string)
}

return &store, nil
}

// Save guarda los aliases en disco
func (s *AliasStore) Save() error {
s.mu.RLock()
defer s.mu.RUnlock()
data, err := json.MarshalIndent(s, "", "  ")
if err != nil {
return err
}
return os.WriteFile(aliasesFile, data, 0600)
}

// Add agrega un alias
func (s *AliasStore) Add(name, did string) {
s.mu.Lock()
defer s.mu.Unlock()
s.Aliases[name] = did
}

// Remove elimina un alias
func (s *AliasStore) Remove(name string) {
s.mu.Lock()
defer s.mu.Unlock()
delete(s.Aliases, name)
}

// Get obtiene un DID por alias
func (s *AliasStore) Get(name string) (string, bool) {
s.mu.RLock()
defer s.mu.RUnlock()
did, ok := s.Aliases[name]
return did, ok
}

// List devuelve todos los alias
func (s *AliasStore) List() map[string]string {
s.mu.RLock()
defer s.mu.RUnlock()
result := make(map[string]string)
for k, v := range s.Aliases {
result[k] = v
}
return result
}

// ============================================================================
// FUNCIONES DE COMPATIBILIDAD (para no romper código existente)
// ============================================================================

// AddAlias agrega un alias (compatibilidad con código existente)
func AddAlias(alias, did string) error {
store, err := LoadAliases()
if err != nil {
return err
}
store.Add(alias, did)
return store.Save()
}

// RemoveAlias elimina un alias (compatibilidad)
func RemoveAlias(alias string) error {
store, err := LoadAliases()
if err != nil {
return err
}
store.Remove(alias)
return store.Save()
}

// ResolveAlias convierte alias → DID (compatibilidad)
func ResolveAlias(name string) (string, error) {
store, err := LoadAliases()
if err != nil {
return "", err
}
if did, ok := store.Get(name); ok {
return did, nil
}
return "", fmt.Errorf("alias '%s' no encontrado", name)
}

// ResolveNode convierte alias → DID. Si ya es DID, lo devuelve tal cual.
func ResolveNode(nameOrDID string) (string, bool) {
if len(nameOrDID) > 4 && nameOrDID[:4] == "did:" {
return nameOrDID, true
}

store, err := LoadAliases()
if err != nil {
return nameOrDID, false
}
if did, ok := store.Get(nameOrDID); ok {
return did, true
}
return nameOrDID, false
}

// ResolveDID convierte DID → alias (para mostrar en pantalla).
// Si no encuentra alias, devuelve el DID truncado.
func ResolveDID(did string) string {
// 1. Si es el propio nodo
selfDIDMu.RLock()
currentSelf := selfDID
selfDIDMu.RUnlock()
if currentSelf != "" && currentSelf == did {
return "yo"
}

// 2. Buscar en aliases
store, err := LoadAliases()
if err == nil {
if alias, ok := store.GetByValue(did); ok {
return alias
}
}

// 3. Fallback: DID truncado
if len(did) > 20 {
return did[:10] + "..." + did[len(did)-6:]
}
return did
}

// GetByValue busca un alias por DID (inverso)
func (s *AliasStore) GetByValue(did string) (string, bool) {
s.mu.RLock()
defer s.mu.RUnlock()
for alias, d := range s.Aliases {
if d == did {
return alias, true
}
}
return "", false
}

// ResolveDIDToAlias es alias de ResolveDID para mantener compatibilidad
func ResolveDIDToAlias(did string) string {
return ResolveDID(did)
}
