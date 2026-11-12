package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"niunexus/internal/apiutil"
	"niunexus/internal/audit"
	"niunexus/internal/auth"
	"niunexus/internal/capabilities"
	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/netctl"
	"niunexus/internal/netinfo"
	"niunexus/internal/nft"
	"niunexus/internal/nm"
	"niunexus/internal/portforward"
	"niunexus/internal/proxy"
	"niunexus/internal/routing"
	"niunexus/internal/rpc"
	"niunexus/internal/secrets"
	"niunexus/internal/snapshot"
	"niunexus/internal/sysctl"
	"niunexus/internal/version"
)

type coreApp struct {
	token       string
	expectedUID uint32
	configDir   string
	varDir      string
	startedAt   time.Time

	store     *config.Store
	auditLog  *audit.Logger
	snapshots *snapshot.Manager
	changes   *changes.Manager
	netctl    *netctl.Controller
	portfwd   *portforward.Controller
	routing   *routing.Controller
	proxy     *proxy.Controller

	capsMu sync.Mutex
	caps   rpc.Capabilities
	capsAt time.Time
}

func main() {
	var (
		socket    = flag.String("socket", env("NIUNEXUS_CORE_SOCKET", ""), "Unix socket to listen on")
		token     = flag.String("token", env("NIUNEXUS_RPC_TOKEN", ""), "shared RPC token")
		appUID    = flag.Uint("app-uid", envUint("NIUNEXUS_APP_UID"), "UID allowed to connect (0 allows root only)")
		appGID    = flag.Int("app-gid", envInt("NIUNEXUS_APP_GID"), "GID that owns the socket")
		configDir = flag.String("config-dir", env("NIUNEXUS_CONFIG_DIR", ""), "persistent configuration directory")
		varDir    = flag.String("var-dir", env("NIUNEXUS_VAR_DIR", ""), "runtime data directory")
	)
	flag.Parse()

	if *socket == "" {
		fatal("-socket or NIUNEXUS_CORE_SOCKET is required")
	}
	if *token == "" {
		fatal("-token or NIUNEXUS_RPC_TOKEN is required")
	}
	if *configDir == "" {
		*configDir = filepath.Dir(*socket)
	}
	if *varDir == "" {
		*varDir = filepath.Dir(*socket)
	}

	if err := os.MkdirAll(filepath.Dir(*socket), 0o750); err != nil {
		fatal("create socket directory: %v", err)
	}
	if err := os.MkdirAll(*varDir, 0o750); err != nil {
		fatal("create var directory: %v", err)
	}
	if err := os.MkdirAll(*configDir, 0o750); err != nil {
		fatal("create config directory: %v", err)
	}

	// Refuse to steal a live socket from another core process.
	if conn, err := net.DialTimeout("unix", *socket, 300*time.Millisecond); err == nil {
		_ = conn.Close()
		fatal("another niunexus-core is already listening on %s", *socket)
	}
	_ = os.Remove(*socket)

	listener, err := net.Listen("unix", *socket)
	if err != nil {
		fatal("listen on %s: %v", *socket, err)
	}
	if err := os.Chmod(*socket, 0o660); err != nil {
		log.Printf("warning: chmod core socket: %v", err)
	}
	if *appGID > 0 {
		if err := os.Chown(*socket, 0, *appGID); err != nil {
			log.Printf("warning: chown core socket to gid %d: %v", *appGID, err)
		}
	}

	store := config.Open(*configDir)
	auditLog, err := audit.New(filepath.Join(*configDir, "audit.log"))
	if err != nil {
		log.Printf("warning: audit log unavailable: %v", err)
		auditLog = nil
	}

	app := &coreApp{
		token:       *token,
		expectedUID: uint32(*appUID),
		configDir:   *configDir,
		varDir:      *varDir,
		startedAt:   time.Now(),
		store:       store,
		auditLog:    auditLog,
		snapshots:   snapshot.New(*configDir, *varDir),
	}

	changeManager := changes.New(app.snapshots, store, *configDir)
	changeManager.SetAudit(func(changeID, action, target, actor, result, message string) {
		app.auditChange(changeID, action, target, actor, result, message)
	})
	netController := netctl.New(nm.New(nm.ExecRunner{Timeout: 30 * time.Second}), store, changeManager)
	netController.RegisterRecovery()
	nftClient := nft.New(nft.ExecRunner{Timeout: 30 * time.Second})
	forwarding := sysctl.New(*configDir)
	portController := portforward.New(
		nftClient,
		forwarding,
		store,
		changeManager,
	)
	portController.RegisterRecovery()
	routingController := routing.New(routing.ExecRunner{Timeout: 20 * time.Second}, store, changeManager, *configDir)
	routingController.RegisterRecovery()
	proxyController := proxy.NewController(
		proxy.New(),
		store,
		changeManager,
		secrets.New(filepath.Join(*configDir, "secrets.json")),
		nftClient,
		forwarding,
	)
	proxyController.RegisterRecovery()
	app.changes = changeManager
	app.netctl = netController
	app.portfwd = portController
	app.routing = routingController
	app.proxy = proxyController
	if err := changeManager.Recover(context.Background()); err != nil {
		log.Printf("warning: recover pending change: %v", err)
	}
	if app.store.Config().Settings.AutoApplyOnStart {
		if err := proxyController.ApplyCurrent(context.Background()); err != nil {
			log.Printf("warning: reapply persisted proxy: %v", err)
		}
		if err := portController.ApplyCurrent(context.Background()); err != nil {
			log.Printf("warning: reapply persisted port forwards: %v", err)
		}
		if err := routingController.ApplyCurrent(context.Background()); err != nil {
			log.Printf("warning: reapply persisted routes: %v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.handleHealth)
	mux.HandleFunc("/rpc/ping", app.handlePing)
	mux.HandleFunc("/rpc/capabilities", app.handleCapabilities)
	mux.HandleFunc("/rpc/overview", app.handleOverview)
	mux.HandleFunc("/rpc/config", app.handleConfig)
	mux.HandleFunc("/rpc/config/settings", app.handleSettings)
	mux.HandleFunc("/rpc/audit", app.handleAudit)
	mux.HandleFunc("/rpc/snapshots", app.handleSnapshots)
	mux.HandleFunc("GET /rpc/interfaces", app.handleInterfaces)
	mux.HandleFunc("POST /rpc/interfaces/{name}/apply", app.handleInterfaceApply)
	mux.HandleFunc("POST /rpc/interfaces/{name}/revert", app.handleInterfaceRevert)
	mux.HandleFunc("GET /rpc/links/virtual", app.handleVirtualLinks)
	mux.HandleFunc("POST /rpc/links/virtual", app.handleVirtualLinkCreate)
	mux.HandleFunc("DELETE /rpc/links/virtual/{id}", app.handleVirtualLinkDelete)
	mux.HandleFunc("GET /rpc/changes/pending", app.handlePendingChange)
	mux.HandleFunc("POST /rpc/changes/{id}/confirm", app.handleConfirmChange)
	mux.HandleFunc("POST /rpc/changes/{id}/rollback", app.handleRollbackChange)
	mux.HandleFunc("GET /rpc/port-forwards", app.handlePortForwards)
	mux.HandleFunc("POST /rpc/port-forwards", app.handlePortForwardCreate)
	mux.HandleFunc("PUT /rpc/port-forwards/{id}", app.handlePortForwardUpdate)
	mux.HandleFunc("DELETE /rpc/port-forwards/{id}", app.handlePortForwardDelete)
	mux.HandleFunc("POST /rpc/port-forwards/{id}/toggle", app.handlePortForwardToggle)
	mux.HandleFunc("GET /rpc/nat", app.handleNAT)
	mux.HandleFunc("PUT /rpc/nat", app.handleNATUpdate)
	mux.HandleFunc("GET /rpc/routes", app.handleRoutes)
	mux.HandleFunc("POST /rpc/routes", app.handleRouteCreate)
	mux.HandleFunc("DELETE /rpc/routes/{id}", app.handleRouteDelete)
	mux.HandleFunc("GET /rpc/policy-rules", app.handlePolicyRules)
	mux.HandleFunc("POST /rpc/policy-rules", app.handlePolicyCreate)
	mux.HandleFunc("DELETE /rpc/policy-rules/{id}", app.handlePolicyDelete)
	mux.HandleFunc("GET /rpc/proxy", app.handleProxy)
	mux.HandleFunc("PUT /rpc/proxy", app.handleProxyUpdate)
	mux.HandleFunc("POST /rpc/proxy/restart", app.handleProxyRestart)

	handler := auth.Bearer(app.token)(auth.RequirePeerUID(app.expectedUID)(logging(mux)))
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if uid, err := auth.PeerUID(conn); err == nil {
				return auth.WithPeerUID(ctx, uid)
			}
			return ctx
		},
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		log.Printf("received %s, shutting down", sig)
		if app.changes != nil {
			changeCtx, changeCancel := context.WithTimeout(context.Background(), 90*time.Second)
			app.changes.Shutdown(changeCtx)
			changeCancel()
		}
		if app.proxy != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := app.proxy.Cleanup(cleanupCtx); err != nil {
				log.Printf("warning: cleanup proxy: %v", err)
			}
			cleanupCancel()
		}
		if app.portfwd != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := app.portfwd.Cleanup(cleanupCtx); err != nil {
				log.Printf("warning: cleanup port forwards: %v", err)
			}
			cleanupCancel()
		}
		if app.routing != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := app.routing.Cleanup(cleanupCtx); err != nil {
				log.Printf("warning: cleanup routes: %v", err)
			}
			cleanupCancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	log.Printf("niunexus-core %s listening on %s (expected uid=%d gid=%d)", version.Version, *socket, app.expectedUID, *appGID)
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fatal("serve: %v", err)
	}
	_ = os.Remove(*socket)
	log.Printf("niunexus-core stopped")
}

func (a *coreApp) health() rpc.Health {
	return rpc.Health{
		Status:    "ok",
		Version:   version.Version,
		BuildTime: version.BuildTime,
		PID:       os.Getpid(),
		UptimeSec: int64(time.Since(a.startedAt).Seconds()),
		Time:      time.Now(),
	}
}

func (a *coreApp) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	apiutil.JSON(w, http.StatusOK, a.health())
}

func (a *coreApp) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	apiutil.JSON(w, http.StatusOK, map[string]any{
		"pong": true,
		"time": time.Now(),
	})
}

func (a *coreApp) capabilities() rpc.Capabilities {
	a.capsMu.Lock()
	defer a.capsMu.Unlock()
	if time.Since(a.capsAt) < 30*time.Second && a.caps.Kernel != "" {
		return a.caps
	}
	a.caps = capabilities.Detect()
	a.capsAt = time.Now()
	return a.caps
}

func (a *coreApp) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	apiutil.JSON(w, http.StatusOK, a.capabilities())
}

func (a *coreApp) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}

	overview := rpc.Overview{
		Health:       a.health(),
		Hostname:     netinfo.Hostname(),
		Kernel:       readTrimmed("/proc/sys/kernel/osrelease"),
		Capabilities: a.capabilities(),
		DNS:          netinfo.DNS(),
		FetchedAt:    time.Now(),
	}
	overview.OSVersion = overview.Capabilities.OSVersion
	overview.Uptime, overview.UptimeSec = netinfo.Uptime()

	if ifaces, err := netinfo.Interfaces(); err != nil {
		overview.Warnings = append(overview.Warnings, err.Error())
	} else {
		overview.Interfaces = ifaces
	}
	if routes, err := netinfo.Routes(); err != nil {
		overview.Warnings = append(overview.Warnings, err.Error())
	} else {
		overview.Routes = routes
	}
	for _, warning := range a.store.Warnings() {
		overview.Warnings = append(overview.Warnings, "配置: "+warning)
	}

	apiutil.JSON(w, http.StatusOK, overview)
}

func (a *coreApp) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	apiutil.JSON(w, http.StatusOK, map[string]any{
		"config":   a.store.Config(),
		"warnings": a.store.Warnings(),
	})
}

func (a *coreApp) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		apiutil.MethodNotAllowed(w, http.MethodPatch)
		return
	}
	start := time.Now()
	var patch config.SettingsPatch
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		a.writeAudit(r, "settings.update", "config.settings", "error", "请求体无效: "+err.Error(), start)
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}

	cfg, err := a.store.UpdateSettings(patch)
	if err != nil {
		a.writeAudit(r, "settings.update", "config.settings", "error", err.Error(), start)
		apiutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	summary := fmt.Sprintf("safeMode=%t confirmTimeout=%ds language=%s logLevel=%s autoApply=%t",
		cfg.Settings.SafeMode, cfg.Settings.ConfirmTimeoutSec, cfg.Settings.Language, cfg.Settings.LogLevel, cfg.Settings.AutoApplyOnStart)
	a.writeAudit(r, "settings.update", "config.settings", "success", summary, start)
	apiutil.JSON(w, http.StatusOK, map[string]any{
		"config":   cfg,
		"warnings": a.store.Warnings(),
	})
}

func (a *coreApp) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiutil.MethodNotAllowed(w, http.MethodGet)
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	entries, err := a.auditLog.Read(limit)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取审计日志失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (a *coreApp) writeAudit(r *http.Request, action, target, result, message string, start time.Time) {
	entry := audit.Entry{
		Time:       time.Now(),
		User:       r.Header.Get("X-Niunexus-User"),
		UID:        r.Header.Get("X-Niunexus-UID"),
		Action:     action,
		Target:     target,
		Result:     result,
		Message:    audit.Sanitize(message),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if err := a.auditLog.Log(entry); err != nil {
		log.Printf("warning: write audit log: %v", err)
	}
}

func (a *coreApp) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := a.snapshots.List()
		if err != nil {
			apiutil.Error(w, http.StatusInternalServerError, "读取快照列表失败: "+err.Error())
			return
		}
		apiutil.JSON(w, http.StatusOK, map[string]any{"snapshots": list})
	case http.MethodPost:
		start := time.Now()
		actor := strings.TrimSpace(r.Header.Get("X-Niunexus-User"))
		if actor == "" {
			actor = "unknown"
		}
		meta, err := a.snapshots.Create("manual", actor)
		if err != nil {
			a.writeAudit(r, "snapshot.create", "snapshot", "error", err.Error(), start)
			apiutil.Error(w, http.StatusInternalServerError, "创建快照失败: "+err.Error())
			return
		}
		a.writeAudit(r, "snapshot.create", "snapshot:"+meta.ID, "success", "manual snapshot", start)
		apiutil.JSON(w, http.StatusCreated, map[string]any{"snapshot": meta})
	default:
		apiutil.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *coreApp) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	list, warnings, err := a.netctl.ListInterfaces(ctx)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取网口失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, map[string]any{
		"interfaces": list,
		"warnings":   warnings,
		"protected":  a.netctl.ProtectedInterfaces(),
	})
}

func (a *coreApp) handleInterfaceApply(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		apiutil.Error(w, http.StatusBadRequest, "缺少网口名称")
		return
	}
	var req netctl.InterfaceRequest
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.netctl.ApplyInterface(r.Context(), name, req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "interface.apply", name, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleInterfaceRevert(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		apiutil.Error(w, http.StatusBadRequest, "缺少网口名称")
		return
	}
	start := time.Now()
	result, err := a.netctl.RevertInterface(r.Context(), name, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "interface.revert", name, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleVirtualLinks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	list, warnings, err := a.netctl.ListVirtualLinks(ctx)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取虚拟链路失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, map[string]any{
		"links":    list,
		"warnings": warnings,
	})
}

func (a *coreApp) handleVirtualLinkCreate(w http.ResponseWriter, r *http.Request) {
	var req netctl.VirtualLinkRequest
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.netctl.CreateVirtualLink(r.Context(), req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "link.create", req.Name, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleVirtualLinkDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		apiutil.Error(w, http.StatusBadRequest, "缺少虚拟链路 ID")
		return
	}
	start := time.Now()
	result, err := a.netctl.DeleteVirtualLink(r.Context(), id, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "link.delete", id, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handlePendingChange(w http.ResponseWriter, r *http.Request) {
	apiutil.JSON(w, http.StatusOK, a.changes.Pending())
}

func (a *coreApp) handleConfirmChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := a.changes.Confirm(id)
	if err != nil {
		apiutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleRollbackChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := a.changes.Rollback(id, "用户请求回滚")
	if err != nil {
		apiutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) auditChange(changeID, action, target, actor, result, message string) {
	entry := audit.Entry{
		Time:     time.Now(),
		User:     actor,
		Action:   action,
		Target:   target,
		Result:   result,
		Message:  audit.Sanitize(message),
		ChangeID: changeID,
	}
	if err := a.auditLog.Log(entry); err != nil {
		log.Printf("warning: write change audit: %v", err)
	}
}

func decodeBody(r *http.Request, out any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func actorFrom(r *http.Request) string {
	actor := strings.TrimSpace(r.Header.Get("X-Niunexus-User"))
	if actor == "" {
		actor = "unknown"
	}
	return actor
}

func changeErrorStatus(err error) int {
	if strings.Contains(err.Error(), "已有待确认") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func (a *coreApp) handlePortForwards(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	snapshot, err := a.portfwd.List(ctx)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取端口转发失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, snapshot)
}

func (a *coreApp) handlePortForwardCreate(w http.ResponseWriter, r *http.Request) {
	var req portforward.Request
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.portfwd.Add(r.Context(), req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "portforward.create", req.Name, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handlePortForwardUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req portforward.Request
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.portfwd.Update(r.Context(), id, req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "portforward.update", id, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handlePortForwardDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	start := time.Now()
	result, err := a.portfwd.Delete(r.Context(), id, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "portforward.delete", id, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handlePortForwardToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req portforward.ToggleRequest
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.portfwd.Toggle(r.Context(), id, req.Enabled, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "portforward.toggle", id, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleNAT(w http.ResponseWriter, r *http.Request) {
	apiutil.JSON(w, http.StatusOK, a.store.Config().NAT)
}

func (a *coreApp) handleNATUpdate(w http.ResponseWriter, r *http.Request) {
	var nat config.NATConfig
	if err := decodeBody(r, &nat); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.portfwd.UpdateNAT(r.Context(), nat, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "nat.update", "nat", "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleRoutes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	snapshot, err := a.routing.List(ctx)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取路由失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, snapshot)
}

func (a *coreApp) handleRouteCreate(w http.ResponseWriter, r *http.Request) {
	var req routing.RouteRequest
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.routing.AddRoute(r.Context(), req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "route.create", req.Destination, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleRouteDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	start := time.Now()
	result, err := a.routing.DeleteRoute(r.Context(), id, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "route.delete", id, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handlePolicyRules(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	snapshot, err := a.routing.List(ctx)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取策略路由失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, snapshot)
}

func (a *coreApp) handlePolicyCreate(w http.ResponseWriter, r *http.Request) {
	var req routing.PolicyRequest
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.routing.AddPolicy(r.Context(), req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "policy.create", req.Source, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handlePolicyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	start := time.Now()
	result, err := a.routing.DeletePolicy(r.Context(), id, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "policy.delete", id, "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleProxy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	snapshot, err := a.proxy.Status(ctx)
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, "读取代理状态失败: "+err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, snapshot)
}

func (a *coreApp) handleProxyUpdate(w http.ResponseWriter, r *http.Request) {
	var req proxy.Request
	if err := decodeBody(r, &req); err != nil {
		apiutil.Error(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	start := time.Now()
	result, err := a.proxy.Update(r.Context(), req, actorFrom(r))
	if err != nil {
		a.writeAudit(r, "proxy.update", "proxy", "error", err.Error(), start)
		apiutil.Error(w, changeErrorStatus(err), err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, result)
}

func (a *coreApp) handleProxyRestart(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if err := a.proxy.Restart(r.Context()); err != nil {
		a.writeAudit(r, "proxy.restart", "proxy", "error", err.Error(), start)
		apiutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeAudit(r, "proxy.restart", "proxy", "success", "代理已重启", start)
	snapshot, err := a.proxy.Status(r.Context())
	if err != nil {
		apiutil.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	apiutil.JSON(w, http.StatusOK, snapshot)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		uid, _ := auth.PeerUIDFromContext(r.Context())
		log.Printf("%s %s status=%d uid=%d duration=%s", r.Method, r.URL.Path, rec.status, uid, time.Since(start).Round(time.Millisecond))
	})
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envUint(key string) uint {
	value, err := strconv.ParseUint(env(key, "0"), 10, 32)
	if err != nil {
		return 0
	}
	return uint(value)
}

func envInt(key string) int {
	value, err := strconv.Atoi(env(key, "0"))
	if err != nil {
		return 0
	}
	return value
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(trimSpace(data))
}

func trimSpace(data []byte) []byte {
	start, end := 0, len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}

func fatal(format string, args ...any) {
	log.Printf("fatal: "+format, args...)
	fmt.Fprintln(os.Stderr, "niunexus-core:", fmt.Sprintf(format, args...))
	os.Exit(1)
}
