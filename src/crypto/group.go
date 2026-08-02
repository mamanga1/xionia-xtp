package crypto

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Group representa un grupo de chat soberano.
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
	Admin   string   `json:"admin"`
	Created string   `json:"created"`
}

// GroupStore contiene todos los grupos del nodo.
type GroupStore struct {
	Groups map[string]*Group `json:"groups"`
	mu     sync.RWMutex
}

var (
	groupCache *GroupStore
	groupOnce  sync.Once
	groupMu    sync.Mutex // protege groupCache al recargar
)

// getGroupsFile retorna la ruta al archivo de grupos.
//
// Prioridad:
//  1. $XION_HOME (seteado por XioniaSetDataDir en mobile.go — es el
//     getApplicationDocumentsDirectory() de Flutter en Android)
//  2. ".xion/groups.json" relativo al directorio de trabajo actual
//     (comportamiento del shell CLI en desktop)
//
// Esto es lo mismo que hace acl.go y alias.go — sin esto los grupos
// se guardan en UserHomeDir() que en Android no es el dataDir de la app.
func getGroupsFile() string {
	if home := os.Getenv("XION_HOME"); home != "" {
		return filepath.Join(home, ".xion", "groups.json")
	}
	return filepath.Join(".xion", "groups.json")
}

// LoadGroups carga el store de grupos desde disco.
// Es seguro llamarlo concurrentemente — usa groupMu para serializar
// recargas y groupOnce para la inicialización del cache en memoria.
func LoadGroups() (*GroupStore, error) {
	groupOnce.Do(func() {
		groupCache = &GroupStore{
			Groups: make(map[string]*Group),
		}
	})

	path := getGroupsFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return groupCache, nil
		}
		return nil, err
	}

	groupMu.Lock()
	defer groupMu.Unlock()
	if err := json.Unmarshal(data, groupCache); err != nil {
		return nil, err
	}
	return groupCache, nil
}

// SaveGroups persiste el store a disco. Crea el directorio si no existe.
func SaveGroups(store *GroupStore) error {
	path := getGroupsFile()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	store.mu.RLock()
	data, err := json.MarshalIndent(store, "", "  ")
	store.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// CreateGroup crea un grupo nuevo con adminDID como administrador y único miembro inicial.
// Si el alias ya existe, lo sobreescribe.
func CreateGroup(alias, name, adminDID string) error {
	store, err := LoadGroups()
	if err != nil {
		return err
	}
	store.mu.Lock()
	store.Groups[alias] = &Group{
		Name:    name,
		Members: []string{adminDID},
		Admin:   adminDID,
		Created: "2026-08-02",
	}
	store.mu.Unlock()
	return SaveGroups(store)
}

// AddMember agrega did al grupo alias. No falla si ya es miembro.
func AddMember(alias, did string) error {
	store, err := LoadGroups()
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	group, exists := store.Groups[alias]
	if !exists {
		return nil
	}
	for _, m := range group.Members {
		if m == did {
			return nil // ya estaba
		}
	}
	group.Members = append(group.Members, did)
	return SaveGroups(store)
}

// RemoveMember saca did del grupo alias. No falla si no era miembro.
func RemoveMember(alias, did string) error {
	store, err := LoadGroups()
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	group, exists := store.Groups[alias]
	if !exists {
		return nil
	}
	newMembers := make([]string, 0, len(group.Members))
	for _, m := range group.Members {
		if m != did {
			newMembers = append(newMembers, m)
		}
	}
	group.Members = newMembers
	return SaveGroups(store)
}

// GetGroup retorna el grupo por alias. ok=false si no existe.
func GetGroup(alias string) (*Group, bool) {
	store, err := LoadGroups()
	if err != nil {
		return nil, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	group, exists := store.Groups[alias]
	return group, exists
}

// SaveGroupDirect guarda un grupo completo recibido por sincronización
// (comando GROUP_SYNC de otro nodo).
func SaveGroupDirect(alias string, group *Group) error {
	store, err := LoadGroups()
	if err != nil {
		return err
	}
	store.mu.Lock()
	store.Groups[alias] = group
	store.mu.Unlock()
	return SaveGroups(store)
}

// DeleteGroup elimina un grupo por alias.
func DeleteGroup(alias string) error {
	store, err := LoadGroups()
	if err != nil {
		return err
	}
	store.mu.Lock()
	delete(store.Groups, alias)
	store.mu.Unlock()
	return SaveGroups(store)
}
