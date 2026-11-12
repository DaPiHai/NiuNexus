import { h, render } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

const html = htm.bind(h);
let csrfToken = "";

const NAV_ITEMS = [
  { id: "overview", label: "概览", icon: "overview", desc: "设备状态、网口、路由与能力检测" },
  { id: "interfaces", label: "接口", icon: "interfaces", desc: "网口地址、链路状态与接口配置" },
  { id: "routes", label: "路由", icon: "routes", desc: "路由表、静态路由与多网口选路" },
  { id: "forward", label: "端口转发", icon: "forward", desc: "NAT、端口映射与 hairpin 回环" },
  { id: "links", label: "网桥 / VLAN", icon: "links", desc: "网桥、VLAN 与链路聚合" },
  { id: "proxy", label: "代理", icon: "proxy", desc: "HTTP/SOCKS5 正向代理与透明代理" },
  { id: "system", label: "系统", icon: "system", desc: "运行信息、能力检测与安全设置" },
];

function apiUrl(path) {
  return new URL(path, document.baseURI).toString();
}

async function api(path, options = {}) {
  const headers = {
    Accept: "application/json",
    ...(options.body ? { "Content-Type": "application/json" } : {}),
    ...(options.headers || {}),
  };
  const method = (options.method || "GET").toUpperCase();
  if (!["GET", "HEAD", "OPTIONS"].includes(method) && csrfToken) {
    headers["X-Niunexus-CSRF"] = csrfToken;
  }
  const response = await fetch(apiUrl(path), {
    ...options,
    method,
    headers,
    credentials: "same-origin",
  });
  const text = await response.text();
  let payload = null;
  try {
    payload = text ? JSON.parse(text) : null;
  } catch {
    payload = null;
  }
  if (!response.ok) {
    const message = payload?.error || text || `HTTP ${response.status}`;
    const error = new Error(message);
    error.status = response.status;
    throw error;
  }
  return payload;
}

function fmtBytes(value, digits = 1) {
  const n = Number(value || 0);
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  return `${v.toFixed(digits)} ${units[i]}`;
}

function fmtRate(value) {
  return `${fmtBytes(value)}/s`;
}

function fmtNumber(value) {
  return Number(value || 0).toLocaleString("zh-CN");
}

function fmtUptime(seconds) {
  const total = Math.max(0, Number(seconds || 0));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) return `${days} 天 ${hours} 小时`;
  if (hours > 0) return `${hours} 小时 ${minutes} 分`;
  return `${minutes} 分`;
}

function shortList(values, limit = 3) {
  const list = values || [];
  if (list.length <= limit) return list;
  return [...list.slice(0, limit), `+${list.length - limit}`];
}

function stateTone(state) {
  const value = String(state || "").toLowerCase();
  if (value === "up") return "ok";
  if (value === "down" || value === "lowerlayerdown") return "bad";
  return "warn";
}

function NavIcon({ name }) {
  const common = {
    class: "nav-icon",
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    "stroke-width": "1.7",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
    "aria-hidden": "true",
  };
  switch (name) {
    case "overview":
      return html`<svg ...${common}><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>`;
    case "interfaces":
      return html`<svg ...${common}><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M7 9h4M7 13h8M7 17h5"/><path d="M17 9v6"/></svg>`;
    case "routes":
      return html`<svg ...${common}><circle cx="5" cy="6" r="2"/><circle cx="19" cy="6" r="2"/><circle cx="12" cy="18" r="2"/><path d="M7 6h10M6 8l5 8M18 8l-5 8"/></svg>`;
    case "forward":
      return html`<svg ...${common}><path d="M4 7h10a4 4 0 0 1 0 8H8"/><path d="m7 11 3 4-3 4"/><path d="M17 3l3 4-3 4"/></svg>`;
    case "links":
      return html`<svg ...${common}><path d="M8 12h8"/><rect x="3" y="8" width="5" height="8" rx="1.5"/><rect x="16" y="8" width="5" height="8" rx="1.5"/><path d="M5.5 4v4M18.5 4v4M5.5 16v4M18.5 16v4"/></svg>`;
    case "proxy":
      return html`<svg ...${common}><path d="M12 3l7 3v5c0 4.4-3 8.3-7 10-4-1.7-7-5.6-7-10V6l7-3z"/><path d="M9.5 12l1.8 1.8 3.4-3.6"/></svg>`;
    case "system":
      return html`<svg ...${common}><circle cx="12" cy="12" r="3.2"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1 1.55V21h-4v-.08a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.55-1H3v-4h.08a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-1.55V3h4v.08a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.55 1H21v4h-.08a1.7 1.7 0 0 0-1.52 1z"/></svg>`;
    default:
      return html`<svg ...${common}><circle cx="12" cy="12" r="9"/></svg>`;
  }
}

function Badge({ tone = "", children }) {
  return html`<span class=${`badge ${tone}`.trim()}>${children}</span>`;
}

function StatusPill({ ok, label }) {
  return html`<span class="status-pill"><span class=${`dot ${ok ? "ok" : "bad"}`}></span>${label}</span>`;
}

function Card({ title, sub, children, class: className = "", tight = false }) {
  return html`
    <section class=${`card ${tight ? "tight" : ""} ${className}`.trim()}>
      ${(title || sub) && html`
        <div class="card-head">
          <div>
            ${title && html`<div class="card-title">${title}</div>`}
            ${sub && html`<div class="card-sub">${sub}</div>`}
          </div>
        </div>
      `}
      ${children}
    </section>
  `;
}

function KV({ rows }) {
  return html`
    <div class="kv">
      ${rows.map(
        (row) => html`
          <div class="kv-row">
            <span class="kv-key">${row.key}</span>
            <span class="kv-val">${row.value}</span>
          </div>
        `,
      )}
    </div>
  `;
}

function ErrorBanner({ message }) {
  if (!message) return null;
  return html`
    <div class="error-banner">
      <span class="banner-icon">!</span>
      <div>${message}</div>
    </div>
  `;
}

function WarningBanner({ warnings }) {
  if (!warnings || warnings.length === 0) return null;
  return html`
    <div class="warn-banner">
      <span class="banner-icon">!</span>
      <div>
        ${warnings.map((warning) => html`<div>${warning}</div>`)}
      </div>
    </div>
  `;
}

function LoadingCards() {
  return html`
    <div class="grid cards">
      ${[0, 1, 2, 3].map(
        (i) => html`
          <div class="card" key=${i}>
            <div class="skeleton" style="width: 38%; height: 11px; margin-bottom: 14px"></div>
            <div class="skeleton" style="width: 68%; height: 26px; margin-bottom: 12px"></div>
            <div class="skeleton" style="width: 86%; height: 11px; margin-bottom: 7px"></div>
            <div class="skeleton" style="width: 62%; height: 11px"></div>
          </div>
        `,
      )}
    </div>
  `;
}

function InterfaceCard({ iface, rate }) {
  const tone = stateTone(iface.state);
  const ipv4 = shortList(iface.ipv4, 3);
  const ipv6 = shortList(iface.ipv6, 2);
  const none = !iface.ipv4?.length && !iface.ipv6?.length;
  return html`
    <article class="card iface-card">
      <div class="iface-head">
        <div>
          <div class="iface-name">
            <span class=${`dot ${tone}`}></span>
            ${iface.name}
            <${Badge} tone=${tone}>${iface.state}</${Badge}>
          </div>
          <div class="iface-meta">
            <${Badge}>${iface.type}</${Badge}>
            ${iface.isPhysical && html`<${Badge} tone="accent">物理</${Badge}>`}
            ${iface.master && html`<${Badge} tone="warn">master: ${iface.master}</${Badge}>`}
          </div>
        </div>
        <div class="muted-2 mono">${iface.mtu} MTU</div>
      </div>

      <div class="iface-addrs">
        ${none && html`<span class="muted-2">未配置 IP 地址</span>`}
        ${ipv4.map(
          (addr) => html`<div class="addr-line" key=${`4-${addr}`}><span class="addr-tag">V4</span><span>${addr}</span></div>`,
        )}
        ${ipv6.map(
          (addr) => html`<div class="addr-line" key=${`6-${addr}`}><span class="addr-tag">V6</span><span>${addr}</span></div>`,
        )}
      </div>

      <div class="iface-stats">
        <div>
          <div class="rate">↓ ${fmtRate(rate?.rx || 0)}</div>
          <div class="rate-total">累计 ${fmtBytes(iface.rxBytes)} · ${fmtNumber(iface.rxPackets)} 包</div>
        </div>
        <div>
          <div class="rate">↑ ${fmtRate(rate?.tx || 0)}</div>
          <div class="rate-total">累计 ${fmtBytes(iface.txBytes)} · ${fmtNumber(iface.txPackets)} 包</div>
        </div>
      </div>

      <${KV}
        rows=${[
          { key: "MAC", value: html`<span class="mono">${iface.mac || "—"}</span>` },
          {
            key: "速率",
            value: iface.speedMbps ? `${iface.speedMbps} Mbps${iface.duplex ? ` · ${iface.duplex}` : ""}` : "—",
          },
          { key: "错误", value: `${fmtNumber((iface.rxErrors || 0) + (iface.txErrors || 0))}` },
        ]}
      />
    </article>
  `;
}

function InterfaceCards({ ifaces, rates }) {
  const list = ifaces || [];
  if (list.length === 0) {
    return html`<div class="placeholder"><h3>没有发现网口</h3><p>请检查系统网络设备或稍后重试。</p></div>`;
  }
  return html`
    <div class="grid ifaces">
      ${list.map((iface) => html`<${InterfaceCard} key=${iface.name} iface=${iface} rate=${rates[iface.name]} />`)}
    </div>
  `;
}

function RoutesTable({ routes, limit = 30 }) {
  const list = routes || [];
  const rows = list.slice(0, limit);
  return html`
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>族</th>
            <th>目标</th>
            <th>网关</th>
            <th>设备</th>
            <th>表</th>
            <th>Metric</th>
            <th>协议</th>
            <th>Scope</th>
          </tr>
        </thead>
        <tbody>
          ${rows.map(
            (route, index) => html`
              <tr key=${`${route.family}-${route.destination}-${route.table}-${index}`}>
                <td><${Badge}>${route.family === "ipv6" ? "V6" : "V4"}</${Badge}></td>
                <td class="mono">${route.destination}</td>
                <td class="mono">${route.gateway || "—"}</td>
                <td class="mono">${route.device || "—"}</td>
                <td class="mono">${route.table}</td>
                <td>${route.metric}</td>
                <td>${route.protocol || "—"}</td>
                <td class="muted">${route.scope || "—"}</td>
              </tr>
            `,
          )}
        </tbody>
      </table>
    </div>
    ${list.length > limit && html`<div class="muted-2" style="margin-top: 8px">显示前 ${limit} 条，共 ${list.length} 条路由</div>`}
  `;
}

function OverviewPage({ data, rates, loading }) {
  if (!data) {
    return html`
      ${loading ? html`<${LoadingCards} />` : html`<div class="placeholder"><h3>暂无网络数据</h3><p>Core 可能尚未启动，或暂时无法读取系统网络状态。</p></div>`}
    `;
  }

  const caps = data.capabilities || {};
  const tools = Object.fromEntries((caps.tools || []).map((tool) => [tool.name, tool]));
  const defaultV4 = (data.routes || []).find((route) => route.family === "ipv4" && route.destination === "default");
  const defaultV6 = (data.routes || []).find((route) => route.family === "ipv6" && route.destination === "default");
  const coreOk = data.health?.status === "ok";

  return html`
    <${WarningBanner} warnings=${data.warnings} />

    <div class="grid cards">
      <${Card} title="设备">
        <div class="stat-value">${data.hostname || "unknown"}</div>
        <div class="stat-label">fnOS ${data.osVersion || "unknown"} · 内核 ${data.kernel || "unknown"}</div>
        <div style="height: 12px"></div>
        <${KV}
          rows=${[
            { key: "运行时长", value: data.uptime || fmtUptime(data.uptimeSec) },
            { key: "网口", value: `${data.interfaces?.length || 0} 个` },
            { key: "路由", value: `${data.routes?.length || 0} 条` },
          ]}
        />
      </${Card}>

      <${Card} title="Core 服务">
        <div class="stat-value small">
          <span class=${`dot ${coreOk ? "ok" : "bad"}`} style="display:inline-block; margin-right:8px"></span>
          ${coreOk ? "运行中" : "不可用"}
        </div>
        <div class="stat-label">特权网络操作由 core 执行，Web 进程不接触 root 命令。</div>
        <div style="height: 12px"></div>
        <${KV}
          rows=${[
            { key: "版本", value: data.health?.version || "—" },
            { key: "PID", value: data.health?.pid || "—" },
            { key: "运行时长", value: fmtUptime(data.health?.uptimeSec) },
          ]}
        />
      </${Card}>

      <${Card} title="默认出口">
        <div class="stat-value mono">${defaultV4?.gateway || "无默认网关"}</div>
        <div class="stat-label">${defaultV4 ? `经由 ${defaultV4.device || "未知设备"}` : "未发现 IPv4 默认路由"}</div>
        <div style="height: 12px"></div>
        <${KV}
          rows=${[
            { key: "IPv6 出口", value: defaultV6?.gateway || "—" },
            { key: "DNS", value: (data.dns || []).join(", ") || "—" },
            { key: "IPv4 转发", value: caps.ipForward ? html`<${Badge} tone="ok">已开启</${Badge}>` : html`<${Badge}>关闭</${Badge}>` },
          ]}
        />
      </${Card}>

      <${Card} title="能力与安全">
        <div class="stat-value small">
          ${caps.firewallEnabled ? html`<${Badge} tone="warn">fnOS 防火墙已开启</${Badge}>` : html`<${Badge} tone="ok">fnOS 防火墙关闭</${Badge}>`}
        </div>
        <div class="stat-label">${caps.firewallDetail || "未读取到防火墙状态"}</div>
        <div style="height: 12px"></div>
        <div class="iface-meta">
          <${Badge} tone=${tools.nft?.available ? "ok" : "bad"}>nft</${Badge}>
          <${Badge} tone=${tools.nmcli?.available ? "ok" : "bad"}>nmcli</${Badge}>
          <${Badge} tone=${tools.ip?.available ? "ok" : "bad"}>ip</${Badge}>
          <${Badge} tone=${tools.dnsmasq?.available ? "ok" : "warn"}>dnsmasq</${Badge}>
        </div>
        <div class="muted-2" style="margin-top: 10px; font-size: 11px">
          ${caps.root ? "Core 以 root 运行，可修改主机网络。" : "Core 当前不是 root，写操作将不可用。"}
        </div>
      </${Card}>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">网口</div>
        <div class="muted-2">每 3 秒刷新 · 统计来自内核计数器</div>
      </div>
      <${InterfaceCards} ifaces=${data.interfaces} rates=${rates} />
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">路由表</div>
        <div class="muted-2">共 ${data.routes?.length || 0} 条</div>
      </div>
      <${RoutesTable} routes=${data.routes} />
    </div>
  `;
}

function Modal({ title, children, onClose, wide = false }) {
  useEffect(() => {
    const handler = (event) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [onClose]);

  return html`
    <div
      class="modal-backdrop"
      onClick=${(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div class=${`modal ${wide ? "wide" : ""}`} role="dialog" aria-modal="true">
        <div class="modal-head">
          <div class="modal-title">${title}</div>
          <button type="button" class="icon-btn" onClick=${onClose} aria-label="关闭">×</button>
        </div>
        <div class="modal-body">${children}</div>
      </div>
    </div>
  `;
}

function PendingBanner({ pending, now, busy, onConfirm, onRollback }) {
  if (!pending) return null;
  const deadline = pending.rollbackDeadline ? new Date(pending.rollbackDeadline).getTime() : 0;
  const remaining = deadline ? Math.max(0, Math.ceil((deadline - now) / 1000)) : 0;
  return html`
    <div class="pending-banner">
      <div class="pending-main">
        <div class="pending-title">
          <span class="dot warn"></span>
          待确认变更：${pending.action} → ${pending.target}
        </div>
        <div class="pending-desc">
          ${remaining > 0
            ? `变更已应用，请在 ${remaining} 秒内确认并保留；超时将自动回滚。`
            : "确认时间已到，正在自动回滚…"}
        </div>
        <div class="pending-hint">未确认的变更会在超时或应用重启后自动撤销。</div>
      </div>
      <div class="pending-actions">
        <button class="btn" type="button" onClick=${() => onRollback(pending.changeId)} disabled=${busy || remaining === 0}>
          立即回滚
        </button>
        <button class="btn btn-primary" type="button" onClick=${() => onConfirm(pending.changeId)} disabled=${busy || remaining === 0}>
          保持变更
        </button>
      </div>
    </div>
  `;
}

function InterfaceEditor({ iface, busy, onClose, onSubmit, onRevert }) {
  const managed = iface.managed || {};
  const firstV4 = managed.ipv4Address || iface.ipv4?.[0] || "";
  const firstV6 = managed.ipv6Address || iface.ipv6?.[0] || "";
  const [draft, setDraft] = useState({
    ipv4Mode: managed.ipv4Mode || (iface.ipv4?.length ? "manual" : "auto"),
    ipv4Address: firstV4,
    ipv4Gateway: managed.ipv4Gateway || "",
    dns: (managed.dns || []).join(", "),
    ipv6Mode: managed.ipv6Mode || "auto",
    ipv6Address: firstV6,
    ipv6Gateway: managed.ipv6Gateway || "",
    ipv6Dns: (managed.ipv6Dns || []).join(", "),
    mtu: managed.mtu || iface.mtu || 1500,
  });
  const [ack, setAck] = useState(!iface.protected);

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const splitList = (value) =>
    String(value || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);

  const submit = (event) => {
    event.preventDefault();
    onSubmit(iface.name, {
      ipv4Mode: draft.ipv4Mode,
      ipv4Address: draft.ipv4Mode === "manual" ? draft.ipv4Address : "",
      ipv4Gateway: draft.ipv4Mode === "manual" ? draft.ipv4Gateway : "",
      dns: draft.ipv4Mode === "manual" ? splitList(draft.dns) : [],
      ipv6Mode: draft.ipv6Mode,
      ipv6Address: draft.ipv6Mode === "manual" ? draft.ipv6Address : "",
      ipv6Gateway: draft.ipv6Mode === "manual" ? draft.ipv6Gateway : "",
      ipv6Dns: draft.ipv6Mode === "manual" ? splitList(draft.ipv6Dns) : [],
      mtu: Number(draft.mtu) || 0,
    });
  };

  return html`
    <${Modal} title=${`配置网口 ${iface.name}`} onClose=${onClose} wide=${true}>
      <form class="settings-form" onSubmit=${submit}>
        ${iface.protected &&
        html`
          <div class="warn-banner" style="margin-bottom: 0">
            <span class="banner-icon">!</span>
            <div>
              ${iface.name} 是当前管理网口或默认路由网口。修改地址可能导致当前页面断开；
              变更需要在倒计时内确认，否则自动恢复原连接。
            </div>
          </div>
        `}

        <div class="form-grid">
          <label class="field">
            <span>IPv4 模式</span>
            <select value=${draft.ipv4Mode} onChange=${(event) => update("ipv4Mode", event.target.value)}>
              <option value="auto">DHCP（自动）</option>
              <option value="manual">静态地址</option>
              <option value="disabled">禁用 IPv4</option>
            </select>
          </label>
          ${draft.ipv4Mode === "manual" &&
          html`
            <label class="field">
              <span>IPv4 地址（CIDR）</span>
              <input
                value=${draft.ipv4Address}
                onInput=${(event) => update("ipv4Address", event.target.value)}
                placeholder="192.168.1.10/24"
              />
            </label>
            <label class="field">
              <span>IPv4 网关</span>
              <input
                value=${draft.ipv4Gateway}
                onInput=${(event) => update("ipv4Gateway", event.target.value)}
                placeholder="192.168.1.1"
              />
            </label>
            <label class="field">
              <span>DNS（逗号分隔）</span>
              <input
                value=${draft.dns}
                onInput=${(event) => update("dns", event.target.value)}
                placeholder="223.5.5.5, 1.1.1.1"
              />
            </label>
          `}
          <label class="field">
            <span>IPv6 模式</span>
            <select value=${draft.ipv6Mode} onChange=${(event) => update("ipv6Mode", event.target.value)}>
              <option value="auto">自动</option>
              <option value="manual">静态地址</option>
              <option value="disabled">禁用</option>
              <option value="ignore">忽略</option>
            </select>
          </label>
          ${draft.ipv6Mode === "manual" &&
          html`
            <label class="field">
              <span>IPv6 地址（CIDR）</span>
              <input
                value=${draft.ipv6Address}
                onInput=${(event) => update("ipv6Address", event.target.value)}
                placeholder="fd00::10/64"
              />
            </label>
            <label class="field">
              <span>IPv6 网关</span>
              <input
                value=${draft.ipv6Gateway}
                onInput=${(event) => update("ipv6Gateway", event.target.value)}
                placeholder="fd00::1"
              />
            </label>
            <label class="field">
              <span>IPv6 DNS（逗号分隔）</span>
              <input
                value=${draft.ipv6Dns}
                onInput=${(event) => update("ipv6Dns", event.target.value)}
                placeholder="2400:3200::1"
              />
            </label>
          `}
          <label class="field">
            <span>MTU</span>
            <input
              type="number"
              min="576"
              max="9000"
              value=${draft.mtu}
              onInput=${(event) => update("mtu", event.target.value)}
            />
          </label>
        </div>

        ${iface.protected &&
        html`
          <label class="switch-row">
            <input type="checkbox" checked=${ack} onChange=${(event) => setAck(event.target.checked)} />
            <span>
              <b>我理解当前连接可能短暂中断</b>
              <small>变更应用后需要在倒计时内回到本页面确认。</small>
            </span>
          </label>
        `}

        <div class="modal-actions">
          ${iface.managed &&
          html`
            <button class="btn" type="button" onClick=${() => onRevert(iface.name)} disabled=${busy}>
              恢复原配置
            </button>
          `}
          <span style="flex: 1"></span>
          <button class="btn" type="button" onClick=${onClose} disabled=${busy}>取消</button>
          <button class="btn btn-primary" type="submit" disabled=${busy || (iface.protected && !ack)}>
            ${busy ? "应用中…" : "应用配置"}
          </button>
        </div>
      </form>
    </${Modal}>
  `;
}

function InterfacesPage({ items, warnings, busy, pending, onEdit }) {
  const ifaces = items || [];
  return html`
    <${WarningBanner} warnings=${warnings} />
    <div class="warn-banner">
      <span class="banner-icon">i</span>
      <div>
        网口配置通过 NetworkManager 克隆连接实现，原配置会保留以便恢复。带 IP 的网口受安全模式保护，
        高风险变更必须在倒计时内确认，否则自动回滚。虚拟链路成员请在“网桥 / VLAN”页面管理。
      </div>
    </div>
    ${ifaces.length === 0
      ? html`<div class="placeholder"><h3>没有发现网口</h3><p>请检查系统网络设备或稍后重试。</p></div>`
      : html`
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>名称</th>
                  <th>类型</th>
                  <th>状态</th>
                  <th>IPv4</th>
                  <th>IPv6</th>
                  <th>MAC</th>
                  <th>MTU</th>
                  <th>管理状态</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                ${ifaces.map((iface) => {
                  const isVirtual = ["bridge", "vlan", "bond"].includes(iface.type) || iface.type === "loopback";
                  return html`
                    <tr key=${iface.name}>
                      <td class="mono">
                        ${iface.name}
                        ${iface.defaultRoute && html`<${Badge} tone="accent">默认路由</${Badge}>`}
                      </td>
                      <td><${Badge}>${iface.type}</${Badge}></td>
                      <td>
                        <span class=${`dot ${stateTone(iface.state)}`} style="display:inline-block;margin-right:6px"></span>
                        ${iface.state}
                      </td>
                      <td class="mono">${iface.ipv4?.join(", ") || "—"}</td>
                      <td class="mono">${iface.ipv6?.join(", ") || "—"}</td>
                      <td class="mono">${iface.mac || "—"}</td>
                      <td>${iface.mtu}</td>
                      <td>
                        ${iface.protected && html`<${Badge} tone="warn">受保护</${Badge}>`}
                        ${iface.managed && html`<${Badge} tone="ok">NiuNexus 管理</${Badge}>`}
                        ${iface.activeConnection && html`<${Badge}>${iface.activeConnection.name}</${Badge}>`}
                      </td>
                      <td>
                        ${isVirtual
                          ? html`<span class="muted-2">在虚拟链路页管理</span>`
                          : html`
                              <button
                                class="btn btn-ghost"
                                type="button"
                                onClick=${() => onEdit(iface)}
                                disabled=${busy || !!pending}
                              >
                                配置
                              </button>
                            `}
                      </td>
                    </tr>
                  `;
                })}
              </tbody>
            </table>
          </div>
        `}
  `;
}

function LinkEditor({ interfaces, busy, onClose, onSubmit }) {
  const physical = (interfaces || []).filter((item) => item.type === "device" && item.name !== "lo");
  const [draft, setDraft] = useState({
    type: "bridge",
    name: "br-lan",
    parent: physical[0]?.name || "",
    vlanId: 100,
    members: [],
    stp: false,
    mode: "active-backup",
    ipv4Mode: "disabled",
    ipv4Address: "",
    ipv4Gateway: "",
    dns: "",
    ipv6Mode: "ignore",
    ipv6Address: "",
    ipv6Gateway: "",
    ipv6Dns: "",
    mtu: 1500,
  });

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const splitList = (value) =>
    String(value || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
  const toggleMember = (name) => {
    setDraft((current) => ({
      ...current,
      members: current.members.includes(name)
        ? current.members.filter((item) => item !== name)
        : [...current.members, name],
    }));
  };

  const isVlan = draft.type === "vlan";
  const isBridge = draft.type === "bridge";
  const isBond = draft.type === "bond";
  const computedVlanName = draft.parent && draft.vlanId ? `${draft.parent}.${draft.vlanId}` : "";

  const submit = (event) => {
    event.preventDefault();
    const name = isVlan ? computedVlanName : draft.name.trim();
    onSubmit({
      type: draft.type,
      name,
      parent: isVlan ? draft.parent : "",
      members: isVlan ? [] : draft.members,
      vlanId: isVlan ? Number(draft.vlanId) : 0,
      stp: isBridge ? draft.stp : false,
      mode: isBond ? draft.mode : "",
      ipv4Mode: draft.ipv4Mode,
      ipv4Address: draft.ipv4Mode === "manual" ? draft.ipv4Address : "",
      ipv4Gateway: draft.ipv4Mode === "manual" ? draft.ipv4Gateway : "",
      dns: draft.ipv4Mode === "manual" ? splitList(draft.dns) : [],
      ipv6Mode: draft.ipv6Mode,
      ipv6Address: draft.ipv6Mode === "manual" ? draft.ipv6Address : "",
      ipv6Gateway: draft.ipv6Mode === "manual" ? draft.ipv6Gateway : "",
      ipv6Dns: draft.ipv6Mode === "manual" ? splitList(draft.ipv6Dns) : [],
      mtu: Number(draft.mtu) || 0,
    });
  };

  return html`
    <${Modal} title="新建虚拟链路" onClose=${onClose} wide=${true}>
      <form class="settings-form" onSubmit=${submit}>
        <div class="warn-banner" style="margin-bottom: 0">
          <span class="banner-icon">!</span>
          <div>
            将带 IP 的网口加入网桥或聚合会短暂中断该网口的连接。此类变更会要求二次确认，
            未在倒计时内确认将自动回滚。账号下只能有一个待确认变更。
          </div>
        </div>

        <div class="form-grid">
          <label class="field">
            <span>类型</span>
            <select value=${draft.type} onChange=${(event) => update("type", event.target.value)}>
              <option value="bridge">网桥</option>
              <option value="vlan">VLAN</option>
              <option value="bond">链路聚合</option>
            </select>
          </label>

          ${isVlan
            ? html`
                <label class="field">
                  <span>父接口</span>
                  <select value=${draft.parent} onChange=${(event) => update("parent", event.target.value)}>
                    ${physical.map((item) => html`<option value=${item.name}>${item.name}</option>`)}
                  </select>
                </label>
                <label class="field">
                  <span>VLAN ID</span>
                  <input
                    type="number"
                    min="1"
                    max="4094"
                    value=${draft.vlanId}
                    onInput=${(event) => update("vlanId", event.target.value)}
                  />
                </label>
                <label class="field">
                  <span>接口名</span>
                  <input value=${computedVlanName} readOnly=${true} />
                </label>
              `
            : html`
                <label class="field">
                  <span>接口名</span>
                  <input
                    value=${draft.name}
                    onInput=${(event) => update("name", event.target.value)}
                    placeholder=${isBridge ? "br-lan" : "bond0"}
                  />
                </label>
              `}

          ${isBridge &&
          html`
            <label class="switch-row">
              <input type="checkbox" checked=${draft.stp} onChange=${(event) => update("stp", event.target.checked)} />
              <span>
                <b>启用 STP</b>
                <small>局域网只有一个网桥时通常可以关闭。</small>
              </span>
            </label>
          `}

          ${isBond &&
          html`
            <label class="field">
              <span>Bond 模式</span>
              <select value=${draft.mode} onChange=${(event) => update("mode", event.target.value)}>
                <option value="active-backup">active-backup</option>
                <option value="802.3ad">802.3ad (LACP)</option>
                <option value="balance-rr">balance-rr</option>
                <option value="balance-xor">balance-xor</option>
                <option value="broadcast">broadcast</option>
                <option value="balance-tlb">balance-tlb</option>
                <option value="balance-alb">balance-alb</option>
              </select>
            </label>
          `}

          <label class="field">
            <span>MTU</span>
            <input
              type="number"
              min="576"
              max="9000"
              value=${draft.mtu}
              onInput=${(event) => update("mtu", event.target.value)}
            />
          </label>
        </div>

        ${!isVlan &&
        html`
          <div class="field">
            <span>成员网口</span>
            <div class="member-grid">
              ${physical.map(
                (item) => html`
                  <label class="member-row" key=${item.name}>
                    <input
                      type="checkbox"
                      checked=${draft.members.includes(item.name)}
                      onChange=${() => toggleMember(item.name)}
                    />
                    <span class="mono">${item.name}</span>
                    ${item.protected && html`<${Badge} tone="warn">带 IP / 受保护</${Badge}>`}
                    <span class="muted-2">${item.ipv4?.join(", ") || "无地址"}</span>
                  </label>
                `,
              )}
            </div>
          </div>
        `}

        <div class="form-grid">
          <label class="field">
            <span>链路 IPv4 模式</span>
            <select value=${draft.ipv4Mode} onChange=${(event) => update("ipv4Mode", event.target.value)}>
              <option value="disabled">禁用</option>
              <option value="auto">DHCP</option>
              <option value="manual">静态地址</option>
            </select>
          </label>
          ${draft.ipv4Mode === "manual" &&
          html`
            <label class="field">
              <span>IPv4 地址（CIDR）</span>
              <input
                value=${draft.ipv4Address}
                onInput=${(event) => update("ipv4Address", event.target.value)}
                placeholder="192.168.50.1/24"
              />
            </label>
            <label class="field">
              <span>IPv4 网关</span>
              <input
                value=${draft.ipv4Gateway}
                onInput=${(event) => update("ipv4Gateway", event.target.value)}
                placeholder="可留空"
              />
            </label>
            <label class="field">
              <span>DNS（逗号分隔）</span>
              <input
                value=${draft.dns}
                onInput=${(event) => update("dns", event.target.value)}
                placeholder="223.5.5.5"
              />
            </label>
          `}
          <label class="field">
            <span>链路 IPv6 模式</span>
            <select value=${draft.ipv6Mode} onChange=${(event) => update("ipv6Mode", event.target.value)}>
              <option value="ignore">忽略</option>
              <option value="auto">自动</option>
              <option value="manual">静态地址</option>
              <option value="disabled">禁用</option>
            </select>
          </label>
          ${draft.ipv6Mode === "manual" &&
          html`
            <label class="field">
              <span>IPv6 地址（CIDR）</span>
              <input
                value=${draft.ipv6Address}
                onInput=${(event) => update("ipv6Address", event.target.value)}
                placeholder="fd00:50::1/64"
              />
            </label>
          `}
        </div>

        <div class="modal-actions">
          <span style="flex: 1"></span>
          <button class="btn" type="button" onClick=${onClose} disabled=${busy}>取消</button>
          <button class="btn btn-primary" type="submit" disabled=${busy}>
            ${busy ? "创建中…" : "创建"}
          </button>
        </div>
      </form>
    </${Modal}>
  `;
}

function LinksPage({ links, warnings, interfaces, busy, pending, onCreate, onDelete }) {
  const list = links || [];
  return html`
    <${WarningBanner} warnings=${warnings} />
    <div class="section" style="margin-top: 0">
      <div class="section-head">
        <div class="section-title">虚拟链路</div>
        <button class="btn btn-primary" type="button" onClick=${onCreate} disabled=${busy || !!pending}>
          新建虚拟链路
        </button>
      </div>
      ${list.length === 0
        ? html`
            <div class="placeholder">
              <h3>还没有虚拟链路</h3>
              <p>可以创建网桥、VLAN 子接口或链路聚合。创建时可以选择是否同时配置 IP。</p>
            </div>
          `
        : html`
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>名称</th>
                    <th>类型</th>
                    <th>成员</th>
                    <th>IPv4</th>
                    <th>状态</th>
                    <th>保护</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  ${list.map((link) => html`
                    <tr key=${link.id}>
                      <td class="mono">${link.name}</td>
                      <td><${Badge}>${link.type}</${Badge}></td>
                      <td class="mono">${(link.members || []).join(", ") || "—"}</td>
                      <td class="mono">${link.ipv4Address || link.ipv4Mode || "—"}</td>
                      <td>
                        <${Badge} tone=${link.present ? "ok" : "bad"}>${link.present ? "存在" : "缺失"}</${Badge}>
                        ${link.active && html`<${Badge} tone="accent">活动</${Badge}>`}
                      </td>
                      <td>${link.protected ? html`<${Badge} tone="warn">受保护</${Badge}>` : html`<span class="muted-2">—</span>`}</td>
                      <td>
                        <button class="btn btn-ghost" type="button" onClick=${() => onDelete(link)} disabled=${busy || !!pending}>
                          删除
                        </button>
                      </td>
                    </tr>
                  `)}
                </tbody>
              </table>
            </div>
          `}
    </div>
  `;
}

function PortForwardEditor({ rule, interfaces, busy, onClose, onSubmit }) {
  const [draft, setDraft] = useState({
    name: rule?.name || "",
    enabled: rule?.enabled ?? true,
    protocol: rule?.protocol || "tcp",
    wan: rule?.wan || "",
    externalPort: rule?.externalPort || "",
    destIp: rule?.destIp || "",
    destPort: rule?.destPort || "",
    hairpin: rule?.hairpin || false,
  });
  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const submit = (event) => {
    event.preventDefault();
    onSubmit(draft, rule?.id || null);
  };
  const candidates = (interfaces || []).filter((item) => item.type !== "loopback");

  return html`
    <${Modal} title=${rule ? "编辑端口转发" : "添加端口转发"} onClose=${onClose}>
      <form class="settings-form" onSubmit=${submit}>
        <div class="form-grid">
          <label class="field">
            <span>名称</span>
            <input value=${draft.name} onInput=${(event) => update("name", event.target.value)} placeholder="例如 Web 服务" />
          </label>
          <label class="field">
            <span>协议</span>
            <select value=${draft.protocol} onChange=${(event) => update("protocol", event.target.value)}>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
              <option value="both">TCP + UDP</option>
            </select>
          </label>
          <label class="field">
            <span>WAN 网口</span>
            <select value=${draft.wan} onChange=${(event) => update("wan", event.target.value)}>
              <option value="">任意网口</option>
              ${candidates.map((item) => html`<option value=${item.name}>${item.name}</option>`)}
            </select>
          </label>
          <label class="field">
            <span>外部端口 / 范围</span>
            <input
              value=${draft.externalPort}
              onInput=${(event) => update("externalPort", event.target.value)}
              placeholder="8080 或 8000-8010"
            />
          </label>
          <label class="field">
            <span>内部 IP</span>
            <input
              value=${draft.destIp}
              onInput=${(event) => update("destIp", event.target.value)}
              placeholder="192.168.1.10"
            />
          </label>
          <label class="field">
            <span>内部端口 / 范围</span>
            <input
              value=${draft.destPort}
              onInput=${(event) => update("destPort", event.target.value)}
              placeholder="80"
            />
          </label>
        </div>

        <label class="switch-row">
          <input
            type="checkbox"
            checked=${draft.hairpin}
            onChange=${(event) => update("hairpin", event.target.checked)}
          />
          <span>
            <b>Hairpin NAT</b>
            <small>允许局域网设备通过 WAN IP 访问这条转发。</small>
          </span>
        </label>
        <label class="switch-row">
          <input
            type="checkbox"
            checked=${draft.enabled}
            onChange=${(event) => update("enabled", event.target.checked)}
          />
          <span>
            <b>启用规则</b>
            <small>保存后立即写入独立的 nft 表。</small>
          </span>
        </label>

        <div class="modal-actions">
          <span style="flex: 1"></span>
          <button class="btn" type="button" onClick=${onClose} disabled=${busy}>取消</button>
          <button class="btn btn-primary" type="submit" disabled=${busy}>
            ${busy ? "保存中…" : rule ? "保存" : "添加"}
          </button>
        </div>
      </form>
    </${Modal}>
  `;
}

function NATCard({ nat, interfaces, busy, pending, onSave }) {
  const [enabled, setEnabled] = useState(!!nat?.enabled);
  const [selected, setSelected] = useState(nat?.interfaces || []);

  useEffect(() => {
    setEnabled(!!nat?.enabled);
    setSelected(nat?.interfaces || []);
  }, [nat]);

  const candidates = (interfaces || []).filter(
    (item) => ["device", "bridge", "vlan", "bond"].includes(item.type) && item.name !== "lo",
  );
  const toggle = (name) =>
    setSelected((current) =>
      current.includes(name) ? current.filter((item) => item !== name) : [...current, name],
    );

  return html`
    <${Card} title="NAT / 路由转发">
      <form class="settings-form" onSubmit=${(event) => {
        event.preventDefault();
        onSave({ enabled, interfaces: selected });
      }}>
        <div class="warn-banner" style="margin-bottom: 0">
          <span class="banner-icon">i</span>
          <div>
            启用 NAT 会开启 IPv4 转发并写入 masquerade 规则；停止应用时会恢复原转发设置。
            fnOS 自带防火墙若开启，可能仍需在系统侧放行转发流量。
          </div>
        </div>
        <label class="switch-row">
          <input type="checkbox" checked=${enabled} onChange=${(event) => setEnabled(event.target.checked)} />
          <span>
            <b>启用 NAT</b>
            <small>让内网设备通过 NAS 的 WAN 网口访问其他网络。</small>
          </span>
        </label>
        ${enabled &&
        html`
          <div class="field">
            <span>WAN 出口网口</span>
            <div class="member-grid">
              ${candidates.map(
                (item) => html`
                  <label class="member-row" key=${item.name}>
                    <input
                      type="checkbox"
                      checked=${selected.includes(item.name)}
                      onChange=${() => toggle(item.name)}
                    />
                    <span class="mono">${item.name}</span>
                    <span class="muted-2">${item.ipv4?.join(", ") || "无地址"}</span>
                  </label>
                `,
              )}
            </div>
          </div>
        `}
        <div class="form-actions">
          <button
            class="btn btn-primary"
            type="submit"
            disabled=${busy || !!pending || (enabled && selected.length === 0)}
          >
            ${busy ? "保存中…" : "保存 NAT 设置"}
          </button>
        </div>
      </form>
    </${Card}>
  `;
}

function PortForwardPage({ data, interfaces, busy, pending, onCreate, onEdit, onToggle, onDelete, onSaveNAT }) {
  const rules = data?.rules || [];
  return html`
    <${WarningBanner} warnings=${data?.warnings} />
    <div class="warn-banner">
      <span class="banner-icon">i</span>
      <div>
        规则写入独立的 inet niunexus 表，不会 flush 系统规则。每条规则带命名计数器；
        规则变更会立即应用并自动生成快照，失败时回滚。
      </div>
    </div>

    <${NATCard}
      nat=${data?.nat}
      interfaces=${interfaces}
      busy=${busy}
      pending=${pending}
      onSave=${onSaveNAT}
    />

    <div class="section">
      <div class="section-head">
        <div class="section-title">端口转发规则</div>
        <button class="btn btn-primary" type="button" onClick=${onCreate} disabled=${busy || !!pending}>
          添加规则
        </button>
      </div>
      ${rules.length === 0
        ? html`
            <div class="placeholder">
              <h3>还没有端口转发规则</h3>
              <p>把 WAN 侧端口映射到内网服务，支持 TCP、UDP、端口范围和 hairpin 回环。</p>
            </div>
          `
        : html`
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>名称</th>
                    <th>协议</th>
                    <th>WAN</th>
                    <th>外部端口</th>
                    <th>内部目标</th>
                    <th>Hairpin</th>
                    <th>状态</th>
                    <th>计数</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  ${rules.map((rule) => html`
                    <tr key=${rule.id}>
                      <td>${rule.name || rule.id}</td>
                      <td><${Badge}>${String(rule.protocol || "").toUpperCase()}</${Badge}></td>
                      <td class="mono">${rule.wan || "任意"}</td>
                      <td class="mono">${rule.externalPort}</td>
                      <td class="mono">${rule.destIp}:${rule.destPort}</td>
                      <td>${rule.hairpin ? html`<${Badge} tone="accent">是</${Badge}>` : html`<span class="muted-2">否</span>`}</td>
                      <td>
                        <${Badge} tone=${rule.enabled ? "ok" : ""}>${rule.enabled ? "启用" : "禁用"}</${Badge}>
                      </td>
                      <td class="mono">
                        ${rule.counter?.packets || 0} 包 / ${fmtBytes(rule.counter?.bytes || 0)}
                      </td>
                      <td>
                        <div class="row-actions">
                          <button
                            class="btn btn-ghost"
                            type="button"
                            onClick=${() => onToggle(rule, !rule.enabled)}
                            disabled=${busy || !!pending}
                          >
                            ${rule.enabled ? "禁用" : "启用"}
                          </button>
                          <button class="btn btn-ghost" type="button" onClick=${() => onEdit(rule)} disabled=${busy || !!pending}>
                            编辑
                          </button>
                          <button class="btn btn-ghost" type="button" onClick=${() => onDelete(rule)} disabled=${busy || !!pending}>
                            删除
                          </button>
                        </div>
                      </td>
                    </tr>
                  `)}
                </tbody>
              </table>
            </div>
          `}
    </div>
  `;
}

function RouteEditor({ interfaces, busy, onClose, onSubmit }) {
  const candidates = (interfaces || []).filter((item) => item.type !== "loopback");
  const [draft, setDraft] = useState({
    destination: "",
    gateway: "",
    device: "",
    metric: 100,
  });
  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const isDefault = ["", "default", "0.0.0.0/0"].includes(String(draft.destination).trim());

  return html`
    <${Modal} title="添加静态路由" onClose=${onClose}>
      <form class="settings-form" onSubmit=${(event) => {
        event.preventDefault();
        onSubmit({
          destination: draft.destination,
          gateway: draft.gateway,
          device: draft.device,
          metric: Number(draft.metric) || 0,
        });
      }}>
        ${isDefault &&
        html`
          <div class="warn-banner" style="margin-bottom: 0">
            <span class="banner-icon">!</span>
            <div>默认路由会影响整机出口。应用后会进入待确认状态，未在倒计时内确认将自动回滚。</div>
          </div>
        `}
        <div class="form-grid">
          <label class="field">
            <span>目标网段</span>
            <input
              value=${draft.destination}
              onInput=${(event) => update("destination", event.target.value)}
              placeholder="10.0.0.0/24 或 default"
            />
          </label>
          <label class="field">
            <span>网关</span>
            <input
              value=${draft.gateway}
              onInput=${(event) => update("gateway", event.target.value)}
              placeholder="172.21.240.1"
            />
          </label>
          <label class="field">
            <span>出口网口（可选）</span>
            <select value=${draft.device} onChange=${(event) => update("device", event.target.value)}>
              <option value="">自动解析</option>
              ${candidates.map((item) => html`<option value=${item.name}>${item.name}</option>`)}
            </select>
          </label>
          <label class="field">
            <span>Metric</span>
            <input
              type="number"
              min="0"
              value=${draft.metric}
              onInput=${(event) => update("metric", event.target.value)}
            />
          </label>
        </div>
        <div class="modal-actions">
          <span style="flex: 1"></span>
          <button class="btn" type="button" onClick=${onClose} disabled=${busy}>取消</button>
          <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? "保存中…" : "添加"}</button>
        </div>
      </form>
    </${Modal}>
  `;
}

function PolicyEditor({ interfaces, busy, onClose, onSubmit }) {
  const candidates = (interfaces || []).filter((item) => item.type !== "loopback");
  const [draft, setDraft] = useState({
    name: "",
    source: "",
    wan: candidates[0]?.name || "",
    gateway: "",
    table: 100,
    priority: 1000,
  });
  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));

  return html`
    <${Modal} title="添加策略路由" onClose=${onClose}>
      <form class="settings-form" onSubmit=${(event) => {
        event.preventDefault();
        onSubmit({
          name: draft.name,
          source: draft.source,
          wan: draft.wan,
          gateway: draft.gateway,
          table: Number(draft.table),
          priority: Number(draft.priority),
        });
      }}>
        <div class="warn-banner" style="margin-bottom: 0">
          <span class="banner-icon">!</span>
          <div>
            策略路由会改变匹配来源的出口。应用后会进入待确认状态，未在倒计时内确认将自动回滚。
          </div>
        </div>
        <div class="form-grid">
          <label class="field">
            <span>名称</span>
            <input value=${draft.name} onInput=${(event) => update("name", event.target.value)} placeholder="例如 LAN 走 WAN2" />
          </label>
          <label class="field">
            <span>来源网段</span>
            <input
              value=${draft.source}
              onInput=${(event) => update("source", event.target.value)}
              placeholder="192.168.50.0/24"
            />
          </label>
          <label class="field">
            <span>出口网口</span>
            <select value=${draft.wan} onChange=${(event) => update("wan", event.target.value)}>
              ${candidates.map((item) => html`<option value=${item.name}>${item.name}</option>`)}
            </select>
          </label>
          <label class="field">
            <span>网关</span>
            <input
              value=${draft.gateway}
              onInput=${(event) => update("gateway", event.target.value)}
              placeholder="192.168.50.1"
            />
          </label>
          <label class="field">
            <span>路由表（100-199）</span>
            <input
              type="number"
              min="100"
              max="199"
              value=${draft.table}
              onInput=${(event) => update("table", event.target.value)}
            />
          </label>
          <label class="field">
            <span>优先级</span>
            <input
              type="number"
              min="1"
              max="32766"
              value=${draft.priority}
              onInput=${(event) => update("priority", event.target.value)}
            />
          </label>
        </div>
        <div class="modal-actions">
          <span style="flex: 1"></span>
          <button class="btn" type="button" onClick=${onClose} disabled=${busy}>取消</button>
          <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? "保存中…" : "添加"}</button>
        </div>
      </form>
    </${Modal}>
  `;
}

function RoutingPage({ data, interfaces, busy, pending, onCreateRoute, onDeleteRoute, onCreatePolicy, onDeletePolicy }) {
  const routes = data?.routes || [];
  const rules = data?.rules || [];
  return html`
    <${WarningBanner} warnings=${data?.warnings} />
    <div class="warn-banner">
      <span class="banner-icon">i</span>
      <div>
        静态路由写入主路由表；策略路由使用独立的 100-199 路由表，并为每个策略创建一条表内默认路由。
        默认路由和策略路由属于高风险变更，需要确认，未确认会自动回滚。
      </div>
    </div>

    <div class="section" style="margin-top: 0">
      <div class="section-head">
        <div class="section-title">静态路由</div>
        <button class="btn btn-primary" type="button" onClick=${onCreateRoute} disabled=${busy || !!pending}>
          添加静态路由
        </button>
      </div>
      ${routes.length === 0
        ? html`<div class="placeholder" style="padding: 24px"><p>暂无静态路由。</p></div>`
        : html`
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>目标</th>
                    <th>网关</th>
                    <th>出口</th>
                    <th>Metric</th>
                    <th>状态</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  ${routes.map((route) => html`
                    <tr key=${route.id}>
                      <td class="mono">${route.destination}</td>
                      <td class="mono">${route.gateway || "—"}</td>
                      <td class="mono">${route.device || "自动"}</td>
                      <td>${route.metric || 0}</td>
                      <td><${Badge} tone=${route.present ? "ok" : "bad"}>${route.present ? "已生效" : "未生效"}</${Badge}></td>
                      <td>
                        <button class="btn btn-ghost" type="button" onClick=${() => onDeleteRoute(route)} disabled=${busy || !!pending}>
                          删除
                        </button>
                      </td>
                    </tr>
                  `)}
                </tbody>
              </table>
            </div>
          `}
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">策略路由</div>
        <button class="btn btn-primary" type="button" onClick=${onCreatePolicy} disabled=${busy || !!pending}>
          添加策略路由
        </button>
      </div>
      ${rules.length === 0
        ? html`<div class="placeholder" style="padding: 24px"><p>暂无策略路由。</p></div>`
        : html`
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>名称</th>
                    <th>来源</th>
                    <th>出口</th>
                    <th>网关</th>
                    <th>表</th>
                    <th>优先级</th>
                    <th>状态</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  ${rules.map((rule) => html`
                    <tr key=${rule.id}>
                      <td>${rule.name || "未命名"}</td>
                      <td class="mono">${rule.source}</td>
                      <td class="mono">${rule.wan}</td>
                      <td class="mono">${rule.gateway}</td>
                      <td class="mono">${rule.table}</td>
                      <td>${rule.priority}</td>
                      <td><${Badge} tone=${rule.present ? "ok" : "bad"}>${rule.present ? "已生效" : "未生效"}</${Badge}></td>
                      <td>
                        <button class="btn btn-ghost" type="button" onClick=${() => onDeletePolicy(rule)} disabled=${busy || !!pending}>
                          删除
                        </button>
                      </td>
                    </tr>
                  `)}
                </tbody>
              </table>
            </div>
          `}
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">内核路由表</div>
        <div class="muted-2">共 ${data?.liveRoutes?.length || 0} 条</div>
      </div>
      <${RoutesTable} routes=${data?.liveRoutes} limit=${100} />
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">内核策略规则</div>
        <div class="muted-2">共 ${data?.liveRules?.length || 0} 条</div>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>优先级</th>
              <th>来源</th>
              <th>表</th>
            </tr>
          </thead>
          <tbody>
            ${(data?.liveRules || []).map((rule, index) => html`
              <tr key=${`${rule.priority}-${index}`}>
                <td class="mono">${rule.priority}</td>
                <td class="mono">${rule.source || "—"}</td>
                <td class="mono">${rule.table}</td>
              </tr>
            `)}
          </tbody>
        </table>
      </div>
    </div>
  `;
}

function ProxyPage({ data, interfaces, busy, pending, onSave, onRestart }) {
  const [draft, setDraft] = useState(null);

  useEffect(() => {
    if (!data?.config) return;
    const config = data.config;
    setDraft({
      enabled: !!config.enabled,
      listen: config.listen || "0.0.0.0",
      listenPort: config.listenPort || 1080,
      transparentPort: config.transparentPort || 12345,
      requireAuth: !!config.requireAuth,
      username: config.username || "",
      proxyPassword: "",
      clearProxyPassword: false,
      transparentEnabled: !!config.transparent?.enabled,
      transparentInterfaces: config.transparent?.interfaces || [],
      sourceCidrs: (config.transparent?.sourceCidrs || []).join(", "),
      upstreamType: config.upstream?.type || "none",
      upstreamAddress: config.upstream?.address || "",
      upstreamUsername: config.upstream?.username || "",
      upstreamPassword: "",
      clearUpstreamPassword: false,
      bypass: (config.bypass || []).join(", "),
    });
  }, [data]);

  if (!data) {
    return html`<div class="placeholder"><h3>正在读取代理状态…</h3></div>`;
  }
  if (!draft) {
    return html`<div class="skeleton" style="height: 240px"></div>`;
  }

  const status = data.status || {};
  const candidates = (interfaces || []).filter(
    (item) => ["device", "bridge", "vlan", "bond"].includes(item.type) && item.name !== "lo",
  );
  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const toggleIface = (name) =>
    setDraft((current) => ({
      ...current,
      transparentInterfaces: current.transparentInterfaces.includes(name)
        ? current.transparentInterfaces.filter((item) => item !== name)
        : [...current.transparentInterfaces, name],
    }));
  const splitList = (value) =>
    String(value || "")
      .split(/[,\n]/)
      .map((item) => item.trim())
      .filter(Boolean);

  const submit = (event) => {
    event.preventDefault();
    onSave({
      enabled: draft.enabled,
      listen: draft.listen,
      listenPort: Number(draft.listenPort),
      transparentPort: Number(draft.transparentPort),
      requireAuth: draft.requireAuth,
      username: draft.username,
      transparent: {
        enabled: draft.transparentEnabled,
        interfaces: draft.transparentInterfaces,
        sourceCidrs: splitList(draft.sourceCidrs),
      },
      upstream: {
        type: draft.upstreamType,
        address: draft.upstreamAddress,
        username: draft.upstreamUsername,
      },
      bypass: splitList(draft.bypass),
      proxyPassword: draft.proxyPassword || undefined,
      upstreamPassword: draft.upstreamPassword || undefined,
      clearProxyPassword: draft.clearProxyPassword,
      clearUpstreamPassword: draft.clearUpstreamPassword,
    });
  };

  return html`
    <${WarningBanner} warnings=${data.warnings} />

    <div class="grid cards">
      <${Card} title="运行状态">
        <div class="stat-value small">
          <span class=${`dot ${status.running ? "ok" : "bad"}`} style="display:inline-block;margin-right:8px"></span>
          ${status.running ? "运行中" : "未运行"}
        </div>
        <div class="stat-label">正向代理 ${status.listen || "—"} · 透明代理 ${status.transparentListen || "—"}</div>
        <div style="height: 12px"></div>
        <${KV}
          rows=${[
            { key: "连接", value: `${status.active || 0} 活跃 / ${status.total || 0} 累计` },
            { key: "流量", value: `↓ ${fmtBytes(status.bytesIn || 0)} · ↑ ${fmtBytes(status.bytesOut || 0)}` },
            { key: "认证", value: data.config?.requireAuth ? "已启用" : "未启用" },
          ]}
        />
      </${Card}>

      <${Card} title="协议说明">
        <div class="stat-value small">HTTP CONNECT + SOCKS5</div>
        <div class="stat-label">同一端口自动识别协议；透明模式仅处理 TCP。</div>
        <div style="height: 12px"></div>
        <div class="muted-2" style="font-size: 12px; line-height: 1.7">
          首版不支持 TLS 解密、UDP/QUIC 和按域名/SNI 分流。反向代理/端口发布请使用“端口转发”。
          透明代理会写入独立 nft 表，并排除本机地址、代理端口和 Bypass CIDR。
        </div>
      </${Card}>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">代理配置</div>
        <div class="muted-2">透明模式变更属于高风险操作，需要确认</div>
      </div>
      <${Card}>
        <form class="settings-form" onSubmit=${submit}>
          <label class="switch-row">
            <input type="checkbox" checked=${draft.enabled} onChange=${(event) => update("enabled", event.target.checked)} />
            <span>
              <b>启用代理</b>
              <small>启用正向代理监听；透明模式在此基础上额外生效。</small>
            </span>
          </label>

          <div class="form-grid">
            <label class="field">
              <span>监听地址</span>
              <input value=${draft.listen} onInput=${(event) => update("listen", event.target.value)} placeholder="0.0.0.0" />
            </label>
            <label class="field">
              <span>正向代理端口</span>
              <input
                type="number"
                min="1"
                max="65535"
                value=${draft.listenPort}
                onInput=${(event) => update("listenPort", event.target.value)}
              />
            </label>
            <label class="field">
              <span>透明代理端口</span>
              <input
                type="number"
                min="1"
                max="65535"
                value=${draft.transparentPort}
                onInput=${(event) => update("transparentPort", event.target.value)}
              />
            </label>
          </div>

          <label class="switch-row">
            <input
              type="checkbox"
              checked=${draft.requireAuth}
              onChange=${(event) => update("requireAuth", event.target.checked)}
            />
            <span>
              <b>启用认证</b>
              <small>HTTP Basic / SOCKS5 用户名密码。监听 0.0.0.0 时强烈建议启用。</small>
            </span>
          </label>
          ${draft.requireAuth &&
          html`
            <div class="form-grid">
              <label class="field">
                <span>认证用户名</span>
                <input value=${draft.username} onInput=${(event) => update("username", event.target.value)} />
              </label>
              <label class="field">
                <span>认证密码</span>
                <input
                  type="password"
                  value=${draft.proxyPassword}
                  onInput=${(event) => update("proxyPassword", event.target.value)}
                  placeholder=${data.hasProxyPassword ? "已保存，留空不修改" : "设置密码"}
                />
              </label>
              ${data.hasProxyPassword &&
              html`
                <label class="switch-row">
                  <input
                    type="checkbox"
                    checked=${draft.clearProxyPassword}
                    onChange=${(event) => update("clearProxyPassword", event.target.checked)}
                  />
                  <span><b>清除已保存密码</b><small>启用认证时不能为空。</small></span>
                </label>
              `}
            </div>
          `}

          <label class="switch-row">
            <input
              type="checkbox"
              checked=${draft.transparentEnabled}
              onChange=${(event) => update("transparentEnabled", event.target.checked)}
            />
            <span>
              <b>启用 TCP 透明代理</b>
              <small>通过 nft redirect 将指定来源的 TCP 流量转到本机代理引擎。</small>
            </span>
          </label>
          ${draft.transparentEnabled &&
          html`
            <div class="field">
              <span>透明代理来源网段（逗号或换行分隔）</span>
              <input
                value=${draft.sourceCidrs}
                onInput=${(event) => update("sourceCidrs", event.target.value)}
                placeholder="192.168.50.0/24"
              />
            </div>
            <div class="field">
              <span>限制来源网口（可选）</span>
              <div class="member-grid">
                ${candidates.map(
                  (item) => html`
                    <label class="member-row" key=${item.name}>
                      <input
                        type="checkbox"
                        checked=${draft.transparentInterfaces.includes(item.name)}
                        onChange=${() => toggleIface(item.name)}
                      />
                      <span class="mono">${item.name}</span>
                      <span class="muted-2">${item.ipv4?.join(", ") || "无地址"}</span>
                    </label>
                  `,
                )}
              </div>
            </div>
          `}

          <div class="form-grid">
            <label class="field">
              <span>上游代理类型</span>
              <select value=${draft.upstreamType} onChange=${(event) => update("upstreamType", event.target.value)}>
                <option value="none">直连</option>
                <option value="http">HTTP CONNECT</option>
                <option value="socks5">SOCKS5</option>
              </select>
            </label>
            ${draft.upstreamType !== "none" &&
            html`
              <label class="field">
                <span>上游地址（host:port）</span>
                <input
                  value=${draft.upstreamAddress}
                  onInput=${(event) => update("upstreamAddress", event.target.value)}
                  placeholder="10.0.0.2:1080"
                />
              </label>
              <label class="field">
                <span>上游用户名（可选）</span>
                <input
                  value=${draft.upstreamUsername}
                  onInput=${(event) => update("upstreamUsername", event.target.value)}
                />
              </label>
              <label class="field">
                <span>上游密码（可选）</span>
                <input
                  type="password"
                  value=${draft.upstreamPassword}
                  onInput=${(event) => update("upstreamPassword", event.target.value)}
                  placeholder=${data.hasUpstreamPassword ? "已保存，留空不修改" : "可选"}
                />
              </label>
              ${data.hasUpstreamPassword &&
              html`
                <label class="switch-row">
                  <input
                    type="checkbox"
                    checked=${draft.clearUpstreamPassword}
                    onChange=${(event) => update("clearUpstreamPassword", event.target.checked)}
                  />
                  <span><b>清除上游密码</b><small>切换上游类型时建议清理。</small></span>
                </label>
              `}
            `}
          </div>

          <label class="field field-wide">
            <span>Bypass（CIDR 或域名，逗号/换行分隔）</span>
            <textarea
              class="field-textarea"
              value=${draft.bypass}
              onInput=${(event) => update("bypass", event.target.value)}
              placeholder="10.0.0.0/8, 192.168.0.0/16, internal.example.com"
            ></textarea>
          </label>

          ${draft.enabled &&
          !draft.requireAuth &&
          html`
            <div class="warn-banner" style="margin-bottom: 0">
              <span class="banner-icon">!</span>
              <div>代理未启用认证。如果监听地址不是 127.0.0.1，局域网内任何人都可以使用该代理。</div>
            </div>
          `}

          <div class="form-actions">
            <button class="btn btn-primary" type="submit" disabled=${busy || !!pending}>
              ${busy ? "保存中…" : "保存代理配置"}
            </button>
            <button class="btn" type="button" onClick=${onRestart} disabled=${busy}>
              重启代理
            </button>
          </div>
        </form>
      </${Card}>
    </div>
  `;
}

function PlaceholderPage({ phase, title, description, features }) {
  return html`
    <div class="placeholder">
      <div class="phase-tag">${phase}</div>
      <h3>${title}</h3>
      <p>${description}</p>
      <div class="feature-list">
        ${(features || []).map(
          (feature) => html`<div><b>•</b><span>${feature}</span></div>`,
        )}
      </div>
    </div>
  `;
}

function SettingsPanel({ config, saving, saveMessage, onSave }) {
  const [draft, setDraft] = useState(null);

  useEffect(() => {
    if (config?.settings) {
      setDraft({
        ...config.settings,
        protectedInterfacesText: (config.settings.protectedInterfaces || []).join(", "),
      });
    }
  }, [config]);

  if (!draft) {
    return html`<div class="skeleton" style="height: 180px"></div>`;
  }

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const submit = (event) => {
    event.preventDefault();
    onSave({
      safeMode: draft.safeMode,
      confirmTimeoutSec: Number(draft.confirmTimeoutSec),
      language: draft.language,
      logLevel: draft.logLevel,
      autoApplyOnStart: draft.autoApplyOnStart,
      protectedInterfaces: String(draft.protectedInterfacesText || "")
        .split(",")
        .map((value) => value.trim())
        .filter(Boolean),
    });
  };

  return html`
    <form class="settings-form" onSubmit=${submit}>
      <div class="form-grid">
        <label class="switch-row">
          <input
            type="checkbox"
            checked=${draft.safeMode}
            onChange=${(event) => update("safeMode", event.target.checked)}
          />
          <span>
            <b>安全模式</b>
            <small>保护默认路由网口；高风险变更必须二次确认。</small>
          </span>
        </label>
        <label class="switch-row">
          <input
            type="checkbox"
            checked=${draft.autoApplyOnStart}
            onChange=${(event) => update("autoApplyOnStart", event.target.checked)}
          />
          <span>
            <b>启动时重放配置</b>
            <small>应用启动后自动恢复端口转发、路由和代理状态。</small>
          </span>
        </label>
        <label class="field">
          <span>确认超时（秒）</span>
          <input
            type="number"
            min="5"
            max="3600"
            value=${draft.confirmTimeoutSec}
            onInput=${(event) => update("confirmTimeoutSec", event.target.value)}
          />
        </label>
        <label class="field">
          <span>界面语言</span>
          <select value=${draft.language} onChange=${(event) => update("language", event.target.value)}>
            <option value="zh-CN">简体中文</option>
            <option value="en">English</option>
          </select>
        </label>
        <label class="field">
          <span>日志级别</span>
          <select value=${draft.logLevel} onChange=${(event) => update("logLevel", event.target.value)}>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </label>
        <label class="field field-wide">
          <span>受保护网口（逗号分隔）</span>
          <input
            type="text"
            value=${draft.protectedInterfacesText}
            onInput=${(event) => update("protectedInterfacesText", event.target.value)}
            placeholder="例如 eth0"
          />
        </label>
      </div>
      <div class="form-actions">
        <button class="btn btn-primary" type="submit" disabled=${saving}>
          ${saving ? "保存中…" : "保存设置"}
        </button>
        ${saveMessage &&
        html`<span class=${`form-message ${saveMessage === "设置已保存" ? "ok" : "bad"}`}>${saveMessage}</span>`}
      </div>
    </form>
  `;
}

function formatTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString("zh-CN", { hour12: false });
}

const ACTION_LABELS = {
  "interface.apply": "接口配置",
  "interface.revert": "恢复接口原配置",
  "link.create": "创建虚拟链路",
  "link.delete": "删除虚拟链路",
  "portforward.create": "添加端口转发",
  "portforward.update": "更新端口转发",
  "portforward.delete": "删除端口转发",
  "portforward.toggle": "切换端口转发",
  "nat.update": "更新 NAT",
  "route.create": "添加静态路由",
  "route.delete": "删除静态路由",
  "policy.create": "添加策略路由",
  "policy.delete": "删除策略路由",
  "proxy.update": "更新代理",
  "proxy.restart": "重启代理",
  "settings.update": "更新设置",
  "snapshot.create": "创建快照",
};

function actionText(value) {
  return ACTION_LABELS[value] || value || "—";
}

function AuditTable({ entries }) {
  const list = entries || [];
  if (list.length === 0) {
    return html`
      <div class="placeholder" style="padding: 24px">
        <p>暂无审计记录。修改设置后这里会记录操作者、动作和结果。</p>
      </div>
    `;
  }
  return html`
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>时间</th>
            <th>用户</th>
            <th>动作</th>
            <th>结果</th>
            <th>详情</th>
            <th>耗时</th>
          </tr>
        </thead>
        <tbody>
          ${list.map(
            (entry, index) => html`
              <tr key=${`${entry.time}-${index}`}>
                <td class="mono">${formatTime(entry.time)}</td>
                <td>${entry.user || "—"}</td>
                <td class="mono">${actionText(entry.action)}</td>
                <td>
                  <${Badge} tone=${entry.result === "success" ? "ok" : "bad"}>${entry.result}</${Badge}>
                </td>
                <td class="muted audit-detail" title=${entry.message || ""}>${entry.message || "—"}</td>
                <td class="mono">${entry.durationMs || 0} ms</td>
              </tr>
            `,
          )}
        </tbody>
      </table>
    </div>
  `;
}

function SnapshotsPanel({ snapshots, message, busy, onCreate }) {
  const list = snapshots || [];
  return html`
    <div class="snapshot-toolbar">
      <button class="btn btn-primary" type="button" onClick=${onCreate} disabled=${busy}>
        ${busy ? "创建中…" : "创建快照"}
      </button>
      <span class="muted-2">
        紧急恢复：<code>sudo niunexus-cli rollback --last</code>
      </span>
      ${message &&
      html`<span class=${`form-message ${message === "快照已创建" ? "ok" : "bad"}`}>${message}</span>`}
    </div>
    ${list.length === 0
      ? html`
          <div class="placeholder" style="padding: 24px; margin-top: 12px">
            <p>暂无快照。高风险变更前会自动创建，也可以在这里手动创建。</p>
          </div>
        `
      : html`
          <div class="table-wrap" style="margin-top: 12px">
            <table>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>类型</th>
                  <th>操作者</th>
                  <th>配置</th>
                  <th>密钥</th>
                  <th>nft</th>
                  <th>系统状态</th>
                </tr>
              </thead>
              <tbody>
                ${list.map(
                  (snapshot) => html`
                    <tr key=${snapshot.id}>
                      <td class="mono">${formatTime(snapshot.createdAt)}</td>
                      <td>${snapshot.reason === "manual" ? "手动" : actionText(snapshot.reason)}</td>
                      <td>${snapshot.actor || "—"}</td>
                      <td><${Badge} tone=${snapshot.hasConfig ? "ok" : ""}>${snapshot.hasConfig ? "有" : "无"}</${Badge}></td>
                      <td><${Badge} tone=${snapshot.hasSecrets ? "ok" : ""}>${snapshot.hasSecrets ? "有" : "无"}</${Badge}></td>
                      <td><${Badge} tone=${snapshot.hasNft ? "ok" : ""}>${snapshot.hasNft ? "有" : "无"}</${Badge}></td>
                      <td><${Badge} tone=${snapshot.hasSystemState ? "ok" : ""}>${snapshot.hasSystemState ? "有" : "无"}</${Badge}></td>
                    </tr>
                  `,
                )}
              </tbody>
            </table>
          </div>
        `}
  `;
}

function SystemPage({
  data,
  session,
  config,
  configWarnings,
  configError,
  audit,
  saving,
  saveMessage,
  onSave,
  snapshots,
  snapshotMessage,
  snapshotBusy,
  onCreateSnapshot,
}) {
  const caps = data?.capabilities || {};
  const tools = caps.tools || [];
  return html`
    <${WarningBanner} warnings=${configWarnings} />
    <${ErrorBanner} message=${configError} />

    <div class="grid cards">
      <${Card} title="应用">
        <div class="stat-value small">NiuNexus ${session?.version || "—"}</div>
        <div class="stat-label">Core ${data?.health?.version || "—"} · Build ${data?.health?.buildTime || "—"}</div>
        <div style="height: 12px"></div>
        <${KV}
          rows=${[
            { key: "登录用户", value: session?.user?.username || "—" },
            { key: "UID", value: session?.user?.uid || "—" },
            { key: "网关", value: "/app/niunexus" },
          ]}
        />
      </${Card}>

      <${Card} title="系统">
        <div class="stat-value small">${data?.hostname || "—"}</div>
        <div class="stat-label">fnOS ${data?.osVersion || "—"} · ${data?.kernel || "—"}</div>
        <div style="height: 12px"></div>
        <${KV}
          rows=${[
            { key: "运行时长", value: data?.uptime || "—" },
            { key: "IPv4 转发", value: caps.ipForward ? "已开启" : "关闭" },
            { key: "IPv6 转发", value: caps.ipv6Forward ? "已开启" : "关闭" },
          ]}
        />
      </${Card}>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">应用设置</div>
        <div class="muted-2">写入配置目录，旧配置自动备份</div>
      </div>
      <${Card}>
        <${SettingsPanel} config=${config} saving=${saving} saveMessage=${saveMessage} onSave=${onSave} />
      </${Card}>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">配置快照</div>
        <div class="muted-2">最近 10 份 · 自动备份配置、密钥、nft 表与转发 sysctl</div>
      </div>
      <${Card}>
        <${SnapshotsPanel}
          snapshots=${snapshots}
          message=${snapshotMessage}
          busy=${snapshotBusy}
          onCreate=${onCreateSnapshot}
        />
      </${Card}>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">主机能力</div>
        <div class="muted-2">root / nft / nmcli 是写操作的关键依赖</div>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>工具</th>
              <th>状态</th>
              <th>版本</th>
              <th>路径</th>
            </tr>
          </thead>
          <tbody>
            ${tools.map(
              (tool) => html`
                <tr key=${tool.name}>
                  <td class="mono">${tool.name}</td>
                  <td><${Badge} tone=${tool.available ? "ok" : "bad"}>${tool.available ? "可用" : "缺失"}</${Badge}></td>
                  <td class="mono">${tool.version || "—"}</td>
                  <td class="mono muted">${tool.path || "—"}</td>
                </tr>
              `,
            )}
          </tbody>
        </table>
      </div>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">安全状态</div>
      </div>
      <${Card}>
        <${KV}
          rows=${[
            { key: "Core 运行身份", value: caps.root ? html`<${Badge} tone="warn">root（仅特权核心）</${Badge}>` : html`<${Badge} tone="bad">非 root（写操作不可用）</${Badge}>` },
            { key: "fnOS 防火墙", value: caps.firewallEnabled ? html`<${Badge} tone="warn">已启用</${Badge}>` : html`<${Badge} tone="ok">关闭</${Badge}>` },
            { key: "防火墙说明", value: caps.firewallDetail || "—" },
          ]}
        />
        <div class="muted-2" style="margin-top: 12px; font-size: 12px">
          接口、网桥/VLAN/聚合、端口转发/NAT、静态/策略路由与代理均已开放。所有变更先创建快照；
          高风险变更需要在倒计时内确认，否则自动回滚。Web 进程永远以应用包用户运行，不直接执行 root 命令。
        </div>
      </${Card}>
    </div>

    <div class="section">
      <div class="section-head">
        <div class="section-title">审计日志</div>
        <div class="muted-2">最近 40 条</div>
      </div>
      <${AuditTable} entries=${audit} />
    </div>

    <details class="raw">
      <summary>查看原始诊断 JSON</summary>
      <pre>${JSON.stringify(data, null, 2)}</pre>
    </details>
  `;
}

function AccessDenied({ message }) {
  return html`
    <div class="denied">
      <div class="denied-card">
        <div class="denied-icon">!</div>
        <h1>无法访问 NiuNexus</h1>
        <p>${message || "该应用仅对飞牛管理员开放。"}</p>
      </div>
    </div>
  `;
}

function App() {
  const [session, setSession] = useState(null);
  const [sessionError, setSessionError] = useState("");
  const [overview, setOverview] = useState(null);
  const [overviewError, setOverviewError] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [page, setPage] = useState("overview");
  const [rates, setRates] = useState({});
  const ratesRef = useRef(new Map());
  const [config, setConfig] = useState(null);
  const [configWarnings, setConfigWarnings] = useState([]);
  const [configError, setConfigError] = useState("");
  const [audit, setAudit] = useState([]);
  const [saving, setSaving] = useState(false);
  const [saveMessage, setSaveMessage] = useState("");
  const [snapshots, setSnapshots] = useState([]);
  const [snapshotMessage, setSnapshotMessage] = useState("");
  const [snapshotBusy, setSnapshotBusy] = useState(false);
  const [interfaces, setInterfaces] = useState([]);
  const [interfaceWarnings, setInterfaceWarnings] = useState([]);
  const [links, setLinks] = useState([]);
  const [linkWarnings, setLinkWarnings] = useState([]);
  const [pending, setPending] = useState(null);
  const [changeMessage, setChangeMessage] = useState("");
  const [changeBusy, setChangeBusy] = useState(false);
  const [editingInterface, setEditingInterface] = useState(null);
  const [linkEditorOpen, setLinkEditorOpen] = useState(false);
  const [portForwardData, setPortForwardData] = useState({
    rules: [],
    nat: { enabled: false, interfaces: [] },
    warnings: [],
  });
  const [portForwardEditor, setPortForwardEditor] = useState(null);
  const [portForwardBusy, setPortForwardBusy] = useState(false);
  const [routingData, setRoutingData] = useState({
    routes: [],
    rules: [],
    liveRoutes: [],
    liveRules: [],
    warnings: [],
  });
  const [routingBusy, setRoutingBusy] = useState(false);
  const [routeEditorOpen, setRouteEditorOpen] = useState(false);
  const [policyEditorOpen, setPolicyEditorOpen] = useState(false);
  const [proxyData, setProxyData] = useState(null);
  const [proxyBusy, setProxyBusy] = useState(false);
  const [now, setNow] = useState(Date.now());

  const loadSession = async () => {
    try {
      const payload = await api("api/v1/session");
      csrfToken = payload.csrf || "";
      setSession(payload);
      setSessionError("");
    } catch (error) {
      setSessionError(error.message || "无法获取会话信息");
    }
  };

  const loadOverview = async (showSpinner = false) => {
    if (showSpinner) setRefreshing(true);
    try {
      const payload = await api("api/v1/overview");
      const nextRates = {};
      const now = performance.now();
      for (const iface of payload.interfaces || []) {
        const previous = ratesRef.current.get(iface.name);
        if (previous) {
          const elapsed = (now - previous.at) / 1000;
          if (elapsed >= 0.5) {
            nextRates[iface.name] = {
              rx: Math.max(0, (iface.rxBytes - previous.rxBytes) / elapsed),
              tx: Math.max(0, (iface.txBytes - previous.txBytes) / elapsed),
            };
          }
        }
        ratesRef.current.set(iface.name, {
          at: now,
          rxBytes: iface.rxBytes,
          txBytes: iface.txBytes,
        });
      }
      setRates(nextRates);
      setOverview(payload);
      setOverviewError("");
    } catch (error) {
      setOverviewError(error.message || "读取网络状态失败");
    } finally {
      setLoading(false);
      if (showSpinner) setRefreshing(false);
    }
  };

  useEffect(() => {
    loadSession();
  }, []);

  useEffect(() => {
    if (!session?.user?.isAdmin) return undefined;
    loadOverview();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") loadOverview();
    }, 3000);
    return () => clearInterval(timer);
  }, [session]);

  const loadConfig = async () => {
    try {
      const payload = await api("api/v1/config");
      setConfig(payload.config);
      setConfigWarnings(payload.warnings || []);
      setConfigError("");
    } catch (error) {
      setConfigError(error.message || "读取配置失败");
    }
  };

  const loadAudit = async () => {
    try {
      const payload = await api("api/v1/audit?limit=40");
      setAudit(payload.entries || []);
    } catch (error) {
      setConfigError(error.message || "读取审计日志失败");
    }
  };

  const loadSnapshots = async () => {
    try {
      const payload = await api("api/v1/snapshots");
      setSnapshots(payload.snapshots || []);
    } catch (error) {
      setSnapshotMessage(error.message || "读取快照失败");
    }
  };

  const createSnapshot = async () => {
    setSnapshotBusy(true);
    setSnapshotMessage("");
    try {
      await api("api/v1/snapshots", { method: "POST" });
      setSnapshotMessage("快照已创建");
      await loadSnapshots();
      await loadAudit();
    } catch (error) {
      setSnapshotMessage(error.message || "创建快照失败");
    } finally {
      setSnapshotBusy(false);
    }
  };

  const loadInterfaces = async () => {
    try {
      const payload = await api("api/v1/interfaces");
      setInterfaces(payload.interfaces || []);
      setInterfaceWarnings(payload.warnings || []);
    } catch (error) {
      setInterfaceWarnings([error.message || "读取网口失败"]);
    }
  };

  const loadLinks = async () => {
    try {
      const payload = await api("api/v1/links");
      setLinks(payload.links || []);
      setLinkWarnings(payload.warnings || []);
    } catch (error) {
      setLinkWarnings([error.message || "读取虚拟链路失败"]);
    }
  };

  const loadPending = async () => {
    try {
      const result = await api("api/v1/changes/pending");
      setPending(result?.status === "pending" ? result : null);
    } catch {
      // The banner is best-effort; a transient core restart must not spam the UI.
    }
  };

  const afterChange = async (result, successMessage) => {
    if (result?.status === "pending") {
      setPending(result);
      setChangeMessage("变更已应用。请在倒计时内确认，否则会自动回滚。");
    } else {
      setPending(null);
      setChangeMessage(successMessage || "变更已应用");
      await loadPending();
    }
    await Promise.all([loadInterfaces(), loadLinks(), loadOverview()]);
  };

  const applyInterface = async (name, form) => {
    setChangeBusy(true);
    setChangeMessage("");
    try {
      const result = await api(`api/v1/interfaces/${encodeURIComponent(name)}/apply`, {
        method: "POST",
        body: JSON.stringify(form),
      });
      setEditingInterface(null);
      await afterChange(result, "网口配置已应用");
    } catch (error) {
      setChangeMessage(error.message || "应用网口配置失败");
    } finally {
      setChangeBusy(false);
    }
  };

  const revertInterface = async (name) => {
    if (!window.confirm(`恢复 ${name} 的原 NetworkManager 配置？当前管理连接会被删除。`)) return;
    setChangeBusy(true);
    setChangeMessage("");
    try {
      const result = await api(`api/v1/interfaces/${encodeURIComponent(name)}/revert`, { method: "POST" });
      setEditingInterface(null);
      await afterChange(result, "已恢复原配置");
    } catch (error) {
      setChangeMessage(`${error.message || "恢复失败"}。如果当前连接中断，请刷新页面确认。`);
    } finally {
      setChangeBusy(false);
    }
  };

  const confirmChange = async (id) => {
    setChangeBusy(true);
    try {
      const result = await api(`api/v1/changes/${encodeURIComponent(id)}/confirm`, { method: "POST" });
      setPending(null);
      setChangeMessage(result?.message || "变更已确认");
      await Promise.all([loadInterfaces(), loadLinks(), loadRouting(), loadOverview()]);
    } catch (error) {
      setChangeMessage(error.message || "确认变更失败");
    } finally {
      setChangeBusy(false);
    }
  };

  const rollbackChange = async (id) => {
    if (!window.confirm("立即回滚这次变更？")) return;
    setChangeBusy(true);
    try {
      const result = await api(`api/v1/changes/${encodeURIComponent(id)}/rollback`, { method: "POST" });
      setPending(null);
      setChangeMessage(result?.message || "变更已回滚");
      await Promise.all([loadInterfaces(), loadLinks(), loadRouting(), loadOverview()]);
    } catch (error) {
      setChangeMessage(error.message || "回滚失败");
    } finally {
      setChangeBusy(false);
    }
  };

  const createLink = async (form) => {
    setChangeBusy(true);
    setChangeMessage("");
    try {
      const result = await api("api/v1/links", { method: "POST", body: JSON.stringify(form) });
      setLinkEditorOpen(false);
      await afterChange(result, "虚拟链路已创建");
    } catch (error) {
      setChangeMessage(error.message || "创建虚拟链路失败");
    } finally {
      setChangeBusy(false);
    }
  };

  const deleteLink = async (link) => {
    if (!window.confirm(`删除 ${link.type} ${link.name}？`)) return;
    setChangeBusy(true);
    setChangeMessage("");
    try {
      const result = await api(`api/v1/links/${encodeURIComponent(link.id)}`, { method: "DELETE" });
      await afterChange(result, "虚拟链路已删除");
    } catch (error) {
      setChangeMessage(error.message || "删除虚拟链路失败");
    } finally {
      setChangeBusy(false);
    }
  };

  const loadPortForwards = async () => {
    try {
      const payload = await api("api/v1/port-forwards");
      setPortForwardData(payload);
    } catch (error) {
      setPortForwardData((current) => ({
        ...current,
        warnings: [error.message || "读取端口转发失败"],
      }));
    }
  };

  const savePortForward = async (draft, id) => {
    setPortForwardBusy(true);
    try {
      if (id) {
        await api(`api/v1/port-forwards/${encodeURIComponent(id)}`, {
          method: "PUT",
          body: JSON.stringify(draft),
        });
      } else {
        await api("api/v1/port-forwards", { method: "POST", body: JSON.stringify(draft) });
      }
      setPortForwardEditor(null);
      setChangeMessage(id ? "端口转发已更新" : "端口转发已创建");
      await loadPortForwards();
    } catch (error) {
      setChangeMessage(error.message || "保存端口转发失败");
    } finally {
      setPortForwardBusy(false);
    }
  };

  const togglePortForward = async (rule, enabled) => {
    setPortForwardBusy(true);
    try {
      await api(`api/v1/port-forwards/${encodeURIComponent(rule.id)}/toggle`, {
        method: "POST",
        body: JSON.stringify({ enabled }),
      });
      setChangeMessage(enabled ? "端口转发已启用" : "端口转发已禁用");
      await loadPortForwards();
    } catch (error) {
      setChangeMessage(error.message || "切换端口转发失败");
    } finally {
      setPortForwardBusy(false);
    }
  };

  const deletePortForward = async (rule) => {
    if (!window.confirm(`删除端口转发 ${rule.name || rule.id}？`)) return;
    setPortForwardBusy(true);
    try {
      await api(`api/v1/port-forwards/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
      setChangeMessage("端口转发已删除");
      await loadPortForwards();
    } catch (error) {
      setChangeMessage(error.message || "删除端口转发失败");
    } finally {
      setPortForwardBusy(false);
    }
  };

  const saveNAT = async (nat) => {
    setPortForwardBusy(true);
    try {
      await api("api/v1/nat", { method: "PUT", body: JSON.stringify(nat) });
      setChangeMessage("NAT 设置已保存");
      await loadPortForwards();
    } catch (error) {
      setChangeMessage(error.message || "保存 NAT 设置失败");
    } finally {
      setPortForwardBusy(false);
    }
  };

  const loadRouting = async () => {
    try {
      const payload = await api("api/v1/routes");
      setRoutingData(payload);
    } catch (error) {
      setRoutingData((current) => ({
        ...current,
        warnings: [error.message || "读取路由失败"],
      }));
    }
  };

  const afterRouting = async (result, successMessage) => {
    if (result?.status === "pending") {
      setPending(result);
      setChangeMessage("路由变更已应用。请在倒计时内确认，否则自动回滚。");
    } else {
      setPending(null);
      setChangeMessage(successMessage || "路由变更已应用");
      await loadPending();
    }
    await Promise.all([loadRouting(), loadInterfaces(), loadOverview()]);
  };

  const createRoute = async (form) => {
    setRoutingBusy(true);
    try {
      const result = await api("api/v1/routes", { method: "POST", body: JSON.stringify(form) });
      setRouteEditorOpen(false);
      await afterRouting(result, "静态路由已添加");
    } catch (error) {
      setChangeMessage(error.message || "添加静态路由失败");
    } finally {
      setRoutingBusy(false);
    }
  };

  const deleteRoute = async (route) => {
    if (!window.confirm(`删除静态路由 ${route.destination}？`)) return;
    setRoutingBusy(true);
    try {
      const result = await api(`api/v1/routes/${encodeURIComponent(route.id)}`, { method: "DELETE" });
      await afterRouting(result, "静态路由已删除");
    } catch (error) {
      setChangeMessage(error.message || "删除静态路由失败");
    } finally {
      setRoutingBusy(false);
    }
  };

  const createPolicy = async (form) => {
    setRoutingBusy(true);
    try {
      const result = await api("api/v1/policy-rules", { method: "POST", body: JSON.stringify(form) });
      setPolicyEditorOpen(false);
      await afterRouting(result, "策略路由已添加");
    } catch (error) {
      setChangeMessage(error.message || "添加策略路由失败");
    } finally {
      setRoutingBusy(false);
    }
  };

  const deletePolicy = async (rule) => {
    if (!window.confirm(`删除策略路由 ${rule.name || rule.source}？`)) return;
    setRoutingBusy(true);
    try {
      const result = await api(`api/v1/policy-rules/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
      await afterRouting(result, "策略路由已删除");
    } catch (error) {
      setChangeMessage(error.message || "删除策略路由失败");
    } finally {
      setRoutingBusy(false);
    }
  };

  const loadProxy = async () => {
    try {
      const payload = await api("api/v1/proxy");
      setProxyData(payload);
    } catch (error) {
      setProxyData((current) => ({
        ...(current || { config: {}, status: {}, hasProxyPassword: false, hasUpstreamPassword: false }),
        warnings: [error.message || "读取代理状态失败"],
      }));
    }
  };

  const afterProxy = async (result, successMessage) => {
    if (result?.status === "pending") {
      setPending(result);
      setChangeMessage("代理变更已应用。请在倒计时内确认，否则自动回滚。");
    } else {
      setPending(null);
      setChangeMessage(successMessage || "代理配置已应用");
      await loadPending();
    }
    await Promise.all([loadProxy(), loadOverview()]);
  };

  const saveProxy = async (form) => {
    setProxyBusy(true);
    try {
      const result = await api("api/v1/proxy", { method: "PUT", body: JSON.stringify(form) });
      await afterProxy(result, "代理配置已保存");
    } catch (error) {
      setChangeMessage(error.message || "保存代理配置失败");
    } finally {
      setProxyBusy(false);
    }
  };

  const refreshCurrent = async () => {
    setRefreshing(true);
    try {
      const tasks = [loadOverview()];
      if (page === "interfaces") tasks.push(loadInterfaces());
      if (page === "links") tasks.push(loadLinks(), loadInterfaces());
      if (page === "forward") tasks.push(loadPortForwards(), loadInterfaces());
      if (page === "routes") tasks.push(loadRouting(), loadInterfaces());
      if (page === "proxy") tasks.push(loadProxy(), loadInterfaces());
      if (page === "system") tasks.push(loadConfig(), loadAudit(), loadSnapshots());
      await Promise.all(tasks);
    } finally {
      setRefreshing(false);
    }
  };

  const restartProxy = async () => {
    setProxyBusy(true);
    try {
      await api("api/v1/proxy/restart", { method: "POST" });
      setChangeMessage("代理已重启");
      await loadProxy();
    } catch (error) {
      setChangeMessage(error.message || "重启代理失败");
    } finally {
      setProxyBusy(false);
    }
  };

  const saveSettings = async (settings) => {
    setSaving(true);
    setSaveMessage("");
    try {
      const payload = await api("api/v1/config/settings", {
        method: "PATCH",
        body: JSON.stringify(settings),
      });
      setConfig(payload.config);
      setConfigWarnings(payload.warnings || []);
      setSaveMessage("设置已保存");
      await loadAudit();
    } catch (error) {
      setSaveMessage(error.message || "保存失败");
    } finally {
      setSaving(false);
    }
  };

  useEffect(() => {
    if (!session?.user?.isAdmin) return undefined;
    if (page === "system") {
      loadConfig();
      loadAudit();
      loadSnapshots();
    }
    return undefined;
  }, [session, page]);

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    if (!session?.user?.isAdmin) return undefined;
    loadPending();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") loadPending();
    }, 2000);
    return () => clearInterval(timer);
  }, [session]);

  useEffect(() => {
    if (!session?.user?.isAdmin) return undefined;
    if (page === "interfaces") {
      loadInterfaces();
    }
    if (page === "links") {
      loadLinks();
      loadInterfaces();
    }
    if (page === "forward") {
      loadPortForwards();
      loadInterfaces();
    }
    if (page === "routes") {
      loadRouting();
      loadInterfaces();
    }
    if (page === "proxy") {
      loadProxy();
      loadInterfaces();
    }
    return undefined;
  }, [session, page]);

  useEffect(() => {
    if (!changeMessage) return undefined;
    const timer = setTimeout(() => setChangeMessage(""), 8000);
    return () => clearTimeout(timer);
  }, [changeMessage]);

  useEffect(() => {
    setChangeMessage("");
  }, [page]);

  if (!session && !sessionError) {
    return html`
      <div class="boot">
        <div class="boot-mark">NN</div>
        <div>正在验证管理员身份…</div>
      </div>
    `;
  }

  if (sessionError) {
    return html`<${AccessDenied} message=${sessionError} />`;
  }

  if (!session?.user?.isAdmin) {
    return html`<${AccessDenied} message="当前账号不是飞牛管理员，无法打开网络管理功能。" />`;
  }

  const current = NAV_ITEMS.find((item) => item.id === page) || NAV_ITEMS[0];
  const coreOk = overview?.health?.status === "ok";

  let content = null;
  switch (page) {
    case "overview":
      content = html`<${OverviewPage} data=${overview} rates=${rates} loading=${loading} />`;
      break;
    case "interfaces":
      content = html`
        <${InterfacesPage}
          items=${interfaces}
          warnings=${interfaceWarnings}
          busy=${changeBusy}
          pending=${pending}
          onEdit=${setEditingInterface}
        />
      `;
      break;
    case "routes":
      content = html`
        <${RoutingPage}
          data=${routingData}
          interfaces=${interfaces}
          busy=${routingBusy}
          pending=${pending}
          onCreateRoute=${() => setRouteEditorOpen(true)}
          onDeleteRoute=${deleteRoute}
          onCreatePolicy=${() => setPolicyEditorOpen(true)}
          onDeletePolicy=${deletePolicy}
        />
      `;
      break;
    case "forward":
      content = html`
        <${PortForwardPage}
          data=${portForwardData}
          interfaces=${interfaces}
          busy=${portForwardBusy}
          pending=${pending}
          onCreate=${() => setPortForwardEditor({})}
          onEdit=${setPortForwardEditor}
          onToggle=${togglePortForward}
          onDelete=${deletePortForward}
          onSaveNAT=${saveNAT}
        />
      `;
      break;
    case "links":
      content = html`
        <${LinksPage}
          links=${links}
          warnings=${linkWarnings}
          interfaces=${interfaces}
          busy=${changeBusy}
          pending=${pending}
          onCreate=${() => setLinkEditorOpen(true)}
          onDelete=${deleteLink}
        />
      `;
      break;
    case "proxy":
      content = html`
        <${ProxyPage}
          data=${proxyData}
          interfaces=${interfaces}
          busy=${proxyBusy}
          pending=${pending}
          onSave=${saveProxy}
          onRestart=${restartProxy}
        />
      `;
      break;
    case "system":
      content = html`
        <${SystemPage}
          data=${overview}
          session=${session}
          config=${config}
          configWarnings=${configWarnings}
          configError=${configError}
          audit=${audit}
          saving=${saving}
          saveMessage=${saveMessage}
          onSave=${saveSettings}
          snapshots=${snapshots}
          snapshotMessage=${snapshotMessage}
          snapshotBusy=${snapshotBusy}
          onCreateSnapshot=${createSnapshot}
        />
      `;
      break;
    default:
      content = null;
  }

  return html`
    <div class="app-shell">
      <aside class="sidebar">
        <div class="brand">
          <div class="brand-mark">NN</div>
          <div class="brand-text">
            <div class="brand-title">NiuNexus</div>
            <div class="brand-sub">fnOS 网络管理</div>
          </div>
        </div>

        <nav class="nav">
          <div class="nav-label">网络</div>
          ${NAV_ITEMS.map(
            (item) => html`
              <button
                type="button"
                class=${`nav-item ${page === item.id ? "active" : ""}`}
                onClick=${() => setPage(item.id)}
                title=${item.desc}
              >
                <${NavIcon} name=${item.icon} />
                <span class="nav-text">${item.label}</span>
              </button>
            `,
          )}
        </nav>

        <div class="sidebar-foot">
          <div class="row"><span>版本</span><span class="mono">${session.version || "—"}</span></div>
          <div class="row"><span>Core</span><span class=${coreOk ? "" : "danger"}>${coreOk ? "在线" : "离线"}</span></div>
          <div class="row"><span>刷新</span><span>${overview?.fetchedAt ? new Date(overview.fetchedAt).toLocaleTimeString("zh-CN") : "—"}</span></div>
        </div>
      </aside>

      <main class="main">
        <div class="topbar">
          <div>
            <h1 class="page-title">${current.label}</h1>
            <p class="page-desc">${current.desc}</p>
          </div>
          <div class="topbar-right">
            <${StatusPill} ok=${coreOk} label=${coreOk ? "Core 在线" : "Core 离线"} />
            <span class="user-chip">
              <span class="user-avatar">${(session.user.username || "A").slice(0, 1).toUpperCase()}</span>
              ${session.user.username || "管理员"}
            </span>
            <button class="btn btn-ghost" type="button" onClick=${refreshCurrent} disabled=${refreshing}>
              ${refreshing ? "刷新中…" : "刷新"}
            </button>
          </div>
        </div>

        <${ErrorBanner} message=${overviewError} />
        <${PendingBanner}
          pending=${pending}
          now=${now}
          busy=${changeBusy}
          onConfirm=${confirmChange}
          onRollback=${rollbackChange}
        />
        ${changeMessage && html`<div class="change-note">${changeMessage}</div>`}
        ${content}

        ${editingInterface &&
        html`
          <${InterfaceEditor}
            key=${editingInterface.name}
            iface=${editingInterface}
            busy=${changeBusy}
            onClose=${() => setEditingInterface(null)}
            onSubmit=${applyInterface}
            onRevert=${revertInterface}
          />
        `}
        ${linkEditorOpen &&
        html`
          <${LinkEditor}
            interfaces=${interfaces}
            busy=${changeBusy}
            onClose=${() => setLinkEditorOpen(false)}
            onSubmit=${createLink}
          />
        `}
        ${portForwardEditor &&
        html`
          <${PortForwardEditor}
            key=${portForwardEditor.id || "new"}
            rule=${portForwardEditor.id ? portForwardEditor : null}
            interfaces=${interfaces}
            busy=${portForwardBusy}
            onClose=${() => setPortForwardEditor(null)}
            onSubmit=${savePortForward}
          />
        `}
        ${routeEditorOpen &&
        html`
          <${RouteEditor}
            interfaces=${interfaces}
            busy=${routingBusy}
            onClose=${() => setRouteEditorOpen(false)}
            onSubmit=${createRoute}
          />
        `}
        ${policyEditorOpen &&
        html`
          <${PolicyEditor}
            interfaces=${interfaces}
            busy=${routingBusy}
            onClose=${() => setPolicyEditorOpen(false)}
            onSubmit=${createPolicy}
          />
        `}

        <div class="footer-note">
          NiuNexus · 飞牛 fnOS 原生网络管理 · 所有网络变更均带快照、确认与超时回滚
        </div>
      </main>
    </div>
  `;
}

render(html`<${App} />`, document.getElementById("app"));
