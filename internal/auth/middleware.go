package auth

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xeonliu/TboxWebdav/internal/config"
)

// Context key constants used to pass auth results to downstream handlers.
const (
	ContextKeyUserToken  = "userToken"
	ContextKeyJaCookie   = "jaCookie"
	ContextKeyAccessMode = "accessMode"
)

// Middleware returns an HTTP handler that enforces the configured authentication mode.
// On success it injects the resolved credentials into the request context.
func Middleware(cfg *config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// None mode: use pre-configured credentials directly.
		if cfg.AuthMode == config.AuthModeNone {
			if cfg.UserToken != "" {
				ctx := context.WithValue(r.Context(), ContextKeyUserToken, cfg.UserToken)
				ctx = context.WithValue(ctx, ContextKeyAccessMode, cfg.AccessMode)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if cfg.Cookie != "" {
				ctx := context.WithValue(r.Context(), ContextKeyJaCookie, cfg.Cookie)
				ctx = context.WithValue(ctx, ContextKeyAccessMode, cfg.AccessMode)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			slog.Warn("auth: None mode but no credentials configured")
			sendUnauthorized(w)
			return
		}

		// All other modes require a Basic auth header.
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Basic ") {
			slog.Debug("auth: missing or non-Basic Authorization header")
			sendUnauthorized(w)
			return
		}

		encoded := strings.TrimSpace(strings.TrimPrefix(authHeader, "Basic "))
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			slog.Warn("auth: malformed Base64 in Authorization header", "error", err)
			sendUnauthorized(w)
			return
		}

		// Split only on the first colon; password may itself contain colons.
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) < 2 {
			slog.Warn("auth: Authorization header missing colon separator")
			sendUnauthorized(w)
			return
		}
		username := parts[0]
		password := parts[1]

		// UserToken mode / Mixed: accept 128-char hex strings as UserToken.
		if cfg.AuthMode == config.AuthModeUserToken || cfg.AuthMode == config.AuthModeMixed {
			if isValidUserToken(password) {
				slog.Debug("auth: accepted as UserToken", "username", username)
				ctx := withCreds(r.Context(), password, "", cfg.AccessMode)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// JaCookie mode / Mixed: accept base64-decodable strings as JaCookie.
		if cfg.AuthMode == config.AuthModeJaCookie || cfg.AuthMode == config.AuthModeMixed {
			if isValidJaCookie(password) {
				slog.Debug("auth: accepted as JaCookie", "username", username)
				ctx := withCreds(r.Context(), "", password, cfg.AccessMode)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// Custom mode / Mixed: check against the user list.
		if cfg.AuthMode == config.AuthModeCustom || cfg.AuthMode == config.AuthModeMixed {
			for _, u := range cfg.Users {
				if u.UserName == "" || u.UserName != username {
					continue
				}
				if u.Password != "" && u.Password != password {
					continue
				}
				// Matched user.
				if u.UserToken != "" {
					slog.Debug("auth: accepted custom user via UserToken", "username", username)
					ctx := withCreds(r.Context(), u.UserToken, "", u.AccessMode)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				if u.Cookie != "" {
					slog.Debug("auth: accepted custom user via JaCookie", "username", username)
					ctx := withCreds(r.Context(), "", u.Cookie, u.AccessMode)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}

		slog.Warn("auth: rejected", "username", username, "mode", cfg.AuthMode)
		sendUnauthorized(w)
	})
}

func sendUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Basic")
	w.WriteHeader(http.StatusUnauthorized)
}

// isValidUserToken returns true if the string is exactly 128 hex characters.
func isValidUserToken(s string) bool {
	if len(s) != 128 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isValidJaCookie returns true if the string is valid base64.
func isValidJaCookie(s string) bool {
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}

// withCreds returns a derived context that carries resolved credentials.
func withCreds(ctx context.Context, userToken, jaCookie string, accessMode config.AccessMode) context.Context {
	if userToken != "" {
		ctx = context.WithValue(ctx, ContextKeyUserToken, userToken)
	}
	if jaCookie != "" {
		ctx = context.WithValue(ctx, ContextKeyJaCookie, jaCookie)
	}
	return context.WithValue(ctx, ContextKeyAccessMode, accessMode)
}
