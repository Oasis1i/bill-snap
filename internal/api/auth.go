package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="bill-snap"`)
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		next(w, r)
	}
}

// authorized accepts Authorization: Bearer <token> or, when that header is
// absent, X-API-Token. A malformed Authorization header does not fall through.
func (s *Server) authorized(r *http.Request) bool {
	return secretsEqual(tokenFrom(r), s.token)
}

func tokenFrom(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h != "" {
		const prefix = "bearer "
		if len(h) >= len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
			return strings.TrimSpace(h[len(prefix):])
		}
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-API-Token"))
}

func secretsEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "err", v, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
