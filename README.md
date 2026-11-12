# NiuNexus

NiuNexus 是一个飞牛 fnOS 原生网络管理应用。它使用 NetworkManager、nftables 和 Linux 路由栈管理主机网络，并通过 fnOS 统一网关提供 Web 管理界面。

当前版本：**0.1.3**。已在 fnOS 1.2.1010 x86_64 上完成安装、网关注册、接口/网桥/VLAN/Bond、端口转发与 DNAT、NAT、代理、确认回滚和清理验证；详见 [docs/TESTING.md](docs/TESTING.md)。

## 功能

- **概览**：网口、地址、路由、DNS、链路速率与流量统计，主机能力检测。
- **接口**：IPv4/IPv6 DHCP/静态地址、网关、DNS、MTU；通过克隆 NetworkManager 连接实现，原配置保留可恢复。
- **网桥 / VLAN / 链路聚合**：创建/删除 NiuNexus 自有的 NetworkManager 连接，支持 STP、VLAN ID、Bond 模式。
- **端口转发 / NAT**：TCP/UDP、端口范围、hairpin、命名计数器；规则写入独立的 `inet niunexus` 表。
- **静态路由 / 策略路由**：主路由表静态路由，100-199 独立路由表 + `ip rule` 多网口选路。
- **代理**：HTTP CONNECT + SOCKS5 同一端口、可选认证、上游 HTTP/SOCKS5 代理链、Bypass、TCP 透明代理。
- **安全**：配置快照、确认超时自动回滚、启动时恢复未确认变更、审计日志、服务端管理员鉴权、CSRF、非 root Web 进程。

## 架构

```text
浏览器
  │  /app/niunexus
  ▼
fnOS 统一网关（校验登录态，注入 X-Trim-* Header）
  │  Unix Socket: /var/apps/niunexus/target/app.sock
  ▼
niunexus-web（应用包用户，非 root）
  │  私有 UDS + 随机 token
  ▼
niunexus-core（root，唯一特权进程）
  ├─ NetworkManager（nmcli）  接口 / 网桥 / VLAN / Bond
  ├─ nftables（nft）          端口转发 / NAT / 透明代理重定向
  ├─ iproute2（ip）           静态路由 / 策略路由
  ├─ 内置代理引擎              HTTP CONNECT / SOCKS5 / 透明 TCP
  └─ 快照 / 回滚 / 审计 / 看门狗
```

- `niunexus-core` 只监听 `$TRIM_PKGVAR/core.sock`，通过 `SO_PEERCRED` + 随机 token 校验调用方。
- `niunexus-web` 永远以 `$TRIM_USERNAME` 运行，不读取网络配置、不执行特权命令。
- 所有网络变更写入 `TRIM_PKGETC/config.json`（0600），代理密码在 `secrets.json`（0600）。
- 所有高风险变更先创建快照；超时未确认或应用重启会自动回滚。

## 构建

开发机需要：

- Linux x86_64
- 项目内 Go 工具链（`.tools/go/bin/go`），或系统 `go`
- `fnpack`（fnOS 应用打包工具）
- `openssl`、`tar`、`curl`

```bash
./scripts/build.sh
```

产物：`niunexus.fpk`。脚本会：

1. 生成 `ICON.PNG` / `ICON_256.PNG` 和入口图标；
2. 静态编译 `niunexus-core`、`niunexus-web`、`niunexus-cli` 到 `niunexus/app/server/`；
3. 调用 `fnpack build --directory niunexus`。

ARM64 交叉编译：

```bash
GOARCH=arm64 ./scripts/build.sh
```

同时需要把 `niunexus/manifest` 的 `platform` 改为 `arm`。

## 安装

1. 在飞牛 App Center 中手动安装 `niunexus.fpk`；
2. 或在设备终端执行：

```bash
sudo appcenter-cli install-fpk /path/to/niunexus.fpk
```

安装后从应用卡片打开 `/app/niunexus`。应用入口仅管理员可见，所有 API 也要求 `X-Trim-Isadmin: true`。

## 使用与安全模型

- **快照**：每次变更前保存在 `TRIM_PKGETC/backups/snapshots/`，最多保留 10 份。
- **待确认变更**：接口地址、默认路由、策略路由、透明代理等高风险变更新增后，页面顶部显示倒计时；必须在超时前点击“保持变更”，否则自动回滚。
- **恢复原配置**：接口编辑弹窗中的“恢复原配置”会重新启用克隆前的 NetworkManager 连接。
- **停止应用**：端口转发、NAT、透明代理、静态/策略路由会停止；NetworkManager 连接保留。
- **卸载**：停止进程、删除自有 nft 表、自有 `niunexus-` 前缀连接、恢复 `ip_forward`、清理密码与路由状态。

### 紧急恢复

如果Web 页面无法访问但 SSH 可用：

```bash
sudo niunexus-cli status
sudo niunexus-cli rollback --last
sudo niunexus-cli rollback --last --no-restart
```

`rollback --last` 会停止应用、恢复最近快照（配置、密码、nft 表、转发 sysctl）、重新应用旧配置并重启应用。

配置文件也可直接备份/恢复：

```text
/var/apps/niunexus/etc/config.json
/var/apps/niunexus/etc/secrets.json
/var/apps/niunexus/etc/backups/
```

## 已知限制

- 端口转发当前仅支持 IPv4 目标地址。
- 代理引擎仅处理 TCP；不代理 UDP/QUIC，不做 TLS 解密，不做按域名/SNI 分流。
- 透明代理依赖 `SO_ORIGINAL_DST`，仅支持 IPv4 重定向。
- 静态路由首版写主路由表；策略路由使用 100-199 路由表。
- Wi-Fi 扫描/连接、DHCP/DNS 服务器、多 WAN 负载均衡尚未实现。
- fnOS 自带防火墙若开启，可能仍需在系统侧放行转发流量；应用会检测并提示，但不会修改系统防火墙。
- 当前测试环境为单网口 fnOS 虚拟机，多网口/聚合/透明代理的完整链路需要在实际设备上验证。

## 开发

```bash
./scripts/dev-run.sh
```

本地开发模式会在 `127.0.0.1:8081` 启动 Web 界面，并使用 `.dev/run/` 保存临时配置/日志。开发模式跳过网关 Header 校验，仅用于本机调试。

测试：

```bash
cd src
go test ./...
go vet ./...
```

## 目录

```text
niunexus/            FPK 包目录（manifest、config、cmd、app/ui、二进制）
src/cmd/core/        root 核心服务
src/cmd/web/         非 root Web/网关服务
src/cmd/cli/         诊断与紧急回滚 CLI
src/internal/        config、audit、snapshot、changes、nm、nft、routing、proxy 等
scripts/build.sh     构建与打包
scripts/dev-run.sh   本地开发运行
```
