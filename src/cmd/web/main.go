package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"niunexus/internal/apiutil"
	"niunexus/internal/auth"
	"niunexus/internal/rpc"
	"niunexus/internal/uds"
	"niunexus/internal/version"
	"niunexus/internal/webui"
)

type webApp struct {
	core    *uds.Client
	csrf    string
	prefix  string
	dev     bool
	started time.Time
}

func main() {
	var (
		socket = flag.String("socket", env("NIUNEXUS_APP_SOCKET", ""), "gateway Unix socket to listen on")
		listen = flag.String("listen", env("NIUNEXUS_DEV_LISTEN", ""), "optional TCP listen address (development only)")
		core   = flag.String("core", env("NIUNEXUS_CORE_SOCKET", ""), "core Unix socket")
		token  = flag.String("token", env("NIUNEXUS_RPC_TOKEN", ""), "shared core RPC token")
		prefix = flag.String("prefix", env("NIUNEXUS_GATEWAY_PREFIX", "/app/niunexus"), "gateway path prefix")
		dev    = flag.Bool("dev", env("NIUNEXUS_DEV", "0") == "1", "development mode: skip gateway header checks")
	)
	flag.Parse()

	if *core == "" {
		fatal("-core or NIUNEXUS_CORE_SOCKET is required")
	}
	if *token == "" {
		fatal("-token or NIUNEXUS_RPC_TOKEN is required")
	}
	if *socket == "" && *listen == "" {
		fatal("either -socket or -listen is required")
	}

	app := &webApp{
		core:    uds.New(*core, *token),
		csrf:    randomToken(),
		prefix:  strings.TrimRight(*prefix, "/"),
		dev:     *dev,
		started: time.Now(),
	}

	server := &http.Server{
		Handler:           app.securityHeaders(app.withCSRF(app.logging(app.router()))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    128 << 10,
	}

	listeners := make([]net.Listener, 0, 2)
	if *socket != "" {
		if err := os.MkdirAll(filepath.Dir(*socket), 0o750); err != nil {
			fatal("create gateway socket directory: %v", err)
		}
		if conn, err := net.DialTimeout("unix", *socket, 300*time.Millisecond); err == nil {
			_ = conn.Close()
			fatal("another niunexus-web is already listening on %s", *socket)
		}
		_ = os.Remove(*socket)
		ln, err := net.Listen("unix", *socket)
		if err != nil {
			fatal("listen on %s: %v", *socket, err)
		}
		if err := os.Chmod(*socket, 0o660); err != nil {
			log.Printf("warning: chmod gateway socket: %v", err)
		}
		listeners = append(listeners, ln)
		log.Printf("niunexus-web listening on unix:%s", *socket)
	}
	if *listen != "" {
		if !*dev {
			log.Printf("warning: TCP listener is enabled outside development mode")
		}
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			fatal("listen on %s: %v", *listen, err)
		}
		listeners = append(listeners, ln)
		log.Printf("niunexus-web development listener on http://%s/", *listen)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	shutdownDone := make(chan struct{})
	go func() {
		sig := <-stop
		log.Printf("received %s, shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		close(shutdownDone)
	}()

	errCh := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func(listener net.Listener) {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}(ln)
	}

	select {
	case err := <-errCh:
		if *socket != "" {
			_ = os.Remove(*socket)
		}
		fatal("serve: %v", err)
	case <-shutdownDone:
	}
	if *socket != "" {
		_ = os.Remove(*socket)
	}
	log.Printf("niunexus-web stopped")
}

func (a *webApp) router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/session", a.handleSession)
	mux.HandleFunc("/api/v1/status", a.requireAdmin(a.handleStatus))
	mux.HandleFunc("/api/v1/overview", a.requireAdmin(a.handleOverview))
	mux.HandleFunc("/api/v1/capabilities", a.requireAdmin(a.handleCapabilities))
	mux.HandleFunc("/api/v1/config", a.requireAdmin(a.handleConfig))
	mux.HandleFunc("/api/v1/config/settings", a.requireAdmin(a.handleSettings))
	mux.HandleFunc("/api/v1/audit", a.requireAdmin(a.handleAudit))
	mux.HandleFunc("/api/v1/snapshots", a.requireAdmin(a.handleSnapshots))
	mux.HandleFunc("/api/v1/interfaces", a.requireAdmin(a.handleInterfaces))
	mux.HandleFunc("/api/v1/interfaces/", a.requireAdmin(a.handleInterfaceAction))
	mux.HandleFunc("/api/v1/links", a.requireAdmin(a.handleLinks))
	mux.HandleFunc("/api/v1/links/", a.requireAdmin(a.handleLinkAction))
	mux.HandleFunc("/api/v1/changes/pending", a.requireAdmin(a.handlePendingChange))
	mux.HandleFunc("/api/v1/changes/", a.requireAdmin(a.handleChangeAction))
	mux.HandleFunc("/api/v1/port-forwards", a.requireAdmin(a.handlePortForwards))
	mux.HandleFunc("/api/v1/port-forwards/", a.requireAdmin(a.handlePortForwardAction))
	mux.HandleFunc("/api/v1/nat", a.requireAdmin(a.handleNAT))
	mux.HandleFunc("/api/v1/routes", a.requireAdmin(a.handleRoutes))
	mux.HandleFunc("/api/v1/routes/", a.requireAdmin(a.handleRouteAction))
	mux.HandleFunc("/api/v1/policy-rules", a.requireAdmin(a.handlePolicyRules))
	mux.HandleFunc("/api/v1/policy-rules/", a.requireAdmin(a.handlePolicyAction))
	mux.HandleFunc("/api/v1/proxy", a.requireAdmin(a.handleProxy))
	mux.HandleFunc("/api/v1/proxy/restart", a.requireAdmin(a.handleProxyRestart))
	mux.HandleFunc("/api/v1/events", a.requireAdmin(a.handleEvents))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		apiutil.Error(w, http.StatusNotFound, "接口不存在")
	})

	sub, err := fs.Sub(webui.FS, "assets")
	if err != nil {
		fatal("load embedded UI: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(r.URL.Path, "/vendor/"):
			w.Header().Set("Cache-Control", "public, max-age=86400")
		default:
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		fileServer.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if a.prefix != "" {
			if path == a.prefix {
				target := a.prefix + "/"
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
				return
			}
			if strings.HasPrefix(path, a.prefix+"/") {
				r.URL.Path = strings.TrimPrefix(path, a.prefix)
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *webApp) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	user := auth.UserFromRequest(r)
	if a.dev {
		user = auth.User{UID: "dev", Username: "dev", IsAdmin: true}
	}
	csrf := ""
	if user.IsAdmin {
		csrf = a.csrf
	}
	apiutil.JSON(w, http.StatusOK, map[string]any{
		"app":     "niunexus",
		"version": version.Version,
		"user":    user,
		"csrf":    csrf,
		"dev":     a.dev,
	})
}

func (a *webApp) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var health rpc.Health
	coreStatus := map[string]any{"status": "ok"}
	if err := a.core.GetJSON(ctx, "/healthz", &health); err != nil {
		coreStatus = map[string]any{"status": "unavailable", "error": err.Error()}
	} else {
		coreStatus = map[string]any{
			"status":    health.Status,
			"version":   health.Version,
			"buildTime": health.BuildTime,
			"pid":       health.PID,
			"uptimeSec": health.UptimeSec,
		}
	}

	apiutil.JSON(w, http.StatusOK, map[string]any{
		"app":     "niunexus",
		"version": version.Version,
		"web": map[string]any{
			"status":    "ok",
			"pid":       os.Getpid(),
			"uptimeSec": int64(time.Since(a.started).Seconds()),
		},
		"core": coreStatus,
		"user": auth.UserFromRequest(r),
		"dev":  a.dev,
	})
}

func (a *webApp) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var overview rpc.Overview
	if err := a.core.GetJSON(ctx, "/rpc/overview", &overview); err != nil {
		apiutil.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, overview)
}

func (a *webApp) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var caps rpc.Capabilities
	if err := a.core.GetJSON(ctx, "/rpc/capabilities", &caps); err != nil {
		apiutil.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, caps)
}

func (a *webApp) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	a.proxyCore(w, r, http.MethodGet, "/rpc/config", nil)
}

func (a *webApp) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		apiutil.MethodNotAllowed(w, http.MethodPatch)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		apiutil.Error(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	if len(body) > 1<<20 {
		apiutil.Error(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return
	}
	a.proxyCore(w, r, http.MethodPatch, "/rpc/config/settings", body)
}

func (a *webApp) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	path := "/rpc/audit"
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	a.proxyCore(w, r, http.MethodGet, path, nil)
}

func (a *webApp) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/snapshots", nil)
	case http.MethodPost:
		a.proxyCore(w, r, http.MethodPost, "/rpc/snapshots", nil)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *webApp) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	a.proxyCore(w, r, http.MethodGet, "/rpc/interfaces", nil)
}

func (a *webApp) handleInterfaceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiutil.MethodNotAllowed(w, http.MethodPost)
		return
	}
	body, err := readBody(r)
	if err != nil {
		apiutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	corePath := "/rpc" + strings.TrimPrefix(r.URL.Path, "/api/v1")
	a.proxyCore(w, r, http.MethodPost, corePath, body)
}

func (a *webApp) handleLinks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/links/virtual", nil)
	case http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPost, "/rpc/links/virtual", body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *webApp) handleLinkAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiutil.MethodNotAllowed(w, http.MethodDelete)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/links/")
	if id == "" || strings.Contains(id, "/") {
		apiutil.Error(w, http.StatusBadRequest, "缺少虚拟链路 ID")
		return
	}
	a.proxyCore(w, r, http.MethodDelete, "/rpc/links/virtual/"+id, nil)
}

func (a *webApp) handlePendingChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	a.proxyCore(w, r, http.MethodGet, "/rpc/changes/pending", nil)
}

func (a *webApp) handleChangeAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiutil.MethodNotAllowed(w, http.MethodPost)
		return
	}
	corePath := "/rpc" + strings.TrimPrefix(r.URL.Path, "/api/v1")
	a.proxyCore(w, r, http.MethodPost, corePath, nil)
}

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 1<<20 {
		return nil, errBodyTooLarge
	}
	return body, nil
}

var errBodyTooLarge = errors.New("请求体过大")

func (a *webApp) handlePortForwards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/port-forwards", nil)
	case http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPost, "/rpc/port-forwards", body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *webApp) handlePortForwardAction(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/port-forwards/")
	if suffix == "" || strings.Contains(suffix, "//") {
		apiutil.Error(w, http.StatusBadRequest, "缺少端口转发 ID")
		return
	}
	corePath := "/rpc/port-forwards/" + suffix
	switch {
	case r.Method == http.MethodPut:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPut, corePath, body)
	case r.Method == http.MethodDelete:
		a.proxyCore(w, r, http.MethodDelete, corePath, nil)
	case r.Method == http.MethodPost && strings.HasSuffix(suffix, "/toggle"):
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPost, corePath, body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodPut, http.MethodDelete, http.MethodPost)
	}
}

func (a *webApp) handleNAT(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/nat", nil)
	case http.MethodPut:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPut, "/rpc/nat", body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (a *webApp) handleRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/routes", nil)
	case http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPost, "/rpc/routes", body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *webApp) handleRouteAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiutil.MethodNotAllowed(w, http.MethodDelete)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/routes/")
	if id == "" || strings.Contains(id, "/") {
		apiutil.Error(w, http.StatusBadRequest, "缺少路由 ID")
		return
	}
	a.proxyCore(w, r, http.MethodDelete, "/rpc/routes/"+id, nil)
}

func (a *webApp) handlePolicyRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/policy-rules", nil)
	case http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPost, "/rpc/policy-rules", body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *webApp) handlePolicyAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiutil.MethodNotAllowed(w, http.MethodDelete)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/policy-rules/")
	if id == "" || strings.Contains(id, "/") {
		apiutil.Error(w, http.StatusBadRequest, "缺少策略路由 ID")
		return
	}
	a.proxyCore(w, r, http.MethodDelete, "/rpc/policy-rules/"+id, nil)
}

func (a *webApp) handleProxy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.proxyCore(w, r, http.MethodGet, "/rpc/proxy", nil)
	case http.MethodPut:
		body, err := readBody(r)
		if err != nil {
			apiutil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		a.proxyCore(w, r, http.MethodPut, "/rpc/proxy", body)
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (a *webApp) handleProxyRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiutil.MethodNotAllowed(w, http.MethodPost)
		return
	}
	a.proxyCore(w, r, http.MethodPost, "/rpc/proxy/restart", nil)
}

func (a *webApp) proxyCore(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
	user := auth.UserFromRequest(r)
	if a.dev {
		user = auth.User{UID: "dev", Username: "dev", IsAdmin: true}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status, payload, err := a.core.Raw(ctx, method, path, body, map[string]string{
		"X-Niunexus-User": user.Username,
		"X-Niunexus-UID":  user.UID,
	})
	if err != nil {
		apiutil.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func (a *webApp) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Live WebSocket/SSE streaming lands in Phase 1. The dashboard currently
	// polls /api/v1/overview, which keeps this milestone dependency-free.
	apiutil.Error(w, http.StatusNotImplemented, "实时事件通道将在下一阶段提供")
}

func (a *webApp) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.dev && !auth.UserFromRequest(r).IsAdmin {
			apiutil.Error(w, http.StatusForbidden, "需要飞牛管理员身份")
			return
		}
		next(w, r)
	}
}

func (a *webApp) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if got := strings.TrimSpace(r.Header.Get("X-Niunexus-CSRF")); got != a.csrf {
				apiutil.Error(w, http.StatusForbidden, "CSRF 校验失败，请刷新页面重试")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *webApp) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (a *webApp) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		user := auth.UserFromRequest(r)
		log.Printf("%s %s status=%d user=%q admin=%t duration=%s", r.Method, r.URL.Path, rec.status, user.Username, user.IsAdmin, time.Since(start).Round(time.Millisecond))
	})
}

func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fatal("generate CSRF token: %v", err)
	}
	return hex.EncodeToString(buf)
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func fatal(format string, args ...any) {
	log.Printf("fatal: "+format, args...)
	fmt.Fprintln(os.Stderr, "niunexus-web:", fmt.Sprintf(format, args...))
	os.Exit(1)
}
