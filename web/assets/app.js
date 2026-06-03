"use strict";

/* ============================================================
   plugdash frontend — vanilla JS, no deps
   ============================================================ */

const main = document.getElementById("main");
const nav = document.getElementById("nav");

let currentView = "dashboard";

/* ---------- tiny DOM helpers ---------- */
function el(tag, attrs, children) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const k in attrs) {
      const v = attrs[k];
      if (v == null || v === false) continue;
      if (k === "class") node.className = v;
      else if (k === "text") node.textContent = v;
      else if (k === "html") node.innerHTML = v;
      else if (k.startsWith("on") && typeof v === "function") {
        node.addEventListener(k.slice(2).toLowerCase(), v);
      } else if (v === true) {
        node.setAttribute(k, "");
      } else {
        node.setAttribute(k, v);
      }
    }
  }
  if (children != null) {
    const arr = Array.isArray(children) ? children : [children];
    for (const c of arr) {
      if (c == null || c === false) continue;
      node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
  }
  return node;
}

function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

/* ---------- API ---------- */
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.text();
      if (body) msg += ` — ${body}`;
    } catch (e) {
      /* ignore */
    }
    throw new Error(msg);
  }
  if (res.status === 204) return null;
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) return res.json();
  return res.text();
}

const API = {
  plugins: () => api("/api/plugins"),
  trackers: () => api("/api/trackers"),
  createTracker: (body) =>
    api("/api/trackers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  updateTracker: (id, body) =>
    api(`/api/trackers/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  deleteTracker: (id) =>
    api(`/api/trackers/${encodeURIComponent(id)}`, { method: "DELETE" }),
  runTracker: (id) => api(`/api/trackers/${encodeURIComponent(id)}/run`),
  run: () => api("/api/run"),
  forceTracker: (id) =>
    api(`/api/trackers/${encodeURIComponent(id)}/run?force=true`),
  rescanPlugins: () => api("/api/plugins/rescan", { method: "POST" }),
  clearTrackers: () => api("/api/trackers/clear", { method: "POST" }),
  reloadTrackers: () => api("/api/trackers/reload", { method: "POST" }),
  importTrackers: (yaml) =>
    api("/api/trackers/import", {
      method: "POST",
      headers: { "Content-Type": "application/x-yaml" },
      body: yaml,
    }),
  getConfig: () => api("/api/config"),
  getLogs: () => api("/api/logs"),
  clearLogs: () => api("/api/logs", { method: "DELETE" }),
  getSettings: () => api("/api/settings"),
  saveSettings: (body) =>
    api("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
};

/* ---------- settings + live-stream state ----------
   Runs are server-driven now: the engine executes trackers on their cadence and
   pushes results over SSE (/api/stream); one upstream call serves every client,
   and the engine idles when nobody is connected. The dashboard subscribes while
   it is the active view and falls back to polling the cached /api/run if SSE is
   unavailable. */
let settingsCache = null;
let streamSource = null; // EventSource
let pollTimer = null; // cached-poll fallback
let onSnapshot = null; // current dashboard's snapshot handler

const SETTINGS_DEFAULTS = {
  autorefresh_enabled: true,
};

// stopUpdates tears down any live stream / poll loop (called when leaving the
// dashboard view so the engine can notice the client left and idle).
function stopUpdates() {
  if (streamSource) {
    streamSource.close();
    streamSource = null;
  }
  if (pollTimer != null) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
  onSnapshot = null;
}

// startUpdates subscribes to the live SSE stream and dispatches each snapshot to
// `handler`. If SSE errors/aborts it falls back to polling the cached /api/run
// (which also keeps the engine marked as watched). `handler(snapshot)` receives
// the same shape as a /api/run array element.
function startUpdates(handler) {
  stopUpdates();
  onSnapshot = handler;

  let usingPoll = false;
  const startPoll = () => {
    if (usingPoll) return;
    usingPoll = true;
    const poll = async () => {
      try {
        const snaps = await API.run();
        if (onSnapshot) for (const s of snaps) onSnapshot(s);
      } catch (e) {
        /* transient */
      }
    };
    poll();
    pollTimer = setInterval(poll, 8000);
  };

  try {
    const es = new EventSource("/api/stream");
    streamSource = es;
    es.addEventListener("snapshot", (ev) => {
      try {
        if (onSnapshot) onSnapshot(JSON.parse(ev.data));
      } catch (e) {
        /* ignore malformed frame */
      }
    });
    es.onerror = () => {
      // EventSource auto-reconnects; if it can't, switch to polling so the
      // dashboard still updates (and the engine still sees a watcher).
      if (es.readyState === EventSource.CLOSED) {
        es.close();
        if (streamSource === es) streamSource = null;
        startPoll();
      }
    };
  } catch (e) {
    startPoll(); // browser without EventSource
  }
}

// The Logs view polls so entries stream in as trackers run.
let logsTimer = null;
function clearLogsTimer() {
  if (logsTimer != null) {
    clearInterval(logsTimer);
    logsTimer = null;
  }
}

// When the dashboard's edit affordance jumps to the Trackers view, this carries
// which tracker to open in the edit form.
let pendingEditId = null;

/* ---------- formatting helpers ---------- */
function fmtTimestamp(ts) {
  if (!ts) return "";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return String(ts);
  const now = Date.now();
  const diff = now - d.getTime();
  const min = 60 * 1000,
    hour = 60 * min,
    day = 24 * hour;
  if (diff >= 0 && diff < min) return "just now";
  if (diff >= 0 && diff < hour) return Math.round(diff / min) + "m ago";
  if (diff >= 0 && diff < day) return Math.round(diff / hour) + "h ago";
  if (diff >= 0 && diff < 7 * day) return Math.round(diff / day) + "d ago";
  return d.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

function cellText(v) {
  if (v == null) return "";
  if (typeof v === "object") {
    try {
      return JSON.stringify(v);
    } catch (e) {
      return String(v);
    }
  }
  return String(v);
}

/* ============================================================
   Visualization renderers
   ============================================================ */
function renderViz(visualization, data) {
  switch (visualization) {
    case "list":
      return renderList(data);
    case "checklist":
      return renderChecklist(data);
    case "table":
      return renderTable(data);
    case "stat":
      return renderStat(data);
    case "timeseries":
      return renderTimeseries(data);
    case "gauge":
      return renderGauge(data);
    default:
      return renderRaw(data);
  }
}

function renderList(data) {
  const items = (data && Array.isArray(data.items) && data.items) || [];
  if (!items.length) {
    return el("div", { class: "skeleton", text: "No items." });
  }
  // Build one .list-item row from an item object.
  function buildRow(it) {
    const item = it || {};
    const titleText = item.title != null ? String(item.title) : "(untitled)";
    const title = item.url
      ? el("a", {
          class: "list-title",
          href: item.url,
          target: "_blank",
          rel: "noopener noreferrer",
          text: titleText,
        })
      : el("span", { class: "list-title", text: titleText });

    const mainCol = el("div", { class: "list-main" }, [
      title,
      item.subtitle
        ? el("div", { class: "list-sub", text: String(item.subtitle) })
        : null,
    ]);

    const metaChildren = [];
    if (item.timestamp) {
      metaChildren.push(
        el("span", { class: "list-ts", text: fmtTimestamp(item.timestamp) })
      );
    }
    if (item.badge) {
      const b = String(item.badge);
      const slug = b.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
      metaChildren.push(el("span", { class: "pill pill-" + slug, text: b }));
    }
    // Optional multi-badge form: [{label, tone}] where tone ∈ ok|warn|bad|neutral.
    if (Array.isArray(item.badges)) {
      for (const bd of item.badges) {
        if (!bd || !bd.label) continue;
        const tone = ["ok", "warn", "bad", "neutral"].includes(bd.tone) ? bd.tone : "neutral";
        metaChildren.push(
          el("span", { class: "pill pill-tone-" + tone, text: String(bd.label) })
        );
      }
    }
    const meta = metaChildren.length
      ? el("div", { class: "list-meta" }, metaChildren)
      : null;

    const avatar = item.icon ? el("img", { class: "avatar", src: item.icon, loading: "lazy", alt: "" }) : null;
    return el("div", { class: "list-item" }, [avatar, mainCol, meta]);
  }

  const wrap = el("div", { class: "viz-list" });
  // Items flagged {collapsed:true} are tucked behind a "show more" expander, so
  // a long tail of uninteresting rows (e.g. up-to-date deps) stays out of the way.
  const collapsed = items.filter((it) => it && it.collapsed);
  for (const it of items) {
    if (it && it.collapsed) continue;
    wrap.appendChild(buildRow(it));
  }
  if (collapsed.length) {
    const hidden = el("div", { class: "list-collapsed" });
    for (const it of collapsed) hidden.appendChild(buildRow(it));
    const label = collapsed.length + " more";
    const toggle = el("button", {
      class: "jobs-toggle",
      type: "button",
      text: "▸ " + label,
    });
    toggle.addEventListener("click", () => {
      const open = hidden.classList.toggle("open");
      toggle.textContent = (open ? "▾ " : "▸ ") + label;
    });
    wrap.appendChild(toggle);
    wrap.appendChild(hidden);
  }
  return wrap;
}

function renderChecklist(data) {
  const items = (data && Array.isArray(data.items) && data.items) || [];
  const allOk =
    data && typeof data.all_ok === "boolean"
      ? data.all_ok
      : items.every((i) => i && i.ok);

  const frag = document.createDocumentFragment();
  frag.appendChild(
    el(
      "div",
      { class: "checklist-head " + (allOk ? "ok" : "fail") },
      [
        el("span", { text: allOk ? "✓" : "✗" }),
        el("span", { text: allOk ? "All checks passing" : "Some checks failing" }),
      ]
    )
  );

  if (!items.length) {
    frag.appendChild(el("div", { class: "skeleton", text: "No checks." }));
    return frag;
  }

  for (const it of items) {
    const item = it || {};
    const ok = !!item.ok;
    const labelText = item.label != null ? String(item.label) : "";
    const labelEl = item.url
      ? el("a", {
          class: "check-label",
          href: item.url,
          target: "_blank",
          rel: "noopener noreferrer",
          text: labelText,
        })
      : el("span", { class: "check-label", text: labelText });
    // Optional per-item job links (e.g. github-actions check runs). Hidden by
    // default behind a toggle to keep the birds-eye row compact.
    const links = (Array.isArray(item.links) ? item.links : []).filter(
      (ln) => ln && ln.url
    );
    let toggleEl = null;
    let linksEl = null;
    if (links.length) {
      const failed = links.filter((ln) => !ln.ok).length;
      const summary =
        links.length + " job" + (links.length === 1 ? "" : "s") +
        (failed ? ` · ${failed} failing` : "");
      toggleEl = el("button", {
        class: "jobs-toggle",
        type: "button",
        text: "▸ " + summary,
      });
      linksEl = el("div", { class: "check-links" });
      for (const ln of links) {
        linksEl.appendChild(
          el("a", {
            class: "job-pill " + (ln.ok ? "ok" : "no"),
            href: ln.url,
            target: "_blank",
            rel: "noopener noreferrer",
            title: (ln.ok ? "passed: " : "failed: ") + (ln.label || ln.url),
            text: (ln.ok ? "✓ " : "✗ ") + String(ln.label || "job"),
          })
        );
      }
      toggleEl.addEventListener("click", () => {
        const open = linksEl.classList.toggle("open");
        toggleEl.textContent = (open ? "▾ " : "▸ ") + summary;
      });
    }

    const avatar = item.icon ? el("img", { class: "avatar", src: item.icon, loading: "lazy", alt: "" }) : null;

    frag.appendChild(
      el("div", { class: "check-item" }, [
        el("span", {
          class: "check-mark " + (ok ? "ok" : "no"),
          text: ok ? "✓" : "✗",
        }),
        avatar,
        el("div", { class: "list-main" }, [
          labelEl,
          item.detail
            ? el("div", { class: "check-detail", text: String(item.detail) })
            : null,
          toggleEl,
          linksEl,
        ]),
      ])
    );
  }
  return frag;
}

function renderTable(data) {
  const columns = (data && Array.isArray(data.columns) && data.columns) || [];
  const rows = (data && Array.isArray(data.rows) && data.rows) || [];
  if (!columns.length && !rows.length) {
    return el("div", { class: "skeleton", text: "No data." });
  }

  const thead = el(
    "thead",
    {},
    el(
      "tr",
      {},
      columns.map((c) => el("th", { text: cellText(c) }))
    )
  );

  const tbody = el("tbody");
  for (const row of rows) {
    const cells = Array.isArray(row) ? row : [row];
    tbody.appendChild(
      el(
        "tr",
        {},
        cells.map((c) => el("td", { text: cellText(c) }))
      )
    );
  }

  return el("div", { class: "table-wrap" }, el("table", { class: "viz-table" }, [
    thead,
    tbody,
  ]));
}

function renderStat(data) {
  const d = data || {};
  const status = ["ok", "warn", "error"].includes(d.status) ? d.status : "";
  return el("div", { class: "viz-stat" }, [
    el("div", {
      class: "stat-value " + status,
      text: d.value != null ? String(d.value) : "—",
    }),
    d.label ? el("div", { class: "stat-label", text: String(d.label) }) : null,
  ]);
}

function renderTimeseries(data) {
  const d = data || {};
  const points = (Array.isArray(d.points) ? d.points : []).filter(
    (p) => p && p.v != null
  );
  const wrap = el("div", { class: "viz-ts" });

  const total =
    d.total != null
      ? d.total
      : points.length
      ? points[points.length - 1].v
      : 0;
  wrap.appendChild(
    el("div", { class: "ts-head" }, [
      el("span", { class: "ts-total", text: String(total) }),
      d.label
        ? el("span", {
            class: "ts-label",
            text: String(d.label) + (d.unit ? " (" + d.unit + ")" : ""),
          })
        : null,
    ])
  );

  if (points.length < 2) {
    wrap.appendChild(
      el("div", {
        class: "skeleton",
        text: points.length ? "Not enough data to chart." : "No data yet.",
      })
    );
    return wrap;
  }

  const W = 300,
    H = 90,
    padL = 3,
    padR = 3,
    padT = 6,
    padB = 4;
  const parsed = points.map((p) => Date.parse(p.t));
  const useDates = parsed.every((x) => !Number.isNaN(x));
  const xv = useDates ? parsed : points.map((_, i) => i);
  const vv = points.map((p) => Number(p.v) || 0);
  const minX = Math.min(...xv),
    maxX = Math.max(...xv);
  const minY = Math.min(...vv),
    maxY = Math.max(...vv);
  const spanX = maxX - minX || 1;
  const spanY = maxY - minY || 1;
  const sx = (x) => padL + ((x - minX) / spanX) * (W - padL - padR);
  const sy = (v) => padT + (1 - (v - minY) / spanY) * (H - padT - padB);

  let line = "";
  points.forEach((_, i) => {
    line += (i === 0 ? "M" : "L") + sx(xv[i]).toFixed(1) + " " + sy(vv[i]).toFixed(1) + " ";
  });
  const area =
    line +
    `L${sx(maxX).toFixed(1)} ${(H - padB).toFixed(1)} ` +
    `L${sx(minX).toFixed(1)} ${(H - padB).toFixed(1)} Z`;

  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  svg.setAttribute("class", "ts-svg");
  svg.setAttribute("preserveAspectRatio", "none");
  const areaPath = document.createElementNS(NS, "path");
  areaPath.setAttribute("d", area);
  areaPath.setAttribute("class", "ts-area");
  const linePath = document.createElementNS(NS, "path");
  linePath.setAttribute("d", line.trim());
  linePath.setAttribute("class", "ts-line");
  linePath.setAttribute("vector-effect", "non-scaling-stroke");
  svg.appendChild(areaPath);
  svg.appendChild(linePath);
  wrap.appendChild(svg);

  const fmtX = (x) =>
    useDates
      ? new Date(x).toLocaleDateString(undefined, { month: "short", year: "2-digit" })
      : String(x);
  wrap.appendChild(
    el("div", { class: "ts-axis" }, [
      el("span", { text: fmtX(minX) }),
      el("span", { text: fmtX(maxX) }),
    ])
  );
  return wrap;
}

function renderGauge(data) {
  const d = data || {};
  const val = Number(d.value) || 0;
  const max = Number(d.max) || 0;
  const pct = max > 0 ? Math.max(0, Math.min(100, (val / max) * 100)) : 0;
  const status = ["ok", "warn", "error"].includes(d.status) ? d.status : "";
  const unit = d.unit ? " " + d.unit : "";

  const wrap = el("div", { class: "viz-gauge" });
  wrap.appendChild(
    el("div", { class: "gauge-head" }, [
      el("span", { class: "gauge-pct " + status, text: Math.round(pct) + "%" }),
      d.label ? el("span", { class: "gauge-label", text: String(d.label) }) : null,
    ])
  );
  const fill = el("div", { class: "gauge-fill " + status });
  fill.style.width = pct.toFixed(1) + "%";
  wrap.appendChild(el("div", { class: "gauge-track" }, fill));

  const footParts = [];
  if (max > 0) footParts.push(val + " / " + max + unit);
  if (d.detail) footParts.push(String(d.detail));
  if (footParts.length) {
    wrap.appendChild(el("div", { class: "gauge-foot", text: footParts.join(" · ") }));
  }
  return wrap;
}

function renderRaw(data) {
  let text;
  try {
    text = JSON.stringify(data, null, 2);
  } catch (e) {
    text = String(data);
  }
  return el("pre", { class: "viz-raw", text: text });
}

/* ============================================================
   Dashboard view
   ============================================================ */
async function renderDashboard() {
  clear(main);
  stopUpdates();

  if (!settingsCache) {
    try {
      settingsCache = await API.getSettings();
    } catch (e) {
      settingsCache = { ...SETTINGS_DEFAULTS };
    }
  }

  // Slim toolbar: refresh-all, edit mode, live toggle.
  const refreshBtn = el("button", { class: "icon-btn", title: "Refresh all now", text: "↻" });

  const editToggle = el("button", { class: "icon-btn", title: "Edit mode — edit/delete widgets", text: "✎" });
  editToggle.addEventListener("click", () => {
    const on = main.classList.toggle("editing");
    editToggle.classList.toggle("active", on);
  });

  // Live: subscribe to server pushes while viewing (and count as a present
  // viewer so the engine runs). Off → show the last cached snapshot, frozen.
  const live = el("input", { type: "checkbox" });
  live.checked = settingsCache.autorefresh_enabled !== false;
  const liveWrap = el("label", { class: "switch switch-sm", title: "Live updates (server pushes results while viewing)" }, [
    live,
    el("span", { class: "switch-track" }, el("span", { class: "switch-thumb" })),
    el("span", { class: "switch-label", text: "Live" }),
  ]);

  main.appendChild(el("div", { class: "dash-toolbar" }, [liveWrap, editToggle, refreshBtn]));

  const body = el("div");
  main.appendChild(body);

  const byId = new Map(); // tracker id -> { tracker, card }

  function applySnapshot(snap) {
    if (!snap || snap.tracker_id == null) return;
    const st = byId.get(snap.tracker_id);
    if (!st) return;
    const at = snap.fetched_at ? Date.parse(snap.fetched_at) : Date.now();
    fillCard(st.card, st.tracker, snap, snap.refresh_interval_seconds, at);
  }

  async function hydrateOnce() {
    try {
      const snaps = await API.run();
      for (const s of snaps) applySnapshot(s);
    } catch (e) {
      /* ignore */
    }
  }

  function connect() {
    if (live.checked) startUpdates(applySnapshot);
    else {
      stopUpdates();
      hydrateOnce();
    }
  }

  live.addEventListener("change", async () => {
    settingsCache.autorefresh_enabled = live.checked;
    connect();
    try {
      settingsCache = await API.saveSettings(settingsCache);
    } catch (e) {
      /* non-fatal */
    }
  });

  function forceOne(t, card) {
    card.root.classList.add("is-loading");
    API.forceTracker(t.id).catch((e) => fillCardError(card, t, e.message || String(e)));
    if (!live.checked) setTimeout(hydrateOnce, 1500);
  }

  refreshBtn.addEventListener("click", () => {
    for (const st of byId.values()) forceOne(st.tracker, st.card);
  });

  async function build() {
    clear(body);
    body.appendChild(el("div", { class: "loading-text", text: "Loading trackers…" }));

    let trackers;
    try {
      trackers = await API.trackers();
    } catch (e) {
      clear(body);
      body.appendChild(el("div", { class: "empty" }, [
        el("strong", { text: "Could not load trackers" }),
        el("span", { text: String(e.message || e) }),
      ]));
      return;
    }

    clear(body);
    if (!trackers || !trackers.length) {
      body.appendChild(el("div", { class: "empty" }, [
        el("strong", { text: "No trackers yet" }),
        el("span", { text: "Head to Trackers to add your first widget." }),
      ]));
      return;
    }

    // Plugin sizes drive each card's grid footprint (unless uniform sizing is on).
    const sizeById = {};
    try {
      if (!pluginsCache) pluginsCache = await API.plugins();
      for (const p of pluginsCache || []) {
        sizeById[p.id] = { w: p.width || 1, h: p.height || 1 };
      }
    } catch (e) {
      /* sizing is optional */
    }
    const uniform = !!(settingsCache && settingsCache.uniform_sizes);

    const grid = el("div", { class: "grid" });
    body.appendChild(grid);

    const ordered = orderTrackers(trackers, settingsCache && settingsCache.dashboard_order);
    byId.clear();
    for (const t of ordered) {
      const card = buildCardShell(t);
      applyCardSize(card.root, sizeById[t.plugin_id], uniform);
      wireCardDrag(card.root, grid);
      grid.appendChild(card.root);
      byId.set(t.id, { tracker: t, card });

      if (card.refreshBtn) {
        card.refreshBtn.addEventListener("click", () => forceOne(t, card));
      }
      if (card.editBtn) {
        card.editBtn.addEventListener("click", () => {
          pendingEditId = t.id;
          setView("configure");
        });
      }
      if (card.deleteBtn) {
        card.deleteBtn.addEventListener("click", async () => {
          if (!confirm(`Delete widget "${t.name || t.plugin_id}"?`)) return;
          try {
            await API.deleteTracker(t.id);
            await build();
            connect();
          } catch (e) {
            alert("Delete failed: " + (e.message || e));
          }
        });
      }
    }

    grid.addEventListener("dragover", (e) => {
      if (!grid.querySelector(".dragging")) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      const after = getDragAfterElement(grid, e.clientX, e.clientY);
      const dragging = grid.querySelector(".dragging");
      if (!dragging) return;
      if (after == null) grid.appendChild(dragging);
      else if (after !== dragging) grid.insertBefore(dragging, after);
    });
  }

  await build();
  connect();
}

// iconFor maps a plugin id to a glyph + accent color. The glyph is used in the
// plugin dropdown (text-only <option>); cards use svgFor() for crisp SVG icons.
function iconFor(pluginId) {
  const map = {
    "github-releases": { g: "🏷️", c: "#a371f7" },
    "github-release-artifacts": { g: "📦", c: "#2dd4bf" },
    "github-repo-stats": { g: "📊", c: "#5b9dff" },
    "github-actions-status": { g: "⚙️", c: "#3fb950" },
    "github-activity": { g: "📈", c: "#e3b341" },
    "github-activity-rate": { g: "📊", c: "#db61a2" },
    "github-issues": { g: "🐛", c: "#e5534b" },
    "github-issue-watch": { g: "👁️", c: "#bc8cff" },
    "github-prs": { g: "🔀", c: "#6cb6ff" },
    "endoflife": { g: "⏳", c: "#e3b341" },
    "osv-vulns": { g: "🛡️", c: "#e5534b" },
    "dependency-freshness": { g: "📦", c: "#3fb950" },
    "github-milestone": { g: "🎯", c: "#a371f7" },
    "github-workflow-health": { g: "💚", c: "#3fb950" },
    "github-review-requested": { g: "👀", c: "#e3b341" },
    "github-stale": { g: "🕸️", c: "#8b949e" },
    "file-version": { g: "📄", c: "#8b949e" },
    "http-health": { g: "🌐", c: "#39c5cf" },
    "rss-feed": { g: "📡", c: "#f0883e" },
    "docker-image": { g: "🐳", c: "#2496ed" },
  };
  return map[pluginId] || { g: "🧩", c: "#9aa4b1" };
}

// Inline line-icons per plugin type — center perfectly and inherit the type
// color via currentColor.
const ICON_SVG = {
  "github-releases": '<path d="M3 3h7l11 11-7 7L3 10z"/><circle cx="7.5" cy="7.5" r="1.4" fill="currentColor" stroke="none"/>',
  "github-release-artifacts": '<path d="M21 8l-9-5-9 5 9 5 9-5z"/><path d="M3 8v8l9 5 9-5V8"/><path d="M12 13v8"/>',
  "github-repo-stats": '<line x1="5" y1="20" x2="5" y2="12"/><line x1="12" y1="20" x2="12" y2="5"/><line x1="19" y1="20" x2="19" y2="9"/>',
  "github-actions-status": '<circle cx="12" cy="12" r="9"/><path d="M8 12l3 3 5-6"/>',
  "github-activity": '<path d="M3 13h4l3-8 4 16 3-8h4"/>',
  "github-activity-rate": '<line x1="6" y1="20" x2="6" y2="13"/><line x1="12" y1="20" x2="12" y2="6"/><line x1="18" y1="20" x2="18" y2="10"/>',
  "github-issues": '<circle cx="12" cy="12" r="9"/><line x1="12" y1="8" x2="12" y2="13"/><circle cx="12" cy="16.3" r="0.7" fill="currentColor" stroke="none"/>',
  "github-issue-watch": '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
  "github-prs": '<circle cx="6" cy="6" r="2.2"/><circle cx="6" cy="18" r="2.2"/><circle cx="18" cy="18" r="2.2"/><path d="M6 8.2v7.6"/><path d="M18 15.8V12a4 4 0 00-4-4h-3"/><path d="M13 5l-2 3 2 3"/>',
  "endoflife": '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  "osv-vulns": '<path d="M12 3l7 3v6c0 4-3 7-7 9-4-2-7-5-7-9V6z"/><path d="M9 12l2 2 4-4"/>',
  "dependency-freshness": '<path d="M21 8l-9-5-9 5 9 5 9-5z"/><path d="M3 8v8l9 5 9-5V8"/><path d="M12 13v8"/>',
  "github-milestone": '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4.5"/><circle cx="12" cy="12" r="1" fill="currentColor" stroke="none"/>',
  "github-workflow-health": '<path d="M3 12h4l2-5 4 12 2-7h6"/>',
  "github-review-requested": '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
  "github-stale": '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',

  "file-version": '<path d="M14 3H7a2 2 0 00-2 2v14a2 2 0 002 2h10a2 2 0 002-2V8z"/><path d="M14 3v5h5"/>',
  "http-health": '<circle cx="12" cy="12" r="9"/><line x1="3" y1="12" x2="21" y2="12"/><path d="M12 3a14 14 0 010 18 14 14 0 010-18"/>',
  "rss-feed": '<path d="M5 11a8 8 0 018 8"/><path d="M5 5a14 14 0 0114 14"/><circle cx="6" cy="18" r="1.4" fill="currentColor" stroke="none"/>',
  "docker-image": '<rect x="3.5" y="10.5" width="4" height="4"/><rect x="9" y="10.5" width="4" height="4"/><rect x="14.5" y="10.5" width="4" height="4"/><rect x="9" y="5.5" width="4" height="4"/><path d="M3 14.5c4 3 13 2 16-3"/>',
};
const ICON_SVG_DEFAULT = '<rect x="4" y="4" width="7" height="7" rx="1"/><rect x="13" y="4" width="7" height="7" rx="1"/><rect x="4" y="13" width="7" height="7" rx="1"/><rect x="13" y="13" width="7" height="7" rx="1"/>';

function svgFor(pluginId) {
  const inner = ICON_SVG[pluginId] || ICON_SVG_DEFAULT;
  return (
    '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" ' +
    'stroke="currentColor" stroke-width="2" stroke-linecap="round" ' +
    'stroke-linejoin="round" aria-hidden="true">' + inner + "</svg>"
  );
}

function buildCardShell(tracker) {
  const ic = iconFor(tracker.plugin_id);
  const iconEl = el("div", { class: "card-icon", html: svgFor(tracker.plugin_id) });
  iconEl.style.color = ic.c;
  iconEl.style.background = ic.c + "22";
  iconEl.style.boxShadow = "inset 0 0 0 1px " + ic.c + "44";

  const isFile = tracker.source === "file";
  const titleEl = el("div", {
    class: "card-title",
    text: tracker.name || "Tracker",
  });
  const configBadge = isFile
    ? el("span", {
        class: "config-badge",
        text: "config",
        title: "Managed by config file (read-only)",
      })
    : null;
  const titleRow = el("div", { class: "card-title-row" }, [titleEl, configBadge]);
  const subtitleEl = el("div", { class: "card-subtitle" });
  const titleWrap = el("div", { class: "card-titles" }, [titleRow, subtitleEl]);
  const handle = el("div", {
    class: "card-handle",
    title: "Drag to reorder",
    text: "⠿",
  });

  // Edit-mode actions (hidden unless the dashboard is in edit mode).
  // File-managed widgets can be deleted (restored via "Reload from file") but
  // not edited (a reload would overwrite the change).
  const editBtn = isFile ? null : el("button", { class: "card-act", title: "Edit widget", text: "✎" });
  const deleteBtn = el("button", { class: "card-act danger", title: "Delete widget", text: "✕" });
  const actionsEl = el("div", { class: "card-actions" }, [editBtn, deleteBtn]);
  const bodyEl = el("div", { class: "card-body" }, [
    el("div", { class: "skeleton", text: "Running…" }),
  ]);

  // Footer: refresh cadence + last-updated + per-widget force-refresh button.
  const cadenceEl = el("span", { class: "card-cadence", text: "" });
  const updatedEl = el("span", { class: "card-updated", text: "" });
  const refreshBtn = el("button", {
    class: "card-refresh",
    title: "Refresh now",
    text: "↻",
  });
  const footEl = el("div", { class: "card-foot" }, [
    cadenceEl,
    updatedEl,
    refreshBtn,
  ]);

  const root = el("div", { class: "card is-loading", draggable: "true" }, [
    el("div", { class: "card-head" }, [handle, iconEl, titleWrap, actionsEl]),
    bodyEl,
    footEl,
  ]);
  root.style.setProperty("--type", ic.c); // per-type accent color for the card
  root.dataset.trackerId = tracker.id;
  return { root, titleEl, subtitleEl, bodyEl, cadenceEl, updatedEl, refreshBtn, editBtn, deleteBtn };
}

// applyCardSize sets a card's grid footprint from its plugin's preferred size
// (width/height in cells, 1 or 2). With uniform sizing on, or no size info, the
// card stays the default 1x1 tile.
function applyCardSize(root, size, uniform) {
  root.style.gridColumn = "";
  root.style.gridRow = "";
  root.classList.remove("card-wide", "card-tall");
  if (uniform || !size) return;
  if (size.w === 2) {
    root.style.gridColumn = "span 2";
    root.classList.add("card-wide");
  }
  if (size.h === 2) {
    root.style.gridRow = "span 2";
    root.classList.add("card-tall");
  }
}

// fmtInterval renders a seconds count as a compact cadence like "30s","2m","1h","1d".
function fmtInterval(s) {
  if (!s || s <= 0) return "";
  if (s < 60) return s + "s";
  if (s < 3600) return Math.round(s / 60) + "m";
  if (s < 86400) return Math.round(s / 3600) + "h";
  return Math.round(s / 86400) + "d";
}

// orderTrackers returns trackers sorted by the saved dashboard order. Trackers
// not in `order` keep their natural (creation) order and follow the ordered
// ones (JS sort is stable).
function orderTrackers(trackers, order) {
  if (!order || !order.length) return trackers;
  const pos = new Map(order.map((id, i) => [Number(id), i]));
  return [...trackers].sort((a, b) => {
    const pa = pos.has(a.id) ? pos.get(a.id) : Infinity;
    const pb = pos.has(b.id) ? pos.get(b.id) : Infinity;
    return pa - pb;
  });
}

// getDragAfterElement finds the card the dragged one should be inserted before,
// based on cursor position, working across a wrapping multi-column grid.
function getDragAfterElement(grid, x, y) {
  const els = [...grid.querySelectorAll(".card:not(.dragging)")];
  let closest = { dist: Infinity, el: null };
  for (const el of els) {
    const box = el.getBoundingClientRect();
    const cx = box.left + box.width / 2;
    const cy = box.top + box.height / 2;
    const offsetY = cy - y;
    const offsetX = cx - x;
    const sameRow = Math.abs(offsetY) < box.height / 2;
    // Consider an element a valid "after" target if the cursor is above it,
    // or to its left within the same row.
    if (offsetY > 0 || (sameRow && offsetX > 0)) {
      const dist = offsetY * offsetY + offsetX * offsetX;
      if (dist < closest.dist) closest = { dist, el };
    }
  }
  return closest.el;
}

function wireCardDrag(root, grid) {
  root.addEventListener("dragstart", (e) => {
    root.classList.add("dragging");
    e.dataTransfer.effectAllowed = "move";
    try {
      e.dataTransfer.setData("text/plain", root.dataset.trackerId || "");
    } catch (_) {
      /* some browsers require a data payload; ignore failures */
    }
  });
  root.addEventListener("dragend", async () => {
    root.classList.remove("dragging");
    await persistOrder(grid);
  });
}

// persistOrder snapshots the current DOM order of cards and saves it to
// settings so the layout survives reloads.
async function persistOrder(grid) {
  const ids = [...grid.querySelectorAll(".card")]
    .map((c) => Number(c.dataset.trackerId))
    .filter((n) => !Number.isNaN(n));
  if (!settingsCache) settingsCache = { ...SETTINGS_DEFAULTS };
  settingsCache.dashboard_order = ids;
  try {
    settingsCache = await API.saveSettings(settingsCache);
  } catch (e) {
    /* keep the local order even if the save fails */
  }
}

function updateCardFoot(card, intervalSec, at) {
  if (card.cadenceEl) {
    const c = fmtInterval(intervalSec);
    card.cadenceEl.textContent = c ? "updates every " + c : "";
  }
  if (card.updatedEl) {
    if (at) {
      // Store the raw fetch time so the live ticker can re-age the "X ago"
      // label without new data, and expose the exact time on hover.
      card.updatedEl.dataset.ts = String(at);
      card.updatedEl.textContent = "updated " + fmtTimestamp(at);
      card.updatedEl.title = "Data fetched " + new Date(at).toLocaleString();
    } else {
      delete card.updatedEl.dataset.ts;
      card.updatedEl.textContent = "";
      card.updatedEl.removeAttribute("title");
    }
  }
}

// The snapshot's fetch time is fixed and correct; only its human rendering
// ("2m ago") drifts as the wall clock advances. Re-age every card's label on a
// timer so a dashboard left open doesn't sit on a stale "updated just now" — no
// server calls, purely cosmetic.
let updatedTicker = null;
function refreshUpdatedLabels() {
  document.querySelectorAll(".card-updated[data-ts]").forEach((elm) => {
    const ts = Number(elm.dataset.ts);
    if (ts) elm.textContent = "updated " + fmtTimestamp(ts);
  });
}
function startUpdatedTicker() {
  if (updatedTicker) return;
  updatedTicker = setInterval(refreshUpdatedLabels, 30000);
}

// applyCardStatus colors the card's accent strip by widget health, so the
// dashboard reads at a glance: green = all good, red = failing, amber = warn.
function applyCardStatus(root, status) {
  root.classList.remove("status-ok", "status-fail", "status-warn");
  if (status) root.classList.add("status-" + status);
}

// cardStatus derives a status from a result where it is meaningful.
function cardStatus(result) {
  if (!result || !result.data) return "";
  const d = result.data;
  switch (result.visualization) {
    case "checklist":
      return d.all_ok ? "ok" : "fail";
    case "stat":
      if (d.status === "ok") return "ok";
      if (d.status === "error") return "fail";
      if (d.status === "warn") return "warn";
      return "";
    default:
      return "";
  }
}

function fillCard(card, tracker, res, intervalSec, at) {
  card.root.classList.remove("is-loading");
  res = res || {};
  updateCardFoot(card, intervalSec || res.refresh_interval_seconds, at || Date.now());

  if (res.error) {
    fillCardError(card, tracker, res.error, intervalSec, at);
    return;
  }

  const result = res.result;
  if (!result) {
    fillCardError(card, tracker, "No result returned.", intervalSec, at);
    return;
  }

  applyCardStatus(card.root, cardStatus(result));
  // The card title is always the user's tracker name; the plugin's own title
  // (e.g. "owner/repo — latest 3 releases") becomes a muted subtitle.
  card.titleEl.textContent = tracker.name || "Tracker";
  if (card.subtitleEl) {
    const sub = result.title && result.title !== tracker.name ? result.title : "";
    card.subtitleEl.textContent = sub;
    card.subtitleEl.style.display = sub ? "" : "none";
  }
  clear(card.bodyEl);
  card.bodyEl.appendChild(renderViz(result.visualization, result.data));
}

function fillCardError(card, tracker, message, intervalSec, at) {
  card.root.classList.remove("is-loading");
  applyCardStatus(card.root, "fail");
  updateCardFoot(card, intervalSec, at || Date.now());
  clear(card.bodyEl);
  card.bodyEl.appendChild(
    el("div", { class: "card-error" }, [
      el("span", { class: "ico", text: "!" }),
      el("span", { text: String(message) }),
    ])
  );
}

/* ============================================================
   Configure view
   ============================================================ */
let pluginsCache = null;

// buildTrackerActions renders the bulk-action bar for the Trackers view:
// reload from the configured file, load an external config (session-only),
// dump the current trackers to a config file, and clear everything. onChanged
// is called after any action that mutates the tracker set, to refresh the list.
function buildTrackerActions(onChanged) {
  const msg = el("div", { class: "form-msg" });
  const ok = (t) => {
    msg.className = "form-msg ok";
    msg.textContent = t;
  };
  const err = (t) => {
    msg.className = "form-msg error";
    msg.textContent = t;
  };
  const clearMsg = () => {
    msg.className = "form-msg";
    msg.textContent = "";
  };

  // Reload from the server's --config file (enabled only when one is set).
  const reloadBtn = el("button", {
    class: "btn btn-sm",
    text: "Reload from file",
    title: "Re-apply the server's --config file (no duplicates)",
    disabled: true,
  });
  reloadBtn.addEventListener("click", async () => {
    clearMsg();
    reloadBtn.disabled = true;
    try {
      const r = await API.reloadTrackers();
      ok(`Reloaded ${r.trackers} tracker(s) from the config file.`);
      await onChanged();
    } catch (e) {
      err("Reload failed: " + (e.message || e));
    } finally {
      reloadBtn.disabled = false;
    }
  });
  // Only enable reload when the server reports a config file.
  API.getConfig()
    .then((c) => {
      if (c && c.configured) {
        reloadBtn.disabled = false;
        reloadBtn.title = "Re-apply " + (c.path || "the --config file") + " (no duplicates)";
      } else {
        reloadBtn.title = "Start plugdash with --config to enable reloading from a file";
      }
    })
    .catch(() => {});

  // Load an external config file or pasted YAML (session-only).
  const fileInput = el("input", {
    type: "file",
    accept: ".yaml,.yml,.txt",
    style: "display:none",
  });
  const importYAML = async (yaml) => {
    clearMsg();
    if (!yaml || !yaml.trim()) {
      err("Nothing to load — the config was empty.");
      return;
    }
    try {
      const r = await API.importTrackers(yaml);
      ok(`Loaded ${r.loaded} tracker(s). These are session-only — a restart reverts to the bundled config.`);
      await onChanged();
    } catch (e) {
      err("Load failed: " + (e.message || e));
    }
  };
  fileInput.addEventListener("change", async () => {
    const f = fileInput.files && fileInput.files[0];
    if (!f) return;
    try {
      const text = await f.text();
      await importYAML(text);
    } catch (e) {
      err("Could not read file: " + (e.message || e));
    } finally {
      fileInput.value = ""; // allow re-selecting the same file
    }
  });
  const loadBtn = el("button", {
    class: "btn btn-sm",
    text: "Load from file…",
    title: "Load trackers from a config file (session-only)",
  });
  loadBtn.addEventListener("click", () => fileInput.click());

  // Paste-config toggle: reveals a textarea + Load button.
  const pasteArea = el("textarea", {
    class: "import-paste",
    placeholder: "Paste a plugdash config (YAML) here…",
    style: "display:none",
  });
  const pasteLoadBtn = el("button", {
    class: "btn btn-sm btn-primary",
    text: "Load pasted config",
    style: "display:none",
  });
  pasteLoadBtn.addEventListener("click", () => importYAML(pasteArea.value));
  const pasteToggle = el("button", {
    class: "btn btn-sm",
    text: "Paste config…",
    title: "Paste a config to load (session-only)",
  });
  pasteToggle.addEventListener("click", () => {
    const open = pasteArea.style.display === "none";
    pasteArea.style.display = open ? "" : "none";
    pasteLoadBtn.style.display = open ? "" : "none";
  });

  // Dump the current trackers to a downloadable config file.
  const dumpBtn = el("a", {
    class: "btn btn-sm",
    href: "/api/trackers/export",
    download: "plugdash-trackers.yaml",
    text: "Dump to config",
    title: "Download the current trackers as a config file",
  });

  // Clear everything currently running (on-disk config is left untouched).
  const clearBtn = el("button", {
    class: "btn btn-sm btn-danger",
    text: "Clear all",
    title: "Remove all trackers from the running dashboard",
  });
  clearBtn.addEventListener("click", async () => {
    if (!confirm("Remove ALL trackers from the running dashboard? The config file on disk is left untouched, so 'Reload from file' (or a restart) restores file-managed trackers.")) {
      return;
    }
    clearMsg();
    clearBtn.disabled = true;
    try {
      const r = await API.clearTrackers();
      ok(`Cleared ${r.cleared} tracker(s).`);
      await onChanged();
    } catch (e) {
      err("Clear failed: " + (e.message || e));
    } finally {
      clearBtn.disabled = false;
    }
  });

  return el("div", { class: "tracker-actions-bar" }, [
    el("div", { class: "tracker-actions-row" }, [
      reloadBtn,
      loadBtn,
      pasteToggle,
      dumpBtn,
      clearBtn,
      fileInput,
    ]),
    pasteArea,
    pasteLoadBtn,
    msg,
  ]);
}

async function renderConfigure() {
  clear(main);

  main.appendChild(
    el("div", { class: "view-head" }, [
      el("div", {}, [
        el("h1", { text: "Trackers" }),
        el("div", { class: "sub", text: "Add, edit and remove the widgets on your dashboard" }),
      ]),
    ])
  );

  // Bulk actions: clear the running set, reload the configured file, import an
  // external config (session-only), or dump the current trackers to a config.
  main.appendChild(buildTrackerActions(() => afterChange()));

  const layout = el("div", { class: "config-layout" });
  const listPanel = el("div", { class: "panel" }, [
    el("h2", { text: "Trackers" }),
    el("div", { class: "skeleton", text: "Loading…" }),
  ]);
  const formPanel = el("div", { class: "panel" }, [
    el("h2", { text: "Add a tracker" }),
    el("div", { class: "skeleton", text: "Loading plugins…" }),
  ]);
  layout.appendChild(listPanel);
  layout.appendChild(formPanel);
  main.appendChild(layout);

  // load plugins + trackers
  let plugins, trackers;
  try {
    [plugins, trackers] = await Promise.all([API.plugins(), API.trackers()]);
    pluginsCache = plugins;
  } catch (e) {
    clear(listPanel);
    listPanel.appendChild(el("h2", { text: "Trackers" }));
    listPanel.appendChild(
      el("div", { class: "form-msg error", text: "Failed to load: " + (e.message || e) })
    );
    return;
  }

  const pluginById = {};
  for (const p of plugins || []) pluginById[p.id] = p;

  function refreshList(list) {
    clear(listPanel);
    listPanel.appendChild(el("h2", { text: "Trackers" }));
    if (!list || !list.length) {
      listPanel.appendChild(
        el("div", { class: "skeleton", text: "No trackers configured yet." })
      );
      return;
    }
    for (const t of list) {
      const plugin = pluginById[t.plugin_id];
      const pluginName = (plugin && plugin.name) || t.plugin_id || "unknown plugin";

      const isFile = t.source === "file";
      // Editing a file-managed tracker is blocked (a reload would overwrite it),
      // but deleting one is allowed — "Reload from file" restores it.
      let editBtn = null;
      if (!isFile) {
        editBtn = el("button", {
          class: "btn btn-ghost btn-sm",
          text: "Edit",
        });
        editBtn.addEventListener("click", () => {
          buildForm(formPanel, plugins, afterChange, t);
          formPanel.scrollIntoView({ behavior: "smooth", block: "start" });
        });
      }

      const delBtn = el("button", {
        class: "btn btn-danger btn-sm",
        text: "Delete",
      });
      delBtn.addEventListener("click", async () => {
        delBtn.disabled = true;
        delBtn.textContent = "Deleting…";
        try {
          await API.deleteTracker(t.id);
          await afterChange();
        } catch (e) {
          delBtn.disabled = false;
          delBtn.textContent = "Delete";
          alert("Delete failed: " + (e.message || e));
        }
      });
      const configBadge = isFile
        ? el("span", {
            class: "config-badge",
            text: "config",
            title: "Managed by config file (read-only)",
          })
        : null;
      const ic = iconFor(t.plugin_id);
      const trIcon = el("div", { class: "card-icon tracker-icon", html: svgFor(t.plugin_id) });
      trIcon.style.color = ic.c;
      trIcon.style.background = ic.c + "22";
      trIcon.style.boxShadow = "inset 0 0 0 1px " + ic.c + "44";
      listPanel.appendChild(
        el("div", { class: "tracker-row" }, [
          trIcon,
          el("div", { class: "tracker-info" }, [
            el("div", { class: "tracker-name-row" }, [
              el("span", { class: "tracker-name", text: t.name || "(unnamed)" }),
              configBadge,
            ]),
            el("div", { class: "tracker-meta", text: pluginName }),
          ]),
          el("div", { class: "tracker-actions" }, [editBtn, delBtn]),
        ])
      );
    }
  }

  async function afterChange() {
    const fresh = await API.trackers();
    refreshList(fresh);
  }

  refreshList(trackers);

  // If we arrived here via a dashboard "edit" button, open that tracker's form.
  const editTarget = pendingEditId
    ? (trackers || []).find((t) => t.id === pendingEditId)
    : null;
  pendingEditId = null;
  if (editTarget) {
    buildForm(formPanel, plugins, afterChange, editTarget);
    formPanel.scrollIntoView({ behavior: "smooth", block: "start" });
  } else {
    buildForm(formPanel, plugins, afterChange, null);
  }
}

// buildForm renders the plugin picker + dynamic config form. When `editing` is
// a tracker, the form starts in edit mode: the plugin is preselected and locked
// (plugin type is immutable), fields are prefilled, and submit performs a PUT.
function buildForm(panel, plugins, onDone, editing) {
  clear(panel);
  const isEdit = !!editing;
  panel.appendChild(el("h2", { text: isEdit ? "Edit tracker" : "Add a tracker" }));

  if (!plugins || !plugins.length) {
    panel.appendChild(
      el("div", { class: "skeleton", text: "No plugins available." })
    );
    return;
  }

  // resetToAdd swaps the panel back to a fresh "add" form.
  const resetToAdd = () => buildForm(panel, plugins, onDone, null);

  const select = el("select", { id: "plugin-select" }, [
    el("option", { value: "", text: "— Select a plugin —" }),
    ...plugins.map((p) =>
      el("option", {
        value: p.id,
        text:
          iconFor(p.id).g + "  " + (p.name || p.id) + (p.external ? " · external" : ""),
      })
    ),
  ]);

  const selectField = el("div", { class: "field" }, [
    el("label", { for: "plugin-select", text: "Plugin" }),
    select,
  ]);

  const descEl = el("div", { class: "plugin-desc" });
  descEl.style.display = "none";

  const dynamic = el("form", { class: "dynamic-form" });

  panel.appendChild(selectField);
  panel.appendChild(descEl);
  panel.appendChild(dynamic);

  function showPlugin(plugin) {
    if (!plugin) {
      descEl.style.display = "none";
      clear(dynamic);
      return;
    }
    if (plugin.description) {
      descEl.textContent = plugin.description;
      descEl.style.display = "";
    } else {
      descEl.style.display = "none";
    }
    renderSchemaForm(dynamic, plugin, onDone, editing, resetToAdd);
  }

  select.addEventListener("change", () => {
    showPlugin(plugins.find((p) => p.id === select.value));
  });

  if (isEdit) {
    select.value = editing.plugin_id;
    select.disabled = true; // plugin type cannot change on an existing tracker
    const plugin = plugins.find((p) => p.id === editing.plugin_id);
    if (plugin) {
      showPlugin(plugin);
    } else {
      dynamic.appendChild(
        el("div", {
          class: "form-msg error",
          text: "Plugin '" + editing.plugin_id + "' is not registered; cannot edit.",
        })
      );
    }
  }
}

function renderSchemaForm(form, plugin, onDone, editing, resetToAdd) {
  clear(form);
  const isEdit = !!editing;
  const cfg = (editing && editing.config) || {};

  // Tracker name field
  const nameInput = el("input", {
    type: "text",
    placeholder: "e.g. My GitHub releases",
    required: true,
  });
  if (isEdit) nameInput.value = editing.name || "";
  form.appendChild(
    el("div", { class: "field" }, [
      el("label", {}, [
        document.createTextNode("Tracker name"),
        el("span", { class: "req", text: "*" }),
      ]),
      nameInput,
    ])
  );

  form.appendChild(el("div", { class: "divider" }));

  const schema = Array.isArray(plugin.schema) ? plugin.schema : [];
  // map of key -> getter()
  const getters = [];

  for (const f of schema) {
    const field = f || {};
    const key = field.key;
    if (!key) continue;
    const labelText = field.label || key;
    const type = field.type || "string";
    const required = !!field.required;

    const hasCv = Object.prototype.hasOwnProperty.call(cfg, key);

    if (type === "bool") {
      const input = el("input", { type: "checkbox" });
      if (field.default === true || field.default === "true") input.checked = true;
      if (hasCv) input.checked = cfg[key] === true || cfg[key] === "true";
      const id = "f-" + key;
      input.id = id;
      const row = el("div", { class: "field" }, [
        el("div", { class: "field-check" }, [
          input,
          el("label", { for: id, text: labelText }),
        ]),
        field.help ? el("div", { class: "help", text: field.help }) : null,
      ]);
      form.appendChild(row);
      getters.push({ key, required, type, get: () => input.checked });
      continue;
    }

    let input;
    if (type === "list") {
      input = el("textarea", {
        placeholder: field.placeholder || "One item per line",
      });
      const lv = hasCv ? cfg[key] : field.default;
      if (lv != null) {
        input.value = Array.isArray(lv) ? lv.join("\n") : String(lv);
      }
    } else if (type === "number") {
      input = el("input", {
        type: "number",
        placeholder: field.placeholder || "",
      });
      const nv = hasCv ? cfg[key] : field.default;
      if (nv != null) input.value = String(nv);
    } else if (type === "select") {
      const opts = Array.isArray(field.options) ? field.options : [];
      input = el(
        "select",
        {},
        opts.map((o) =>
          el("option", { value: o.value, text: o.label || o.value })
        )
      );
      const selv = hasCv ? cfg[key] : field.default;
      if (selv != null) input.value = String(selv);
    } else {
      // string (and any unknown) -> text input
      input = el("input", {
        type: "text",
        placeholder: field.placeholder || "",
      });
      const sv = hasCv ? cfg[key] : field.default;
      if (sv != null) input.value = String(sv);
    }
    if (required) input.required = true;

    const labelChildren = [document.createTextNode(labelText)];
    if (required) labelChildren.push(el("span", { class: "req", text: "*" }));

    form.appendChild(
      el("div", { class: "field" }, [
        el("label", {}, labelChildren),
        input,
        field.help ? el("div", { class: "help", text: field.help }) : null,
      ])
    );

    getters.push({
      key,
      required,
      type,
      get: () => {
        const raw = input.value;
        if (type === "number") {
          if (raw === "" || raw == null) return undefined;
          return Number(raw);
        }
        return raw;
      },
    });
  }

  // Per-tracker refresh interval, prefilled with the plugin's default cadence.
  form.appendChild(el("div", { class: "divider" }));
  const pluginDefault = Number(plugin.refresh_interval_seconds) || 0;
  const intervalInput = el("input", { type: "number", min: "1" });
  const editInterval = isEdit ? Number(editing.refresh_interval_seconds) || 0 : 0;
  intervalInput.value = String(editInterval > 0 ? editInterval : pluginDefault);
  form.appendChild(
    el("div", { class: "field" }, [
      el("label", {}, [document.createTextNode("Refresh interval (seconds)")]),
      intervalInput,
      el("div", {
        class: "help",
        text:
          "How often this widget auto-refreshes. Plugin default is " +
          (pluginDefault > 0 ? fmtInterval(pluginDefault) : "—") +
          ". Lower = more frequent (and more API calls).",
      }),
    ])
  );

  const submitLabel = isEdit ? "Save changes" : "Add tracker";
  const submitBtn = el("button", {
    type: "submit",
    class: "btn btn-primary",
    text: submitLabel,
  });
  const actions = [submitBtn];
  if (isEdit) {
    const cancelBtn = el("button", {
      type: "button",
      class: "btn btn-ghost",
      text: "Cancel",
    });
    cancelBtn.addEventListener("click", () => {
      if (resetToAdd) resetToAdd();
    });
    actions.push(cancelBtn);
  }
  const msg = el("div", { class: "form-msg" });
  actions.push(msg);

  form.appendChild(el("div", { class: "form-actions" }, actions));

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    msg.className = "form-msg";
    msg.textContent = "";

    const name = nameInput.value.trim();
    if (!name) {
      msg.className = "form-msg error";
      msg.textContent = "Tracker name is required.";
      return;
    }

    const config = {};
    for (const g of getters) {
      const val = g.get();
      if (g.type === "bool") {
        config[g.key] = val;
        continue;
      }
      if (g.type === "number") {
        if (val === undefined || Number.isNaN(val)) {
          if (g.required) {
            msg.className = "form-msg error";
            msg.textContent = `"${g.key}" must be a number.`;
            return;
          }
          continue;
        }
        config[g.key] = val;
        continue;
      }
      // string / list -> raw string (backend splits lists)
      if (val == null || val === "") {
        if (g.required) {
          msg.className = "form-msg error";
          msg.textContent = `"${g.key}" is required.`;
          return;
        }
        continue;
      }
      config[g.key] = val;
    }

    let refreshInterval = Math.round(Number(intervalInput.value));
    if (!Number.isFinite(refreshInterval) || refreshInterval < 1) refreshInterval = 0;

    submitBtn.disabled = true;
    submitBtn.textContent = isEdit ? "Saving…" : "Adding…";
    try {
      if (isEdit) {
        await API.updateTracker(editing.id, {
          plugin_id: plugin.id,
          name,
          config,
          refresh_interval_seconds: refreshInterval,
        });
        if (onDone) await onDone();
        if (resetToAdd) resetToAdd();
        return;
      }
      await API.createTracker({
        plugin_id: plugin.id,
        name,
        config,
        refresh_interval_seconds: refreshInterval,
      });
      msg.className = "form-msg ok";
      msg.textContent = "Tracker added.";
      form.reset();
      if (onDone) await onDone();
    } catch (e) {
      msg.className = "form-msg error";
      msg.textContent = "Failed: " + (e.message || e);
    } finally {
      submitBtn.disabled = false;
      submitBtn.textContent = submitLabel;
    }
  });
}

/* ============================================================
   Settings view
   ============================================================ */
async function renderSettings() {
  clear(main);

  main.appendChild(
    el("div", { class: "view-head" }, [
      el("div", {}, [
        el("h1", { text: "Settings" }),
        el("div", { class: "sub", text: "Dashboard preferences" }),
      ]),
    ])
  );

  const panel = el("div", { class: "panel" }, [
    el("h2", { text: "Auto-refresh" }),
    el("div", { class: "skeleton", text: "Loading…" }),
  ]);
  main.appendChild(panel);

  // External plugins panel (rescan).
  main.appendChild(buildPluginsPanel());

  let settings;
  try {
    settings = await API.getSettings();
    settingsCache = settings;
  } catch (e) {
    clear(panel);
    panel.appendChild(el("h2", { text: "Auto-refresh" }));
    panel.appendChild(
      el("div", { class: "form-msg error", text: "Failed to load: " + (e.message || e) })
    );
    return;
  }

  clear(panel);
  panel.appendChild(el("h2", { text: "Auto-refresh" }));
  panel.appendChild(
    el("div", {
      class: "sub",
      text:
        "Master switch for auto-refresh. Each widget refreshes on its own cadence — " +
        "set per tracker in Configure (defaults to the plugin's interval, shown on the widget).",
    })
  );

  const enabled = el("input", { type: "checkbox", id: "set-enabled" });
  enabled.checked = !!settings.autorefresh_enabled;
  panel.appendChild(
    el("div", { class: "field" }, [
      el("div", { class: "field-check" }, [
        enabled,
        el("label", { for: "set-enabled", text: "Enable auto-refresh" }),
      ]),
    ])
  );

  const debug = el("input", { type: "checkbox", id: "set-debug" });
  debug.checked = !!settings.debug;
  panel.appendChild(
    el("div", { class: "field" }, [
      el("div", { class: "field-check" }, [
        debug,
        el("label", { for: "set-debug", text: "Debug logging" }),
      ]),
      el("div", {
        class: "help",
        text: "Log every run, outbound query and plugin output. View them in the Logs tab.",
      }),
    ])
  );

  const uniform = el("input", { type: "checkbox", id: "set-uniform" });
  uniform.checked = !!settings.uniform_sizes;
  panel.appendChild(
    el("div", { class: "field" }, [
      el("div", { class: "field-check" }, [
        uniform,
        el("label", { for: "set-uniform", text: "Uniform widget sizes" }),
      ]),
      el("div", {
        class: "help",
        text: "Force every widget onto the same 1×1 tile. Off by default — widgets that ask for more space (wide PR lists, tall CI overviews) get it.",
      }),
    ])
  );

  // Text size: a per-browser display preference (like the theme), applied live
  // and saved to localStorage — independent of the server-side settings below.
  const fontSel = el(
    "select",
    { id: "set-fontscale" },
    [
      el("option", { value: "small", text: "Small" }),
      el("option", { value: "normal", text: "Normal" }),
      el("option", { value: "large", text: "Large" }),
    ]
  );
  fontSel.value = currentFontScale();
  fontSel.addEventListener("change", () => applyFontScale(fontSel.value));
  panel.appendChild(
    el("div", { class: "field" }, [
      el("label", { for: "set-fontscale", text: "Text size" }),
      fontSel,
      el("div", {
        class: "help",
        text: "Scales the whole dashboard. Saved in this browser (like the theme), applied instantly.",
      }),
    ])
  );

  const ghToken = el("input", {
    type: "password",
    id: "set-ghtoken",
    placeholder: "ghp_…",
    autocomplete: "off",
  });
  ghToken.value = settings.github_token || "";
  panel.appendChild(
    el("div", { class: "field" }, [
      el("label", { for: "set-ghtoken", text: "GitHub token" }),
      ghToken,
      el("div", {
        class: "help",
        text:
          "Recommended. Used by all GitHub widgets to raise the API rate limit " +
          "(60/hr unauthenticated → 5000/hr). A fine-grained read-only token is enough.",
      }),
    ])
  );

  const saveBtn = el("button", { class: "btn btn-primary", text: "Save settings" });
  const msg = el("div", { class: "form-msg" });
  panel.appendChild(el("div", { class: "form-actions" }, [saveBtn, msg]));

  saveBtn.addEventListener("click", async () => {
    msg.className = "form-msg";
    msg.textContent = "";
    saveBtn.disabled = true;
    saveBtn.textContent = "Saving…";
    try {
      const saved = await API.saveSettings({
        autorefresh_enabled: enabled.checked,
        debug: debug.checked,
        github_token: ghToken.value.trim(),
        uniform_sizes: uniform.checked,
        dashboard_order: settingsCache && settingsCache.dashboard_order,
      });
      settingsCache = saved;
      enabled.checked = !!saved.autorefresh_enabled;
      debug.checked = !!saved.debug;
      uniform.checked = !!saved.uniform_sizes;
      ghToken.value = saved.github_token || "";
      msg.className = "form-msg ok";
      msg.textContent = "Settings saved.";
    } catch (e) {
      msg.className = "form-msg error";
      msg.textContent = "Failed: " + (e.message || e);
    } finally {
      saveBtn.disabled = false;
      saveBtn.textContent = "Save settings";
    }
  });
}

// buildPluginsPanel renders the external-plugins panel with a rescan button.
function buildPluginsPanel() {
  const msg = el("div", { class: "form-msg" });
  const rescanBtn = el("button", { class: "btn", text: "Rescan plugins" });
  rescanBtn.addEventListener("click", async () => {
    msg.className = "form-msg";
    msg.textContent = "";
    rescanBtn.disabled = true;
    rescanBtn.textContent = "Rescanning…";
    try {
      const r = await API.rescanPlugins();
      pluginsCache = null; // force Configure to reload the plugin list
      msg.className = "form-msg ok";
      msg.textContent = `Scanned ${r.dir || "plugins dir"}: +${r.added} added, -${r.removed} removed.`;
    } catch (e) {
      msg.className = "form-msg error";
      msg.textContent = "Rescan failed: " + (e.message || e);
    } finally {
      rescanBtn.disabled = false;
      rescanBtn.textContent = "Rescan plugins";
    }
  });

  return el("div", { class: "panel" }, [
    el("h2", { text: "External plugins" }),
    el("div", {
      class: "sub",
      text:
        "External plugins are executables named plugdash-plugin-* in the plugins " +
        "directory. Drop one in and rescan to pick it up without a restart.",
    }),
    el("div", { class: "form-actions" }, [rescanBtn, msg]),
  ]);
}

/* ============================================================
   Logs view
   ============================================================ */
async function renderLogs() {
  clear(main);

  const refreshBtn = el("button", { class: "btn btn-primary", text: "Refresh" });
  const clearBtn = el("button", { class: "btn btn-ghost", text: "Clear" });
  const status = el("div", { class: "sub" });

  main.appendChild(
    el("div", { class: "view-head" }, [
      el("div", {}, [el("h1", { text: "Logs" }), status]),
      el("div", { class: "view-actions" }, [clearBtn, refreshBtn]),
    ])
  );

  const body = el("div", { class: "panel" });
  main.appendChild(body);

  async function load() {
    clear(body);
    body.appendChild(el("div", { class: "skeleton", text: "Loading…" }));
    let data;
    try {
      data = await API.getLogs();
    } catch (e) {
      clear(body);
      body.appendChild(el("div", { class: "form-msg error", text: "Failed: " + (e.message || e) }));
      return;
    }
    const entries = Array.isArray(data) ? data : data.entries || [];
    const debugOn = !Array.isArray(data) && data.debug;
    status.textContent = debugOn
      ? "Debug logging is ON · " + entries.length + " entries"
      : "Debug logging is OFF (enable it in Settings to capture query-level detail) · " +
        entries.length +
        " entries";

    clear(body);
    if (!entries.length) {
      body.appendChild(el("div", { class: "skeleton", text: "No log entries yet." }));
      return;
    }
    const list = el("div", { class: "log-list" });
    // newest first
    for (const e of entries.slice().reverse()) {
      list.appendChild(renderLogEntry(e));
    }
    body.appendChild(list);
  }

  refreshBtn.addEventListener("click", load);
  clearBtn.addEventListener("click", async () => {
    try {
      await API.clearLogs();
      await load();
    } catch (e) {
      alert("Clear failed: " + (e.message || e));
    }
  });
  await load();
  // Poll so entries stream in as trackers run (cleared when leaving the view).
  clearLogsTimer();
  logsTimer = setInterval(load, 3000);
}

function renderLogEntry(e) {
  const lvl = String(e.level || "INFO").toUpperCase();
  const time = e.time ? new Date(e.time).toLocaleTimeString() : "";
  const attrs = e.attrs || {};
  const attrStr = Object.keys(attrs)
    .filter((k) => attrs[k] !== "" && attrs[k] != null)
    .map((k) => k + "=" + cellText(attrs[k]))
    .join("  ");
  return el("div", { class: "log-row log-" + lvl.toLowerCase() }, [
    el("span", { class: "log-time", text: time }),
    el("span", { class: "log-level", text: lvl }),
    el("span", { class: "log-msg", text: String(e.msg || "") }),
    attrStr ? el("span", { class: "log-attrs", text: attrStr }) : null,
  ]);
}

/* ============================================================
   Theme (dark / light)
   ============================================================ */
const THEME_KEY = "plugdash:theme";

function currentTheme() {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

function applyTheme(t) {
  if (t === "light") document.documentElement.dataset.theme = "light";
  else delete document.documentElement.dataset.theme;
  const btn = document.getElementById("theme-toggle");
  if (btn) btn.textContent = t === "light" ? "☀️" : "🌙";
  try {
    localStorage.setItem(THEME_KEY, t);
  } catch (e) {
    /* ignore */
  }
}

/* ---------- text size (per-browser display preference) ---------- */
const FONTSCALE_KEY = "plugdash:fontscale";

function currentFontScale() {
  const v = document.documentElement.dataset.fontScale;
  return v === "small" || v === "large" ? v : "normal";
}

function applyFontScale(scale) {
  if (scale === "small" || scale === "large") {
    document.documentElement.dataset.fontScale = scale;
  } else {
    delete document.documentElement.dataset.fontScale;
  }
  try {
    localStorage.setItem(FONTSCALE_KEY, scale);
  } catch (e) {
    /* ignore */
  }
}

function setupTheme() {
  applyTheme(currentTheme()); // sync the toggle icon with the pre-paint state
  const btn = document.getElementById("theme-toggle");
  if (btn) {
    btn.addEventListener("click", () =>
      applyTheme(currentTheme() === "light" ? "dark" : "light")
    );
  }
}

/* ============================================================
   Konami code easter egg (on the Settings page) → party mode + confetti
   ============================================================ */
const KONAMI = [
  "ArrowUp", "ArrowUp", "ArrowDown", "ArrowDown",
  "ArrowLeft", "ArrowRight", "ArrowLeft", "ArrowRight", "b", "a",
];
let konamiPos = 0;

function setupKonami() {
  document.addEventListener("keydown", (e) => {
    // Only armed on the Settings page.
    if (currentView !== "settings") {
      konamiPos = 0;
      return;
    }
    const k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    if (k === KONAMI[konamiPos]) {
      konamiPos++;
      if (konamiPos === KONAMI.length) {
        konamiPos = 0;
        triggerKonami();
      }
    } else {
      konamiPos = k === KONAMI[0] ? 1 : 0;
    }
  });
}

function triggerKonami() {
  const on = document.documentElement.classList.toggle("party");
  if (on) {
    startConfetti();
    toast("🎉 Party mode unlocked! ↑↑↓↓←→←→BA");
  } else {
    stopConfetti();
    toast("Party mode off");
  }
}

function toast(msg) {
  const t = el("div", { class: "pd-toast", text: msg });
  document.body.appendChild(t);
  setTimeout(() => {
    t.classList.add("out");
    setTimeout(() => t.remove(), 400);
  }, 2400);
}

// Continuous, dependency-free confetti that runs for as long as party mode is
// on. startConfetti keeps emitting from the top; stopConfetti stops emitting
// and lets the last pieces fall out before removing the canvas.
const CONFETTI_COLORS = ["#5b9dff", "#3fb950", "#f85149", "#d29922", "#a371f7", "#2dd4bf", "#f0883e"];
let confetti = null;

function spawnConfetti(W) {
  return {
    x: Math.random() * W,
    y: -20,
    r: 4 + Math.random() * 5,
    vx: (Math.random() - 0.5) * 3,
    vy: 2 + Math.random() * 4.5,
    rot: Math.random() * Math.PI,
    vr: (Math.random() - 0.5) * 0.3,
    color: CONFETTI_COLORS[(Math.random() * CONFETTI_COLORS.length) | 0],
  };
}

function startConfetti() {
  if (confetti) return; // already running
  const canvas = el("canvas", { class: "pd-confetti" });
  document.body.appendChild(canvas);
  const ctx = canvas.getContext("2d");
  const dpr = window.devicePixelRatio || 1;
  const resize = () => {
    canvas.width = window.innerWidth * dpr;
    canvas.height = window.innerHeight * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  };
  resize();
  window.addEventListener("resize", resize);

  const state = { canvas, emitting: true, parts: [], resize };
  confetti = state;

  // Seed an initial burst.
  for (let i = 0; i < 120; i++) {
    const p = spawnConfetti(window.innerWidth);
    p.y = -Math.random() * window.innerHeight;
    state.parts.push(p);
  }

  function frame() {
    const W = window.innerWidth,
      H = window.innerHeight;
    ctx.clearRect(0, 0, W, H);
    if (state.emitting && state.parts.length < 320) {
      for (let i = 0; i < 5; i++) state.parts.push(spawnConfetti(W));
    }
    state.parts = state.parts.filter((p) => p.y < H + 30);
    for (const p of state.parts) {
      p.x += p.vx;
      p.y += p.vy;
      p.vy += 0.05;
      p.rot += p.vr;
      ctx.save();
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rot);
      ctx.fillStyle = p.color;
      ctx.fillRect(-p.r / 2, -p.r / 2, p.r, p.r * 1.6);
      ctx.restore();
    }
    if (state.emitting || state.parts.length > 0) {
      state.raf = requestAnimationFrame(frame);
    } else {
      window.removeEventListener("resize", state.resize);
      state.canvas.remove();
      if (confetti === state) confetti = null;
    }
  }
  state.raf = requestAnimationFrame(frame);
}

function stopConfetti() {
  if (confetti) confetti.emitting = false; // stop spawning; existing pieces fall out
}

/* ============================================================
   Router / nav
   ============================================================ */
const VIEWS = ["dashboard", "configure", "settings", "logs"];

function setView(view) {
  if (!VIEWS.includes(view)) view = "dashboard";
  currentView = view;
  if (location.hash.slice(1) !== view) location.hash = view; // deep-linkable
  main.className = "main view-" + view; // lets CSS widen the dashboard, narrow forms
  stopUpdates();
  clearLogsTimer();
  for (const btn of nav.querySelectorAll(".nav-btn")) {
    btn.classList.toggle("active", btn.dataset.view === view);
  }
  if (view === "configure") {
    renderConfigure();
  } else if (view === "settings") {
    renderSettings();
  } else if (view === "logs") {
    renderLogs();
  } else {
    renderDashboard();
  }
}

nav.addEventListener("click", (ev) => {
  const btn = ev.target.closest(".nav-btn");
  if (!btn) return;
  const view = btn.dataset.view;
  if (view && view !== currentView) setView(view);
});

// React to hash changes (back/forward, deep links).
window.addEventListener("hashchange", () => {
  const view = location.hash.slice(1);
  if (VIEWS.includes(view) && view !== currentView) setView(view);
});

// initial — honor a deep-linked view in the URL hash.
setupTheme();
setupKonami();
startUpdatedTicker();
setView(location.hash.slice(1) || "dashboard");
