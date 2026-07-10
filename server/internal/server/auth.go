package server

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jeremytondo/atelier-code/internal/api"
)

const attachTokenSubprotocolPrefix = "atc.token."

// withAuth guards next with Atelier Code's transport-aware authentication. Requests
// arriving on the owner-only Unix socket are always trusted. Requests on the
// TCP listener must present the configured bearer token. When no token is
// configured the guard is a no-op seam, so local TCP use keeps working until an
// operator opts in by setting one.
func withAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if lc, ok := ListenerFromContext(r.Context()); ok && lc.Kind == ListenerUnix {
			next.ServeHTTP(w, r)
			return
		}
		if tokenMatches(r, token) {
			next.ServeHTTP(w, r)
			return
		}
		if protocol, ok := attachSubprotocolToken(r, token); ok {
			next.ServeHTTP(w, r.WithContext(api.WithAcceptedAttachSubprotocol(r.Context(), protocol)))
			return
		}
		writeUnauthorized(w)
	})
}

// attachSubprotocolToken authenticates browser WebSocket attach handshakes.
// It is deliberately scoped to the id-based attach route. withAuth wraps the
// handler before the /api prefix is stripped, so the path is matched with its
// prefix intact.
func attachSubprotocolToken(r *http.Request, token string) (string, bool) {
	if r.Method != http.MethodGet || !isAttachPath(r.URL.Path) {
		return "", false
	}
	for _, presented := range websocketSubprotocols(r.Header.Values("Sec-WebSocket-Protocol")) {
		if attachSubprotocolMatchesToken(presented, token) {
			return presented, true
		}
	}
	return "", false
}

func attachSubprotocolForToken(token string) string {
	return attachTokenSubprotocolPrefix + base64.RawURLEncoding.EncodeToString([]byte(token))
}

func attachSubprotocolMatchesToken(presented, token string) bool {
	if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
		return true
	}
	if !strings.HasPrefix(presented, attachTokenSubprotocolPrefix) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(presented, attachTokenSubprotocolPrefix))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(decoded, []byte(token)) == 1
}

func websocketSubprotocols(values []string) []string {
	var protocols []string
	for _, value := range values {
		for _, protocol := range strings.Split(value, ",") {
			protocol = strings.TrimSpace(protocol)
			if protocol != "" {
				protocols = append(protocols, protocol)
			}
		}
	}
	return protocols
}

func isAttachPath(path string) bool {
	const prefix = "/api/sessions/"
	const suffix = "/attach"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	if !strings.HasSuffix(rest, suffix) {
		return false
	}
	id := strings.TrimSuffix(rest, suffix)
	return id != "" && !strings.Contains(id, "/")
}

// tokenMatches reports whether the request carries the expected bearer token,
// compared in constant time to avoid leaking it through timing.
func tokenMatches(r *http.Request, token string) bool {
	presented := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "unauthorized",
		"message": "missing or invalid API token",
	})
}
