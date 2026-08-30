(function () {
  const state = window.__DODAEMON__ || { hostname: "", services: [] };
  const servicesByKey = {};
  state.services.forEach((s) => (servicesByKey[s.key] = s));

  let selected = (state.services.find((s) => s.enabled) || state.services[0] || {}).key || "tftp";
  let levelFilter = "all";

  // Per-source running stats, kept for every source regardless of which
  // one is currently selected so switching tabs shows correct numbers.
  const stats = {}; // key -> {total, errors, lastActivity}
  const active = new Map(); // "source|remoteAddr|file" -> {source, remoteAddr, kind, detail, startedAt}
  const logBuffer = []; // capped list of all received events, newest first
  const MAX_LOG = 500;

  function statsFor(source) {
    if (!stats[source]) stats[source] = { total: 0, errors: 0, lastActivity: null };
    return stats[source];
  }

  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  function fmtTime(iso) {
    return new Date(iso).toLocaleTimeString(undefined, { hour12: false });
  }

  // ---------- Sidebar ----------

  function renderSidebar() {
    document.querySelectorAll(".service-item").forEach((el) => {
      const key = el.dataset.key;
      const svc = servicesByKey[key];
      el.classList.toggle("active", key === selected);
      el.classList.toggle("on", !!(svc && svc.enabled));
    });
  }

  document.querySelectorAll(".service-item").forEach((el) => {
    el.addEventListener("click", () => {
      selected = el.dataset.key;
      renderSidebar();
      renderDetail();
      renderActive();
      renderLog();
    });
  });

  // ---------- Detail header ----------

  function renderDetail() {
    const svc = servicesByKey[selected];
    const title = document.getElementById("detail-title");
    const meta = document.getElementById("detail-meta");
    const status = document.getElementById("detail-status");
    if (!svc) return;
    title.textContent = svc.name;
    meta.textContent = svc.meta;
    status.className = "badge " + (svc.enabled ? "on" : "off");
    status.innerHTML = '<span class="dot"></span>' + (svc.enabled ? "실행 중" : "중지됨");

    const st = statsFor(selected);
    document.getElementById("kpi-active").textContent = countActive(selected);
    document.getElementById("kpi-total").textContent = st.total;
    document.getElementById("kpi-errors").textContent = st.errors;
    document.getElementById("kpi-lastactivity").textContent = st.lastActivity ? fmtTime(st.lastActivity) : "-";
  }

  function countActive(source) {
    let n = 0;
    active.forEach((v) => {
      if (v.source === source) n++;
    });
    return n;
  }

  // ---------- Active sessions table ----------

  function activeKeyFor(ev) {
    return ev.source + "|" + (ev.remote_addr || "") + "|" + (ev.fields && ev.fields.file ? ev.fields.file : "");
  }

  function renderActive() {
    const tbody = document.getElementById("active-tbody");
    const rows = Array.from(active.values()).filter((v) => v.source === selected);
    document.getElementById("active-count-badge").innerHTML = '<span class="dot"></span>' + rows.length + "개";

    if (rows.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty">활성 세션이 없습니다.</td></tr>';
      return;
    }
    rows.sort((a, b) => b.startedAt - a.startedAt);
    tbody.innerHTML = rows
      .map((r) => {
        const dirPill = r.direction ? '<span class="dir-pill ' + r.direction.toLowerCase() + '">' + r.direction + "</span>" : "";
        return (
          "<tr><td>" + escapeHTML(r.remoteAddr || "-") + "</td><td>" + dirPill + " " + escapeHTML(r.kind) +
          "</td><td>" + escapeHTML(r.detail || "-") + "</td><td>" + fmtTime(r.startedAt) + "</td></tr>"
        );
      })
      .join("");
  }

  // ---------- Event log ----------

  function passesFilter(ev) {
    if (ev.source !== selected) return false;
    if (levelFilter === "all") return true;
    const isErr = ev.kind === "error";
    return levelFilter === "error" ? isErr : !isErr;
  }

  function renderLog() {
    const viewer = document.getElementById("log-viewer");
    const rows = logBuffer.filter(passesFilter).slice(0, 300);
    if (rows.length === 0) {
      viewer.innerHTML = '<div class="empty">표시할 이벤트가 없습니다.</div>';
      return;
    }
    viewer.innerHTML = rows.map(renderLogRow).join("");
  }

  function renderLogRow(ev) {
    const isErr = ev.kind === "error";
    return (
      '<div class="log-row' + (isErr ? " severity-err" : "") + '">' +
      '<span class="log-time">' + fmtTime(ev.time) + "</span>" +
      '<span class="log-source">' + escapeHTML(ev.source) + "</span>" +
      '<span class="log-level ' + (isErr ? "error" : "info") + '">' + (isErr ? "ERR" : "INFO") + "</span>" +
      '<span class="log-msg">' + escapeHTML(ev.message || "") +
      (ev.remote_addr ? ' <span class="log-time">(' + escapeHTML(ev.remote_addr) + ")</span>" : "") +
      "</span></div>"
    );
  }

  function appendLogRowLive(ev) {
    if (!passesFilter(ev)) return;
    const viewer = document.getElementById("log-viewer");
    const empty = viewer.querySelector(".empty");
    if (empty) empty.remove();
    viewer.insertAdjacentHTML("afterbegin", renderLogRow(ev));
    while (viewer.children.length > 300) viewer.removeChild(viewer.lastChild);
    if (document.getElementById("autoscroll").checked) viewer.scrollTop = 0;
  }

  document.getElementById("log-filters").addEventListener("click", (e) => {
    const btn = e.target.closest(".filter-pill");
    if (!btn) return;
    levelFilter = btn.dataset.level;
    document.querySelectorAll(".filter-pill").forEach((p) => p.classList.toggle("active", p === btn));
    renderLog();
  });

  // ---------- Active-session bookkeeping from event kinds ----------
  // TFTP/FTP publish "... 시작"/"started" and "... 완료"/"complete" text in
  // Message (see internal/tftp/session.go, internal/ftp/permfs.go); we key
  // on remote+file rather than trying to parse a transfer id.

  function trackActivity(ev) {
    const key = activeKeyFor(ev);
    const msg = ev.message || "";
    const isStart = /started|시작/.test(msg);
    const isEnd = /complete|완료|error|실패/.test(msg) || ev.kind === "error" || ev.kind === "disconnect";

    if (ev.kind === "connect") {
      active.set(ev.source + "|" + ev.remote_addr + "|", {
        source: ev.source, remoteAddr: ev.remote_addr, kind: "연결", direction: "", detail: "", startedAt: new Date(ev.time),
      });
    } else if (ev.kind === "disconnect") {
      active.delete(ev.source + "|" + ev.remote_addr + "|");
    } else if (ev.kind === "transfer" && isStart) {
      const m = msg.match(/(GET|PUT|다운로드|업로드)/);
      let direction = "GET";
      if (m && (m[1] === "PUT" || m[1] === "업로드")) direction = "PUT";
      active.set(key, {
        source: ev.source, remoteAddr: ev.remote_addr, kind: "전송",
        direction, detail: msg.replace(/^.*?:\s*/, ""), startedAt: new Date(ev.time),
      });
    } else if (isEnd) {
      active.delete(key);
      // Also drop a possible bare-file-less start row for the same peer/source.
    }
  }

  function handleEvent(ev) {
    const st = statsFor(ev.source);
    st.lastActivity = ev.time;
    if (ev.kind === "connect") st.total++;
    if (ev.kind === "error") st.errors++;

    trackActivity(ev);

    logBuffer.unshift(ev);
    if (logBuffer.length > MAX_LOG) logBuffer.length = MAX_LOG;

    if (ev.source === selected) {
      renderActive();
      appendLogRowLive(ev);
      document.getElementById("kpi-active").textContent = countActive(selected);
      document.getElementById("kpi-total").textContent = st.total;
      document.getElementById("kpi-errors").textContent = st.errors;
      document.getElementById("kpi-lastactivity").textContent = fmtTime(st.lastActivity);
    }
  }

  function refreshServices() {
    fetch("/api/services")
      .then((r) => r.json())
      .then((data) => {
        data.services.forEach((s) => (servicesByKey[s.key] = s));
        renderSidebar();
        renderDetail();
      })
      .catch(() => {});
  }

  // ---------- SSE ----------

  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    try {
      const ev = JSON.parse(e.data);
      if (ev.kind === "config") {
        refreshServices();
        return;
      }
      handleEvent(ev);
    } catch (err) {
      /* ignore malformed frame */
    }
  };
  es.onerror = () => {
    const badge = document.getElementById("conn-badge");
    const dot = document.getElementById("conn-dot");
    if (badge) badge.classList.replace("on", "err");
    if (dot) dot.style.background = "var(--err)";
  };

  // Seed from the server-rendered snapshot (newest first already) so the
  // log/active/KPI views are populated before the first SSE frame arrives.
  (state.recent_events || []).slice().reverse().forEach((ev) => {
    const st = statsFor(ev.source);
    st.lastActivity = ev.time;
    if (ev.kind === "connect") st.total++;
    if (ev.kind === "error") st.errors++;
    trackActivity(ev);
    logBuffer.unshift(ev);
  });

  renderSidebar();
  renderDetail();
  renderActive();
  renderLog();
})();
