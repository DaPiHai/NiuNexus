package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"golang.org/x/sys/unix"
)

type contextKey int

const peerUIDKey contextKey = iota

// User is the identity forwarded by the fnOS gateway. It is trusted only
// because the gateway rewrites these headers before forwarding a request.
type User struct {
	UID      string `json:"uid"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
}

// UserFromRequest extracts the gateway identity from request headers.
func UserFromRequest(r *http.Request) User {
	return User{
		UID:      strings.TrimSpace(r.Header.Get("X-Trim-Userid")),
		Username: strings.TrimSpace(r.Header.Get("X-Trim-Username")),
		IsAdmin:  strings.EqualFold(r.Header.Get("X-Trim-Isadmin"), "true"),
	}
}

// PeerUID reads SO_PEERCRED from a Unix connection.
func PeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix connection")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			sockErr = err
			return
		}
		uid = cred.Uid
	}); err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	return uid, nil
}

// WithPeerUID stores the peer UID in a request context.
func WithPeerUID(ctx context.Context, uid uint32) context.Context {
	return context.WithValue(ctx, peerUIDKey, uid)
}

// PeerUIDFromContext returns the peer UID stored by ConnContext.
func PeerUIDFromContext(ctx context.Context) (uint32, bool) {
	uid, ok := ctx.Value(peerUIDKey).(uint32)
	return uid, ok
}

// RequirePeerUID allows the expected application user and root. Root is
// needed for the recovery CLI.
func RequirePeerUID(expected uint32) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := PeerUIDFromContext(r.Context())
			if !ok {
				http.Error(w, "无法确认调用方身份", http.StatusForbidden)
				return
			}
			if uid == 0 {
				next.ServeHTTP(w, r)
				return
			}
			if expected != 0 && uid == expected {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "调用方无权访问 core", http.StatusForbidden)
		})
	}
}

// Bearer enforces a shared random token. Comparison is constant time.
func Bearer(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := strings.TrimSpace(r.Header.Get("Authorization"))
			got := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "无效的 core 访问令牌", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AdminOnly rejects users that the gateway did not mark as administrators.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !UserFromRequest(r).IsAdmin {
			http.Error(w, "需要飞牛管理员身份", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF validates a double-submit header for mutating requests.
func RequireCSRF(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			got := strings.TrimSpace(r.Header.Get("X-Niunexus-CSRF"))
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "CSRF 校验失败，请刷新页面重试", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
