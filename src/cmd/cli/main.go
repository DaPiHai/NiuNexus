package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"niunexus/internal/snapshot"
	"niunexus/internal/version"
)

type appPaths struct {
	AppRoot   string
	ConfigDir string
	VarDir    string
	AppSocket string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("niunexus %s (build %s)\n", version.Version, version.BuildTime)
	case "status":
		status()
	case "rollback":
		rollback(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func status() {
	paths := resolvePaths()
	coreSocket := filepath.Join(paths.VarDir, "core.sock")
	result := map[string]any{
		"app":        "niunexus",
		"version":    version.Version,
		"appRoot":    paths.AppRoot,
		"configDir":  paths.ConfigDir,
		"varDir":     paths.VarDir,
		"coreSocket": checkSocket(coreSocket),
		"appSocket":  checkSocket(paths.AppSocket),
		"corePid":    pidState(filepath.Join(paths.VarDir, "core.pid")),
		"webPid":     pidState(filepath.Join(paths.VarDir, "web.pid")),
	}

	manager := snapshot.New(paths.ConfigDir, paths.VarDir)
	if list, err := manager.List(); err == nil {
		result["snapshots"] = len(list)
		if len(list) > 0 {
			result["latestSnapshot"] = list[0]
		}
	} else {
		result["snapshotError"] = err.Error()
	}

	payload, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(payload))
}

func rollback(args []string) {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "错误: rollback 需要 root 权限（请通过 SSH 以 root 执行）")
		os.Exit(1)
	}

	last := false
	noRestart := false
	for _, arg := range args {
		switch arg {
		case "--last":
			last = true
		case "--no-restart":
			noRestart = true
		default:
			fmt.Fprintf(os.Stderr, "未知参数: %s\n", arg)
			os.Exit(2)
		}
	}
	if !last {
		fmt.Fprintln(os.Stderr, "用法: niunexus-cli rollback --last [--no-restart]")
		os.Exit(2)
	}

	paths := resolvePaths()
	manager := snapshot.New(paths.ConfigDir, paths.VarDir)
	if _, err := manager.Latest(); err != nil {
		fmt.Fprintf(os.Stderr, "无法回滚: %v\n", err)
		os.Exit(3)
	}

	wasRunning := appRunning(paths)
	if wasRunning {
		fmt.Fprintln(os.Stderr, "停止 NiuNexus 以安全恢复快照…")
		if err := runMain(paths, "stop"); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 停止应用失败: %v\n", err)
		}
	}

	report, restoreErr := manager.RestoreLast()
	if restoreErr != nil {
		fmt.Fprintf(os.Stderr, "回滚失败: %v\n", restoreErr)
	} else {
		payload, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(payload))
	}

	restarted := false
	if wasRunning && !noRestart {
		fmt.Fprintln(os.Stderr, "重新启动 NiuNexus…")
		if err := runMain(paths, "start"); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 重新启动失败，请手动执行 %s start: %v\n", filepath.Join(paths.AppRoot, "cmd", "main"), err)
		} else {
			restarted = true
		}
	}
	if restoreErr != nil {
		os.Exit(1)
	}
	if wasRunning && !noRestart && !restarted {
		os.Exit(1)
	}
}

func appRunning(paths appPaths) bool {
	if alivePid(filepath.Join(paths.VarDir, "core.pid")) && alivePid(filepath.Join(paths.VarDir, "web.pid")) {
		return true
	}
	return false
}

func runMain(paths appPaths, action string) error {
	script := filepath.Join(paths.AppRoot, "cmd", "main")
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return fmt.Errorf("找不到生命周期脚本: %s", script)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, script, action)
	cmd.Env = append(os.Environ(),
		"TRIM_APPDEST="+filepath.Join(paths.AppRoot, "target"),
		"TRIM_PKGETC="+paths.ConfigDir,
		"TRIM_PKGVAR="+paths.VarDir,
		"TRIM_USERNAME=niunexus",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func resolvePaths() appPaths {
	root := strings.TrimSpace(os.Getenv("NIUNEXUS_APP_ROOT"))
	if root == "" {
		if exe, err := os.Executable(); err == nil {
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				exe = real
			}
			target := filepath.Dir(filepath.Dir(exe))
			if filepath.Base(target) == "target" {
				root = filepath.Dir(target)
			}
		}
	}
	if root == "" {
		root = "/var/apps/niunexus"
	}

	configDir := firstEnv("NIUNEXUS_CONFIG_DIR", "TRIM_PKGETC")
	if configDir == "" {
		configDir = filepath.Join(root, "etc")
	}
	varDir := firstEnv("NIUNEXUS_VAR_DIR", "TRIM_PKGVAR")
	if varDir == "" {
		varDir = filepath.Join(root, "var")
	}
	appSocket := firstEnv("NIUNEXUS_APP_SOCKET", "")
	if appSocket == "" {
		appSocket = filepath.Join(root, "target", "app.sock")
	}
	return appPaths{AppRoot: root, ConfigDir: configDir, VarDir: varDir, AppSocket: appSocket}
}

func checkSocket(path string) map[string]any {
	info := map[string]any{"path": path, "exists": false, "reachable": false}
	if st, err := os.Stat(path); err == nil {
		info["exists"] = true
		info["mode"] = st.Mode().String()
	}
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		info["reachable"] = true
	}
	return info
}

func pidState(path string) map[string]any {
	info := map[string]any{"path": path, "exists": false, "alive": false}
	data, err := os.ReadFile(path)
	if err != nil {
		return info
	}
	info["exists"] = true
	pid := strings.TrimSpace(string(data))
	info["pid"] = pid
	if pid != "" {
		info["alive"] = alivePid(path)
	}
	return info
}

func alivePid(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid := atoi(strings.TrimSpace(string(data)))
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func atoi(value string) int {
	result := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0
		}
		result = result*10 + int(ch-'0')
	}
	return result
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func usage() {
	fmt.Fprintf(os.Stderr, `NiuNexus CLI %s

用法:
  niunexus-cli status                    查看进程、socket 与快照状态
  niunexus-cli version                   显示版本
  niunexus-cli rollback --last           停止应用、恢复上一次快照并重启（root）
  niunexus-cli rollback --last --no-restart  恢复后不自动启动（root）
`, version.Version)
}
