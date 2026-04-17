package protect

import (
	"net/http"
	"strings"
)

// SessionAccess reports whether the authenticated identity is allowed to
// operate on a session owned by sessionOwner with the given snapshot of
// owner-side groups. Access is granted when any of:
//
//   - identity is admin
//   - identity's username equals the session owner
//   - identity's groups intersect the session's OwnerGroups snapshot
//
// OwnerGroups are captured at session-creation time, so revoking group
// membership later does not retroactively change ACL for in-flight sessions.
func SessionAccess(id Identity, sessionOwner string, sessionOwnerGroups []string) bool {
	if id.IsAdmin {
		return true
	}
	if sessionOwner != "" && sessionOwner == id.User {
		return true
	}
	return groupsIntersect(id.Groups, sessionOwnerGroups)
}

func groupsIntersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, g := range a {
		if g != "" {
			set[g] = struct{}{}
		}
	}
	for _, g := range b {
		if _, ok := set[g]; ok {
			return true
		}
	}
	return false
}

func ExtractSessionID(path string, idIndex int) string {
	fragments := strings.Split(path, "/")
	if len(fragments) <= idIndex {
		return ""
	}
	return fragments[idIndex]
}

func WriteForbidden(w http.ResponseWriter) {
	http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
}
