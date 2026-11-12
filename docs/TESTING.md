# NiuNexus 测试记录

## 测试环境

- fnOS 1.2.1010，内核 6.18.18.c1186-trim，x86_64 虚拟机
- 两个网口：`eth0`（WAN，DHCP 172.21.245.132/20）、`eth1`（测试网口）
- NetworkManager 1.42.4，nftables 1.0.6，iproute2 6.1.0
- 安装版本：0.1.3（`install_type=root`，数据卷 `/vol1`）

## 已验证项

### 安装与生命周期

- [x] `fnpack build` 生成 FPK，应用中心安装成功。
- [x] 统一网关注册：`/app/niunexus` → `/var/apps/niunexus/target/app.sock`，`isAdminOnly=true`。
- [x] 未登录访问网关返回 `invalid token`，管理员登录后可打开 UI。
- [x] `niunexus-core` 以 root 运行，`niunexus-web` 以 `niunexus` 用户运行。
- [x] App Center 停止/启动：进程、PID、socket 正确清理与重建。
- [x] 卸载 0.1.1：`ip_forward` 恢复 0、代理监听关闭、`niunexus-*` NM 连接删除。
- [x] 卸载后重新安装，应用配置（代理/NAT）保留并由启动重放恢复。

### 接口管理

- [x] 通过 UI 将 `eth1` 配置为 `192.168.5.1/24`，NM 中生成 `niunexus-eth1`，原 `Wired connection 2` 保留。
- [x] 接口变更成功写入内核地址与路由。
- [x] 高风险接口变更的确认/回滚框架由单元测试覆盖；设备上完成了成功应用与回滚清理验证。

### 静态路由与策略路由

- [x] 添加/删除静态路由成功；删除后内核路由清理。
- [x] 添加策略路由进入待确认状态；点击“保持变更”后确认成功。
- [x] 删除策略路由进入待确认状态并确认成功；100-199 表规则无残留。
- [x] 非法路由（缺少网关/出口）被后端校验拒绝并自动回滚。

### 端口转发 / NAT

- [x] 创建 `TestWeb`：TCP、WAN `eth0`、外部 18080 → `192.168.50.1:9000`。
- [x] 从 172.21 网络独立工作站访问 `http://172.21.245.132:18080/`，返回内部服务页面，DNAT 生效。
- [x] 规则计数器从 0 增加到 2 包 / 104 B。
- [x] 启用 NAT（eth0）后 `ip_forward=1`；关闭后恢复 0。
- [x] 删除规则后列表清空，NAT 设置保持正确。
- [x] 端口转发规则变更失败时自动回滚（单元测试与设备错误路径验证）。

### 网桥 / VLAN / Bond

- [x] 空网桥 `br-test`：NM `niunexus-br-test`，MTU 1500，STP off。
- [x] 带成员网桥 `br-lan`：成员 `eth1`，IPv4 `192.168.50.1/24`，MTU 1500。
  - `eth1` 成为 bridge port，state forwarding；
  - `niunexus-br-lan` 与 `niunexus-br-lan-port-eth1` 生成；
  - 原 `Wired connection 2` 保留；
  - 删除时因带 IP 进入待确认，确认后内核接口、NM 连接、路由全部清理。
- [x] VLAN `eth1.100`：802.1Q id 100，NM `niunexus-eth1.100`，父接口 `eth1`；删除后清理。
- [x] Bond `bond-test`：active-backup，成员 `eth1`，miimon 100，MTU 1500；
  - `/proc/net/bonding/bond-test` 显示 active slave `eth1`；
  - `niunexus-bond-test` 与 `niunexus-bond-test-port-eth1` 生成；
  - 删除后 `eth1` 恢复普通网口。
- [x] 0.1.2 修复：虚拟链路 MTU 使用 `802-3-ethernet.mtu`，不再使用无效的 `bridge.mtu`。

### 代理

- [x] 正向代理 `127.0.0.1:18080`，无认证：
  - HTTP 代理请求返回 200；
  - HTTPS CONNECT 返回 200；
  - SOCKS5 返回 200。
- [x] 关闭代理后监听端口消失，运行状态“未运行”。
- [x] 认证、上游代理链、透明 TCP 代理由单元测试覆盖；设备上未启用透明代理（需要 LAN 客户端流量验证）。
- [x] 透明代理 nft redirect 规则在隔离 network namespace 中完成语法与加载验证。

### UI / 刷新

- [x] 0.1.3 顶部“刷新”按当前页面重新请求数据；在网桥/VLAN 页确认新增 `GET /api/v1/links` 与 `/api/v1/interfaces`。
- [x] 快照列表显示配置/密钥/nft/系统状态；审计日志显示操作者、动作、结果、耗时。
- [x] 待确认横幅、倒计时、保持变更/立即回滚、错误提示均正常。

## 未在设备上执行

- 完整重启后的开机自启：需要重启 NAS，会中断当前远程会话；App Center 的停止/启动已验证，重启项保留待用户安排。
- 透明代理的真实 LAN 客户端流量：nft 规则语法和引擎单元测试已覆盖，需要第二个客户端/网段验证。
- 多 WAN 负载均衡、DHCP/DNS 服务器、Wi-Fi：尚未实现。

## 测试后状态

- 应用版本 0.1.3，状态 running。
- 虚拟链路、端口转发规则均为空；NAT 与代理关闭。
- `ip_forward=0`，18080 无监听。
- 仅保留系统原有 NetworkManager 连接，`eth1` 为普通网口。
- 临时 HTTP 测试服务已停止。
