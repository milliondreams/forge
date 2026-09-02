package guild

import (
	"fmt"
	"regexp"
)

var safeGuildIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ValidateID enforces the v1 guild identifier contract used by storage
// namespaces and canonical filesystem paths.
func ValidateID(id string) error {
	if !safeGuildIDPattern.MatchString(id) {
		return fmt.Errorf("guild id must match %s", safeGuildIDPattern.String())
	}
	return nil
}
