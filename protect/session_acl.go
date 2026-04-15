package protect

import (
	"net/http"
	"strings"
)

func SessionOwnership(id Identity, sessionOwner string) bool {
	if id.IsAdmin {
		return true
	}
	return sessionOwner != "" && sessionOwner == id.User
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
