package supervisor

import (
	"os"
	"strings"
)

// agentProcessEnvironment preserves ordinary runtime configuration while
// preventing credentials from bypassing Forge's declared secret resolution.
// Sensitive parent keys are always removed; declared values are appended from
// the resolved agent environment below.
func agentProcessEnvironment(agentEnv []string) []string {
	result := make([]string, 0, len(os.Environ())+len(agentEnv))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if isCredentialEnvironmentKey(key) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, agentEnv...)
}

func isCredentialEnvironmentKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	for _, suffix := range []string{"_API_KEY", "_ACCESS_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIAL", "_CREDENTIALS"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	switch upper {
	case "API_KEY", "TOKEN", "SECRET", "PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS":
		return true
	default:
		return false
	}
}
