package apiutil

import (
	"encoding/json"
	"net/http"

	"niunexus/internal/rpc"
)

// JSON writes a JSON response with no-store caching.
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if value == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

// Error writes the standard error body.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, rpc.Error{Error: message})
}

// MethodNotAllowed is a small helper for handlers with a fixed method.
func MethodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", join(allowed))
	Error(w, http.StatusMethodNotAllowed, "不支持该请求方法")
}

func join(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
