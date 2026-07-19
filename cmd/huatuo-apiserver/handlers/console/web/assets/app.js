/*
 * HuaTuo Console - single-page application.
 *
 * Vanilla JS, no build step. Authenticated API calls carry the stored API key
 * in the Authorization header, matching the huatuo-apiserver auth model.
 */
(function () {
  "use strict";

  const STORAGE_KEY = "huatuo_api_key";

  const NAV = [
    { id: "dashboard", label: "Dashboard", admin: false },
    { id: "tasks", label: "Tasks", admin: false },
    { id: "tracing", label: "Event Tracing", admin: false },
    { id: "profiling", label: "Profiling", admin: false },
    { id: "metrics", label: "Metrics", admin: false },
    { id: "access", label: "Access Control", admin: true },
  ];

  const state = {
    apiKey: localStorage.getItem(STORAGE_KEY) || "",
    user: null,
    system: null,
    pollTimers: [],
  };

  /* ------------------------------------------------------------------ */
  /* Tiny helpers                                                        */
  /* ------------------------------------------------------------------ */

  const $ = (sel, root = document) => root.querySelector(sel);
  const el = (tag, attrs = {}, ...children) => {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (k === "class") node.className = v;
      else if (k === "html") node.innerHTML = v;
      else if (k.startsWith("on") && typeof v === "function")
        node.addEventListener(k.slice(2).toLowerCase(), v);
      else if (v !== null && v !== undefined) node.setAttribute(k, v);
    }
    for (const c of children) {
      if (c === null || c === undefined) continue;
      node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return node;
  };

  function escapeHtml(s) {
    if (s === null || s === undefined) return "";
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function statusBadge(status) {
    const s = String(status || "").toLowerCase();
    return `<span class="badge badge-${escapeHtml(s) || "muted"}">${escapeHtml(
      status || "unknown"
    )}</span>`;
  }

  function relativeTime(iso) {
    if (!iso) return "-";
    const d = new Date(iso);
    if (isNaN(d)) return iso;
    return d.toLocaleString();
  }

  function toast(message, type = "info") {
    const host = $("#toast-host") || (() => {
      const h = el("div", { class: "toast-host", id: "toast-host" });
      document.body.appendChild(h);
      return h;
    })();
    const t = el("div", { class: `toast toast-${type}` }, message);
    host.appendChild(t);
    setTimeout(() => t.remove(), 4000);
  }

  /* ------------------------------------------------------------------ */
  /* API client                                                          */
  /* ------------------------------------------------------------------ */

  async function api(path, opts = {}) {
    const headers = Object.assign({}, opts.headers || {});
    if (state.apiKey) headers["Authorization"] = state.apiKey;
    if (opts.body && !headers["Content-Type"])
      headers["Content-Type"] = "application/json";

    const resp = await fetch(path, {
      method: opts.method || "GET",
      headers,
      body: opts.body ? JSON.stringify(opts.body) : undefined,
    });

    if (resp.status === 204) return null;

    const text = await resp.text();
    const ct = resp.headers.get("content-type") || "";
    if (!ct.includes("application/json")) {
      if (!resp.ok) throw new Error(text || resp.statusText);
      return text;
    }

    let payload;
    try {
      payload = text ? JSON.parse(text) : {};
    } catch {
      throw new Error("invalid JSON response");
    }

    if (!resp.ok) {
      const msg = payload && payload.message ? payload.message : resp.statusText;
      const err = new Error(msg);
      err.status = resp.status;
      err.payload = payload;
      throw err;
    }

    if (payload && typeof payload === "object" && "code" in payload) {
      if (payload.code !== 0) {
        throw new Error(payload.message || "request failed");
      }
      return payload.data;
    }
    return payload;
  }

  /* ------------------------------------------------------------------ */
  /* Bootstrapping / navigation                                          */
  /* ------------------------------------------------------------------ */

  function stopPolling() {
    state.pollTimers.forEach((t) => clearInterval(t));
    state.pollTimers = [];
  }

  function pollEvery(fn, ms) {
    fn();
    const id = setInterval(fn, ms);
    state.pollTimers.push(id);
  }

  async function bootstrap() {
    if (!state.apiKey) {
      renderLogin();
      return;
    }
    try {
      state.user = await api("/v1/auth/whoami");
      try {
        state.system = await api("/v1/system/info");
      } catch {
        state.system = null;
      }
      showChrome();
      route();
    } catch (err) {
      if (err.status === 401 || err.status === 403) {
        toast("API key is invalid or unauthorized", "error");
      }
      state.apiKey = "";
      localStorage.removeItem(STORAGE_KEY);
      renderLogin();
    }
  }

  function showChrome() {
    const topbar = $(".topbar");
    topbar.hidden = false;

    const nav = $("#nav");
    nav.innerHTML = "";
    for (const item of NAV) {
      if (item.admin && !(state.user && state.user.is_admin)) continue;
      const link = el(
        "button",
        {
          class: "nav-link",
          "data-nav": item.id,
          onclick: () => {
            location.hash = "#/" + item.id;
          },
        },
        item.label
      );
      nav.appendChild(link);
    }

    const badge = $("#user-badge");
    badge.innerHTML = "";
    badge.appendChild(document.createTextNode(state.user.name || state.user.id || "user"));
    if (state.user.is_admin) {
      badge.appendChild(el("span", { class: "admin-tag" }, "admin"));
    }

    const versionEl = $("#server-version");
    if (state.system && state.system.version) {
      versionEl.textContent =
        "HuaTuo apiserver v" + state.system.version +
        (state.system.commit ? " (" + state.system.commit + ")" : "");
    }

    $("#logout-btn").onclick = () => {
      stopPolling();
      state.apiKey = "";
      state.user = null;
      localStorage.removeItem(STORAGE_KEY);
      renderLogin();
    };
  }

  function route() {
    stopPolling();
    const hash = location.hash.replace(/^#\/?/, "") || "dashboard";
    document.querySelectorAll(".nav-link").forEach((n) => {
      n.classList.toggle("active", n.getAttribute("data-nav") === hash);
    });
    const view = $("#view");
    view.innerHTML = "";
    try {
      switch (hash) {
        case "dashboard":
          return renderDashboard(view);
        case "tasks":
          return renderTasks(view);
        case "tracing":
          return renderTracing(view);
        case "profiling":
          return renderProfiling(view);
        case "metrics":
          return renderMetrics(view);
        case "access":
          if (!(state.user && state.user.is_admin)) {
            view.appendChild(emptyState("Access Control is available to administrators only."));
            return;
          }
          return renderAccess(view);
        default:
          return renderDashboard(view);
      }
    } catch (err) {
      view.appendChild(alertBox(err.message, "error"));
    }
  }

  window.addEventListener("hashchange", route);

  /* ------------------------------------------------------------------ */
  /* Shared UI fragments                                                 */
  /* ------------------------------------------------------------------ */

  function alertBox(message, type = "info") {
    return el("div", { class: `alert alert-${type}` }, message);
  }

  function emptyState(message) {
    return el("div", { class: "empty" }, message);
  }

  function pageHeader(title, subtitle) {
    return el("div", {}, el("h2", { class: "page-title" }, title), el("p", { class: "page-subtitle" }, subtitle));
  }

  function panel(title, bodyNodes) {
    return el("section", { class: "card" }, el("p", { class: "card-title" }, title), ...bodyNodes);
  }

  function metricCard(value, label, opts = {}) {
    return el("div", { class: "card" }, el("div", { class: "metric-value", style: opts.color ? `color:${opts.color}` : "" }, String(value)), el("div", { class: "metric-label" }, label));
  }

  /* ------------------------------------------------------------------ */
  /* Login view                                                          */
  /* ------------------------------------------------------------------ */

  function renderLogin() {
    $(".topbar").hidden = true;
    const view = $("#view");
    view.innerHTML = "";
    const card = el("div", { class: "login-card" });
    card.appendChild(el("h1", {}, "HuaTuo Console"));
    card.appendChild(el("p", {}, "Sign in with your API key. Administrators provision keys from Access Control."));

    let input;
    let submitBtn;

    const form = el("form", { class: "form-grid", onsubmit: async (e) => {
      e.preventDefault();
      const key = input.value.trim();
      if (!key) return;
      submitBtn.disabled = true;
      try {
        state.apiKey = key;
        localStorage.setItem(STORAGE_KEY, key);
        state.user = await api("/v1/auth/whoami");
        try {
          state.system = await api("/v1/system/info");
        } catch {
          state.system = null;
        }
        showChrome();
        location.hash = "#/dashboard";
        route();
      } catch (err) {
        state.apiKey = "";
        localStorage.removeItem(STORAGE_KEY);
        card.insertBefore(alertBox(err.message || "Login failed", "error"), form);
      } finally {
        submitBtn.disabled = false;
      }
    } });

    const field = el("div", { class: "field" }, el("label", { for: "api-key" }, "API key"), (input = el("input", { id: "api-key", type: "password", placeholder: "htk_...", autocomplete: "current-password", required: true })), el("div", { class: "hint" }, "The API key is the credential sent in the Authorization header."));
    form.appendChild(field);

    submitBtn = el("button", { class: "btn btn-primary", type: "submit" }, "Sign in");
    form.appendChild(el("div", { class: "form-actions" }, submitBtn));
    card.appendChild(form);
    view.appendChild(el("div", { class: "login-wrap" }, card));
  }

  /* ------------------------------------------------------------------ */
  /* Dashboard view                                                      */
  /* ------------------------------------------------------------------ */

  async function renderDashboard(root) {
    root.appendChild(pageHeader("Dashboard", "Unified overview of integrated observability modules."));

    let info = state.system;
    try {
      info = await api("/v1/system/info");
      state.system = info;
    } catch (err) {
      root.appendChild(alertBox("Failed to load system info: " + err.message, "error"));
    }

    const totals = { tracing: 0, profiling: 0, running: 0 };
    try {
      const traces = await api("/v1/traces?limit=500");
      totals.tracing = traces.total;
      totals.running += countRunning(traces.items);
    } catch {
      /* non-fatal */
    }
    try {
      const profiles = await api("/v1/profiles?limit=500");
      totals.profiling = profiles.total;
      totals.running += countRunning(profiles.items);
    } catch {
      /* non-fatal */
    }

    const cards = el("div", { class: "grid grid-4" });
    cards.appendChild(metricCard(totals.running, "Running jobs", { color: "var(--green)" }));
    cards.appendChild(metricCard(totals.tracing, "Tracing jobs"));
    cards.appendChild(metricCard(totals.profiling, "Profiling jobs"));
    cards.appendChild(metricCard(info ? info.version : "-", "Server version"));
    root.appendChild(cards);

    if (info && info.modules && info.modules.length) {
      const grid = el("div", { class: "grid grid-3" });
      for (const m of info.modules) {
        const card = el("div", { class: "card module-card" });
        card.appendChild(el("div", { class: "row" }, el("span", { class: "module-name" }, m.display_name || m.name), el("span", { class: "spacer" }), el("span", { class: m.enabled ? "badge badge-completed" : "badge badge-muted" }, m.enabled ? "enabled" : "disabled")));
        card.appendChild(el("div", { class: "module-desc" }, m.description || ""));
        card.appendChild(el("div", { class: "row" }, el("span", { class: "mono perm-tag" }, m.endpoint || "-"), el("a", { class: "btn btn-ghost btn-sm", href: moduleHref(m.name) }, "Open")));
        grid.appendChild(card);
      }
      root.appendChild(el("div", { class: "section" }, panel("Integrated modules", [grid])));
    }

    if (info && info.limits) {
      const limits = info.limits;
      root.appendChild(el("div", { class: "section" }, panel("Scheduling limits", [el("div", { class: "grid grid-4" }, metricCard(limits.max_profiling_tasks_per_host, "Profiling / host"), metricCard(limits.max_tracing_tasks_per_host, "Tracing / host"), metricCard(limits.max_total_profiling_tasks, "Total profiling"), metricCard(limits.max_total_tracing_tasks, "Total tracing"))])));
    }
  }

  function countRunning(items) {
    if (!items) return 0;
    return items.filter((i) => String(i.status).toLowerCase() === "running").length;
  }

  function moduleHref(name) {
    return "#/" + (name === "tracing" ? "tracing" : name === "profiling" ? "profiling" : name === "metrics" ? "metrics" : "dashboard");
  }

  /* ------------------------------------------------------------------ */
  /* Tasks view (unified)                                                */
  /* ------------------------------------------------------------------ */

  async function renderTasks(root) {
    root.appendChild(pageHeader("Tasks", "All collection jobs across tracing and profiling, with live status."));

    const toolbar = el("div", { class: "toolbar" });
    const statusSel = el("select", { class: "field" });
    for (const s of ["", "running", "pending", "completed", "failed", "stopped", "timeout"]) {
      statusSel.appendChild(el("option", { value: s }, s || "all statuses"));
    }
    const hostInput = el("input", { type: "text", placeholder: "filter by host" });
    const reload = el("button", { class: "btn btn-ghost btn-sm", onclick: () => load() }, "Refresh");
    toolbar.appendChild(el("label", {}, "Status", statusSel));
    toolbar.appendChild(el("label", {}, "Host", hostInput));
    toolbar.appendChild(el("span", { class: "spacer" }));
    toolbar.appendChild(reload);
    root.appendChild(toolbar);

    const tableHost = el("div", { class: "table-wrap" });
    root.appendChild(tableHost);

    async function load() {
      try {
        const params = new URLSearchParams();
        params.set("limit", "200");
        if (statusSel.value) params.set("status", statusSel.value);
        if (hostInput.value.trim()) params.set("host", hostInput.value.trim());
        const q = "?" + params.toString();

        let traces = [];
        let profiles = [];
        try {
          const t = await api("/v1/traces" + q);
          traces = (t.items || []).map((i) => Object.assign({}, i, { _kind: "trace" }));
        } catch (err) {
          tableHost.replaceChildren(alertBox("Failed to load traces: " + err.message, "error"));
        }
        try {
          const p = await api("/v1/profiles" + q);
          profiles = (p.items || []).map((i) => Object.assign({}, i, { _kind: "profile" }));
        } catch (err) {
          tableHost.replaceChildren(alertBox("Failed to load profiles: " + err.message, "error"));
        }

        const all = traces.concat(profiles).sort((a, b) => (b.start_time || "").localeCompare(a.start_time || ""));
        renderTaskTable(tableHost, all);
      } catch (err) {
        tableHost.replaceChildren(alertBox(err.message, "error"));
      }
    }

    pollEvery(load, 5000);
  }

  function renderTaskTable(host, rows) {
    if (!rows.length) {
      host.replaceChildren(emptyState("No jobs match the current filters."));
      return;
    }
    const table = el("table", { class: "data" });
    table.appendChild(el("thead", {}, el("tr", {}, el("th", {}, "Module"), el("th", {}, "ID"), el("th", {}, "Host"), el("th", {}, "Container"), el("th", {}, "Status"), el("th", {}, "Started"), el("th", {}, "Result"))));
    const tbody = el("tbody");
    for (const r of rows) {
      const resultUrl = r.results && r.results.url;
      const resultCell = resultUrl
        ? el("a", { class: "btn btn-ghost btn-sm", href: resultUrl, target: "_blank", rel: "noopener" }, "Open")
        : el("span", { class: "badge badge-muted" }, "-");
      tbody.appendChild(el("tr", {}, el("td", {}, r._kind === "profile" ? "Profiling" : "Tracing"), el("td", { class: "mono" }, r.id), el("td", {}, r.hostname || "-"), el("td", {}, r.container || "-"), el("td", { html: statusBadge(r.status) }), el("td", {}, relativeTime(r.start_time)), el("td", {}, resultCell)));
    }
    table.appendChild(tbody);
    host.replaceChildren(table);
  }

  /* ------------------------------------------------------------------ */
  /* Event Tracing view                                                  */
  /* ------------------------------------------------------------------ */

  async function renderTracing(root) {
    root.appendChild(pageHeader("Event Tracing", "Create, monitor and stop on-demand tracing jobs."));

    root.appendChild(buildTraceForm());

    const listHost = el("div", { class: "section" });
    root.appendChild(listHost);

    async function load() {
      try {
        const data = await api("/v1/traces?limit=100");
        renderJobList(listHost, "Tracing jobs", data.items || [], {
          kind: "trace",
          onStop: stopJob("/v1/traces"),
          onDelete: deleteJob("/v1/traces"),
        });
      } catch (err) {
        listHost.replaceChildren(alertBox(err.message, "error"));
      }
    }
    pollEvery(load, 5000);
  }

  function buildTraceForm() {
    const card = panel("Start a trace", []);
    const host = el("input", { type: "text", placeholder: "host name", required: true });
    const container = el("input", { type: "text", placeholder: "container (optional)" });
    const duration = el("input", { type: "number", min: "1", max: "300", value: "30", required: true });
    const typeSel = el("select", {});
    for (const t of ["tracing", "syscall", "network"]) {
      typeSel.appendChild(el("option", { value: t }, t));
    }

    const form = el("form", { class: "form-grid", onsubmit: async (e) => {
      e.preventDefault();
      const btn = form.querySelector("button[type=submit]");
      btn.disabled = true;
      try {
        await api("/v1/traces", {
          method: "POST",
          body: {
            hostname: host.value.trim(),
            container: container.value.trim(),
            duration: Number(duration.value),
            type: typeSel.value,
          },
        });
        toast("Trace job created", "success");
        form.reset();
        location.hash = "#/tracing";
      } catch (err) {
        toast(err.message, "error");
      } finally {
        btn.disabled = false;
      }
    } });

    const grid = el("div", { class: "grid grid-4" });
    grid.appendChild(fieldWrap("Host", host));
    grid.appendChild(fieldWrap("Container", container));
    grid.appendChild(fieldWrap("Duration (s)", duration, "Maximum 300 seconds."));
    grid.appendChild(fieldWrap("Type", typeSel));
    form.appendChild(grid);
    form.appendChild(el("div", { class: "form-actions" }, el("button", { class: "btn btn-primary", type: "submit" }, "Start trace")));
    card.replaceChildren(el("p", { class: "card-title" }, "Start a trace"), form);
    return card;
  }

  /* ------------------------------------------------------------------ */
  /* Profiling view                                                      */
  /* ------------------------------------------------------------------ */

  async function renderProfiling(root) {
    root.appendChild(pageHeader("Continuous Profiling", "CPU and memory profiling with flame-graph integration."));

    let caps = null;
    try {
      caps = await api("/v1/profiles/capabilities");
    } catch (err) {
      root.appendChild(alertBox("Failed to load capabilities: " + err.message, "error"));
    }

    root.appendChild(buildProfilingForm(caps));

    const listHost = el("div", { class: "section" });
    root.appendChild(listHost);

    async function load() {
      try {
        const data = await api("/v1/profiles?limit=100");
        renderJobList(listHost, "Profiling jobs", data.items || [], {
          kind: "profile",
          onStop: stopJob("/v1/profiles"),
          onDelete: deleteJob("/v1/profiles"),
        });
      } catch (err) {
        listHost.replaceChildren(alertBox(err.message, "error"));
      }
    }
    pollEvery(load, 5000);
  }

  function buildProfilingForm(caps) {
    const card = panel("Start profiling", []);
    const typeSel = el("select", {});
    const profileTypes = (caps && caps.profile_types) || ["cpu", "memory"];
    for (const t of profileTypes) {
      typeSel.appendChild(el("option", { value: t }, t));
    }

    const host = el("input", { type: "text", placeholder: "host name", required: true });
    const container = el("input", { type: "text", placeholder: "container (optional)" });
    const execPath = el("input", { type: "text", placeholder: "/usr/bin/myapp" });
    const langSel = el("select", {});
    const modeSel = el("select", {});
    const duration = el("input", { type: "number", min: "1", value: "60", required: true });

    const cpuLangs = (caps && caps.cpu_supported_languages) || [];
    const memLangs = (caps && caps.memory_supported_languages) || [];
    const modes = (caps && caps.memory_modes) || {};

    function refreshControls() {
      const t = typeSel.value;
      langSel.innerHTML = "";
      modeSel.innerHTML = "";
      const langs = t === "memory" ? memLangs : cpuLangs;
      for (const l of langs) langSel.appendChild(el("option", { value: l }, l));
      if (t === "memory") {
        for (const [display, internal] of Object.entries(modes)) {
          modeSel.appendChild(el("option", { value: display }, display));
        }
      }
    }
    typeSel.addEventListener("change", refreshControls);
    refreshControls();

    const form = el("form", { class: "form-grid", onsubmit: async (e) => {
      e.preventDefault();
      const btn = form.querySelector("button[type=submit]");
      btn.disabled = true;
      try {
        const body = {
          hostname: host.value.trim(),
          container: container.value.trim(),
          duration: Number(duration.value),
          type: typeSel.value,
        };
        if (typeSel.value === "cpu") {
          body.target_exec_path = execPath.value.trim();
          body.target_process_language = langSel.value;
        } else {
          body.target_process_language = langSel.value;
          body.memory_mode = modeSel.value;
        }
        await api("/v1/profiles", { method: "POST", body });
        toast("Profiling job created", "success");
        form.reset();
        refreshControls();
      } catch (err) {
        toast(err.message, "error");
      } finally {
        btn.disabled = false;
      }
    } });

    const grid = el("div", { class: "grid grid-4" });
    grid.appendChild(fieldWrap("Host", host));
    grid.appendChild(fieldWrap("Container", container));
    grid.appendChild(fieldWrap("Profile type", typeSel));
    grid.appendChild(fieldWrap("Duration (s)", duration));
    grid.appendChild(fieldWrap("Executable path", execPath, "CPU profiling target executable."));
    grid.appendChild(fieldWrap("Language", langSel));
    grid.appendChild(fieldWrap("Memory mode", modeSel, "Only used for memory profiling."));
    form.appendChild(grid);
    form.appendChild(el("div", { class: "form-actions" }, el("button", { class: "btn btn-primary", type: "submit" }, "Start profiling")));
    card.replaceChildren(el("p", { class: "card-title" }, "Start profiling"), form);
    return card;
  }

  /* ------------------------------------------------------------------ */
  /* Shared job list with stop/delete                                    */
  /* ------------------------------------------------------------------ */

  function renderJobList(host, title, items, opts) {
    const card = el("section", { class: "card" });
    card.appendChild(el("p", { class: "card-title" }, title));
    if (!items.length) {
      card.appendChild(emptyState("No jobs yet."));
      host.replaceChildren(card);
      return;
    }
    const table = el("table", { class: "data" });
    table.appendChild(el("thead", {}, el("tr", {}, el("th", {}, "ID"), el("th", {}, "Host"), el("th", {}, "Status"), el("th", {}, "Started"), el("th", {}, "Result"), el("th", {}, "Actions"))));
    const tbody = el("tbody");
    for (const job of items) {
      const status = String(job.status).toLowerCase();
      const isActive = status === "running" || status === "pending";
      const resultUrl = job.results && job.results.url;
      const actions = el("div", { class: "row" });
      if (isActive) {
        actions.appendChild(el("button", { class: "btn btn-ghost btn-sm", onclick: () => opts.onStop(job) }, "Stop"));
      }
      actions.appendChild(el("button", { class: "btn btn-danger btn-sm", onclick: () => opts.onDelete(job) }, "Delete"));
      const resultCell = resultUrl
        ? el("a", { class: "btn btn-ghost btn-sm", href: resultUrl, target: "_blank", rel: "noopener" }, "Open")
        : el("span", { class: "badge badge-muted" }, "-");
      tbody.appendChild(el("tr", {}, el("td", { class: "mono" }, job.id), el("td", {}, job.hostname || "-"), el("td", { html: statusBadge(job.status) }), el("td", {}, relativeTime(job.start_time)), el("td", {}, resultCell), el("td", {}, actions)));
    }
    table.appendChild(tbody);
    card.appendChild(table);
    host.replaceChildren(card);
  }

  function stopJob(base) {
    return async (job) => {
      try {
        await api(base + "/" + encodeURIComponent(job.id), { method: "PATCH", body: { status: "stopped" } });
        toast("Job " + job.id + " stop requested", "success");
        route();
      } catch (err) {
        toast(err.message, "error");
      }
    };
  }

  function deleteJob(base) {
    return async (job) => {
      if (!confirm("Delete job " + job.id + "?")) return;
      try {
        await api(base + "/" + encodeURIComponent(job.id), { method: "DELETE" });
        toast("Job " + job.id + " deleted", "success");
        route();
      } catch (err) {
        toast(err.message, "error");
      }
    };
  }

  /* ------------------------------------------------------------------ */
  /* Metrics view                                                        */
  /* ------------------------------------------------------------------ */

  async function renderMetrics(root) {
    root.appendChild(pageHeader("Metrics", "Prometheus exposition surface scraped from /metrics."));
    const toolbar = el("div", { class: "toolbar" });
    const refresh = el("button", { class: "btn btn-ghost btn-sm", onclick: load }, "Refresh");
    const filter = el("input", { type: "text", placeholder: "filter metric families" });
    toolbar.appendChild(el("label", {}, "Filter", filter));
    toolbar.appendChild(el("span", { class: "spacer" }));
    toolbar.appendChild(refresh);
    root.appendChild(toolbar);

    const host = el("div", { class: "section" });
    root.appendChild(host);

    async function load() {
      try {
        const text = await api("/metrics");
        const families = parsePrometheus(text);
        const needle = filter.value.trim().toLowerCase();
        const rows = needle ? families.filter((f) => f.name.toLowerCase().includes(needle)) : families;
        renderMetricsTable(host, rows, text);
      } catch (err) {
        host.replaceChildren(alertBox("Failed to load metrics: " + err.message, "error"));
      }
    }
    filter.addEventListener("input", load);
    pollEvery(load, 10000);
  }

  function parsePrometheus(text) {
    const families = [];
    let current = null;
    for (const line of text.split("\n")) {
      if (!line) continue;
      if (line.startsWith("# HELP ")) {
        const rest = line.slice(8);
        const sp = rest.indexOf(" ");
        const name = rest.slice(0, sp);
        const help = rest.slice(sp + 1);
        current = { name, help, samples: [] };
        families.push(current);
      } else if (line.startsWith("# TYPE ") || line.startsWith("#")) {
        continue;
      } else {
        const m = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+([-+eE0-9.]+)/);
        if (m) {
          if (!current || current.name.split("_")[0] !== m[1].split("_")[0]) {
            // best-effort grouping
          }
          const fam = families.find((f) => f.name === m[1]) || (() => {
            const f = { name: m[1], help: "", samples: [] };
            families.push(f);
            return f;
          })();
          fam.samples.push({ labels: m[2] || "", value: m[3] });
        }
      }
    }
    return families.sort((a, b) => a.name.localeCompare(b.name));
  }

  function renderMetricsTable(host, families, raw) {
    const card = el("section", { class: "card" });
    card.appendChild(el("p", { class: "card-title" }, families.length + " metric families"));
    if (!families.length) {
      card.appendChild(emptyState("No metric families match the filter."));
      host.replaceChildren(card);
      return;
    }
    const table = el("table", { class: "data" });
    table.appendChild(el("thead", {}, el("tr", {}, el("th", {}, "Metric"), el("th", {}, "Help"), el("th", {}, "Samples"))));
    const tbody = el("tbody");
    for (const f of families) {
      tbody.appendChild(el("tr", {}, el("td", { class: "mono" }, f.name), el("td", {}, escapeHtml(f.help)), el("td", {}, String(f.samples.length))));
    }
    table.appendChild(tbody);
    card.appendChild(table);
    card.appendChild(el("div", { class: "row", style: "margin-top:12px;justify-content:flex-end" }, el("a", { class: "btn btn-ghost btn-sm", href: "/metrics", target: "_blank" }, "Open raw /metrics")));
    host.replaceChildren(card);
  }

  /* ------------------------------------------------------------------ */
  /* Access Control view (RBAC)                                          */
  /* ------------------------------------------------------------------ */

  async function renderAccess(root) {
    root.appendChild(pageHeader("Access Control", "Manage users, roles and API keys."));

    root.appendChild(buildCreateUserForm());

    const rolesHost = el("div", { class: "section" });
    root.appendChild(rolesHost);
    const usersHost = el("div", { class: "section" });
    root.appendChild(usersHost);

    async function load() {
      try {
        const roles = await api("/v1/roles");
        renderRoles(rolesHost, roles || []);
      } catch (err) {
        rolesHost.replaceChildren(alertBox("Failed to load roles: " + err.message, "error"));
      }
      try {
        const users = await api("/v1/users");
        renderUsers(usersHost, users || []);
      } catch (err) {
        usersHost.replaceChildren(alertBox("Failed to load users: " + err.message, "error"));
      }
    }
    load();
  }

  function renderRoles(host, roles) {
    const card = el("section", { class: "card" });
    card.appendChild(el("p", { class: "card-title" }, "Role catalog"));
    if (!roles.length) {
      card.appendChild(emptyState("No roles defined."));
      host.replaceChildren(card);
      return;
    }
    for (const r of roles) {
      const block = el("div", { class: "section" }, el("div", { class: "row" }, el("strong", {}, r.name), r.is_admin ? el("span", { class: "badge badge-completed" }, "admin") : null), el("div", { class: "metric-label", style: "margin-top:6px" }, r.description || ""));
      const tags = el("div", { class: "tag-list", style: "margin-top:8px" });
      for (const p of r.permissions || []) tags.appendChild(el("span", { class: "perm-tag" }, p));
      block.appendChild(tags);
      card.appendChild(block);
    }
    host.replaceChildren(card);
  }

  function renderUsers(host, users) {
    const card = el("section", { class: "card" });
    card.appendChild(el("p", { class: "card-title" }, "Users & API keys (" + users.length + ")"));
    if (!users.length) {
      card.appendChild(emptyState("No users configured."));
      host.replaceChildren(card);
      return;
    }
    const table = el("table", { class: "data" });
    table.appendChild(el("thead", {}, el("tr", {}, el("th", {}, "Name"), el("th", {}, "ID"), el("th", {}, "Role"), el("th", {}, "Permissions"), el("th", {}, "Actions"))));
    const tbody = el("tbody");
    for (const u of users) {
      const actions = el("div", { class: "row" });
      if (u.id !== state.user.id) {
        actions.appendChild(el("button", { class: "btn btn-danger btn-sm", onclick: () => deleteUser(u) }, "Revoke"));
      } else {
        actions.appendChild(el("span", { class: "badge badge-muted" }, "you"));
      }
      const perms = el("div", { class: "tag-list" });
      if (u.is_admin) {
        perms.appendChild(el("span", { class: "perm-tag" }, "admin (all)"));
      } else {
        for (const p of u.permissions || []) perms.appendChild(el("span", { class: "perm-tag" }, p));
      }
      tbody.appendChild(el("tr", {}, el("td", {}, u.name || "-"), el("td", { class: "mono" }, maskKey(u.id)), el("td", {}, u.is_admin ? "admin" : "custom"), el("td", {}, perms), el("td", {}, actions)));
    }
    table.appendChild(tbody);
    card.appendChild(table);
    host.replaceChildren(card);
  }

  function maskKey(id) {
    if (!id) return "-";
    if (id.length <= 8) return id;
    return id.slice(0, 4) + "..." + id.slice(-4);
  }

  function buildCreateUserForm() {
    const card = panel("Provision API key", []);
    const name = el("input", { type: "text", required: true, placeholder: "display name" });
    const roleSel = el("select", {});
    roleSel.appendChild(el("option", { value: "viewer" }, "viewer"));
    roleSel.appendChild(el("option", { value: "operator" }, "operator"));
    roleSel.appendChild(el("option", { value: "admin" }, "admin"));
    const generate = el("input", { type: "checkbox", checked: true });
    const resultBox = el("div");

    const form = el("form", { class: "form-grid", onsubmit: async (e) => {
      e.preventDefault();
      resultBox.innerHTML = "";
      const btn = form.querySelector("button[type=submit]");
      btn.disabled = true;
      try {
        const role = roleSel.value;
        const body = { name: name.value.trim(), generate_key: generate.checked };
        if (role === "admin") {
          body.is_admin = true;
        } else {
          const catalog = await loadRoleCatalog();
          const tmpl = (catalog || []).find((r) => r.name === role);
          body.is_admin = false;
          body.permissions = tmpl ? tmpl.permissions : [];
        }
        const created = await api("/v1/users", { method: "POST", body });
        toast("API key created", "success");
        resultBox.appendChild(alertBox("Store this key securely. It will not be shown again.", "success"));
        resultBox.appendChild(el("div", { class: "code-block" }, created.id));
        form.reset();
        generate.checked = true;
        route();
      } catch (err) {
        toast(err.message, "error");
        resultBox.appendChild(alertBox(err.message, "error"));
      } finally {
        btn.disabled = false;
      }
    } });

    const grid = el("div", { class: "grid grid-3" });
    grid.appendChild(fieldWrap("Name", name));
    grid.appendChild(fieldWrap("Role", roleSel));
    grid.appendChild(fieldWrap("Generate key", generate, "Generate a random secret API key."));
    form.appendChild(grid);
    form.appendChild(el("div", { class: "form-actions" }, el("button", { class: "btn btn-primary", type: "submit" }, "Create API key")));
    form.appendChild(resultBox);
    card.replaceChildren(el("p", { class: "card-title" }, "Provision API key"), form);
    return card;
  }

  let roleCatalogCache = null;
  async function loadRoleCatalog() {
    if (roleCatalogCache) return roleCatalogCache;
    try {
      roleCatalogCache = await api("/v1/roles");
    } catch {
      roleCatalogCache = [];
    }
    return roleCatalogCache;
  }

  async function deleteUser(u) {
    if (!confirm("Revoke API key for " + (u.name || u.id) + "?")) return;
    try {
      await api("/v1/users/" + encodeURIComponent(u.id), { method: "DELETE" });
      toast("API key revoked", "success");
      route();
    } catch (err) {
      toast(err.message, "error");
    }
  }

  /* ------------------------------------------------------------------ */
  /* Field helper                                                        */
  /* ------------------------------------------------------------------ */

  function fieldWrap(labelText, control, hint) {
    const f = el("div", { class: "field" });
    f.appendChild(el("label", {}, labelText));
    f.appendChild(control);
    if (hint) f.appendChild(el("div", { class: "hint" }, hint));
    return f;
  }

  /* ------------------------------------------------------------------ */
  /* Go                                                                  */
  /* ------------------------------------------------------------------ */

  bootstrap();
})();
