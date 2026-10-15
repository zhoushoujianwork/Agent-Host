package state

import (
	"regexp"
	"strings"
)

var userIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// PrivateUserID recognizes only Host-selected private partitions. Existing owner
// partitions retain their identity; group audience hashes never match this form.
func PrivateUserID(audience string) string {
	if audience == OwnerAudienceID {
		return "owner"
	}
	if strings.HasPrefix(audience, "user:") {
		id := strings.TrimPrefix(audience, "user:")
		if id != "owner" && userIdentifier.MatchString(id) {
			return id
		}
	}
	return ""
}
