package protect

import (
	"net/http"
	"strings"
)

// SessionOwnership returns whether the given identity is allowed to act on
// a session whose recorded owner is sessionOwner. Admins may always act;
// other users are restricted to sessions they themselves created.
func SessionOwnership(id Identity, sessionOwner string) bool {
	if id.IsAdmin {
		return true
	}
	return sessionOwner != "" && sessionOwner == id.User
}

// ExtractSessionID returns the path fragment at idIndex when the URL path
// is split on "/". Returns the empty string when the path is shorter than
// expected — callers should treat that as "no session" and let downstream
// handlers render their normal 404 response.
func ExtractSessionID(path string, idIndex int) string {
	fragments := strings.Split(path, "/")
	if len(fragments) <= idIndex {
		return ""
	}
	return fragments[idIndex]
}

// WriteForbidden sends a 403 with a short body. Provided so callers don't
// have to reach into net/http for a one-line denial.
func WriteForbidden(w http.ResponseWriter) {
	http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
}
