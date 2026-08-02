package credentials

import "sort"

const (
	AgentOSDirectoryEnv = "FORGE_AGENTOS_CREDENTIALS_DIR"
	BackendName         = "forge-vault"
)

type Store interface {
	Get(key string) (string, bool)
	Put(key, value string) error
	Delete(key string) (bool, error)
	List(prefix string) []string
}

func cloneEntries(entries map[string]string) map[string]string {
	clone := make(map[string]string, len(entries))
	for key, value := range entries {
		clone[key] = value
	}
	return clone
}

func sortedKeys(entries map[string]string, prefix string) []string {
	keys := make([]string, 0)
	for key := range entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
