package crypto

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const groupsFile = ".xion/groups.json"

type Group struct {
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Admin    string   `json:"admin"`
	Created  string   `json:"created"`
}

type GroupStore struct {
	Groups map[string]*Group `json:"groups"`
	mu     sync.RWMutex
}

var groupCache *GroupStore
var groupOnce sync.Once

func getGroupsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, groupsFile)
}

func LoadGroups() (*GroupStore, error) {
	groupOnce.Do(func() {
		groupCache = &GroupStore{
			Groups: make(map[string]*Group),
		}
	})

	path := getGroupsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return groupCache, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, groupCache); err != nil {
		return nil, err
	}
	return groupCache, nil
}

func SaveGroups(store *GroupStore) error {
	path := getGroupsPath()
	os.MkdirAll(filepath.Dir(path), 0700)
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

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
		Created: "2026-07-01",
	}
	store.mu.Unlock()
	return SaveGroups(store)
}

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
			return nil
		}
	}

	group.Members = append(group.Members, did)
	return SaveGroups(store)
}

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

	newMembers := []string{}
	for _, m := range group.Members {
		if m != did {
			newMembers = append(newMembers, m)
		}
	}
	group.Members = newMembers
	return SaveGroups(store)
}

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

// SaveGroupDirect guarda un grupo completo (para sincronización entre nodos)
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

// DeleteGroup elimina un grupo por alias (exportada para uso desde commands y shell)
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
