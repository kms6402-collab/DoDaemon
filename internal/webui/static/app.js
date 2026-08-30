(function () {
  const state = window.__DODAEMON__ || { hostname: "", server_addr: "", services: [], recent_events: [] };
  const servicesByKey = {};
  state.services.forEach((s) => (servicesByKey[s.key] = s));

  let selected = (state.services.find((s) => s.enabled) || state.services[0] || {}).key || "tftp";
  let levelFilters = { info: true, warn: true, error: true };
  let autoscroll = true;

  // Per-source running stats, kept for every source regardless of which
  // one is currently selected so switching tabs shows correct numbers.
  const stats = {}; // key -> {completed, errors, lastActivity}
  const active = new Map(); // "source|remoteAddr|file" -> row
  const logBuffer = []; // capped list of all received events, newest first
  const MAX_LOG = 500;
  let rxTotal = 0, txTotal = 0; // bytes received/sent by the server, session-cumulative

  function statsFor(source) {
    if (!stats[source]) stats[source] = { completed: 0, errors: 0, lastActivity: null };
    return stats[source];
  }

  function $(id) { return document.getElementById(id); }
  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  function fmtTime(iso) {
    return new Date(iso).toLocaleTimeString(undefined, { hour12: false });
  }
  function fmtBytes(n) {
    if (!n || n <= 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n.toFixed(0) : n.toFixed(1)) + " " + units[i];
  }
  function fmtSpeed(bps) {
    if (!bps || bps <= 0) return "-";
    return fmtBytes(bps) + "/s";
  }
  function fmtDuration(sec) {
    if (!isFinite(sec) || sec < 0) return "-";
    sec = Math.round(sec);
    const m = Math.floor(sec / 60), s = sec % 60;
    return String(m).padStart(2, "0") + ":" + String(s).padStart(2, "0");
  }

  // ---------- Title bar clock ----------

  $("tb-ip").textContent = state.server_addr || "-";
  function tickClock() {
    const el = $("tb-clock");
    if (el) el.textContent = new Date().toLocaleTimeString(undefined, { hour12: false });
  }
  tickClock();
  setInterval(tickClock, 1000);

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
      renderDirPanel();
    });
  });

  function throughputFor(source) {
    let sum = 0;
    active.forEach((v) => { if (v.source === source && v.speed) sum += v.speed; });
    return sum;
  }

  function renderSidebarRates() {
    state.services.forEach((s) => {
      const el = document.getElementById("rate-" + s.key);
      if (!el) return;
      const bps = throughputFor(s.key);
      el.textContent = bps > 0 ? fmtSpeed(bps) : "—";
    });
  }

  // ---------- Directory / permission quick panel ----------

  function renderDirPanel() {
    const svc = servicesByKey[selected];
    const dirSection = $("dir-section");
    const permSection = $("perm-section");
    $("dir-listing").hidden = true;
    $("dir-listing").innerHTML = "";

    if (!svc || !svc.dir) {
      dirSection.hidden = true;
    } else {
      dirSection.hidden = false;
      $("dir-path").textContent = svc.dir;
    }

    if (selected === "tftp") {
      permSection.hidden = false;
      const mode = svc && svc.perm_mode ? svc.perm_mode : "rw";
      document.querySelectorAll('input[name="perm-mode"]').forEach((r) => (r.checked = r.value === mode));
    } else {
      permSection.hidden = true;
    }
  }

  document.querySelectorAll('input[name="perm-mode"]').forEach((r) => {
    r.addEventListener("change", () => {
      if (!r.checked) return;
      const allowRead = r.value === "rw" || r.value === "ro";
      const allowWrite = r.value === "rw" || r.value === "wo";
      patchSettings({ tftp_allow_read: allowRead, tftp_allow_write: allowWrite }).then(() => toast("TFTP 권한이 변경되었습니다.", false));
    });
  });

  $("dir-change").addEventListener("click", () => {
    const svc = servicesByKey[selected];
    if (!svc) return;
    openBrowseModal(svc.dir || "", (chosenPath) => {
      const fieldByKey = { tftp: "tftp_root_dir", ftp: "ftp_anonymous_home_dir", syslog: "syslog_log_dir" };
      const field = fieldByKey[selected];
      if (!field) return;
      patchSettings({ [field]: chosenPath }).then(() => {
        svc.dir = chosenPath;
        renderDirPanel();
        toast("디렉터리가 변경되었습니다.", false);
      });
    });
  });

  $("dir-open").addEventListener("click", () => {
    const svc = servicesByKey[selected];
    if (!svc || !svc.dir) return;
    const box = $("dir-listing");
    if (!box.hidden) { box.hidden = true; return; }
    fetch("/api/browse?path=" + encodeURIComponent(svc.dir))
      .then((r) => r.json())
      .then((data) => {
        if (data.error) { toast(data.error, true); return; }
        box.hidden = false;
        if (!data.dirs || data.dirs.length === 0) {
          box.innerHTML = '<div class="empty">하위 폴더가 없습니다.</div>';
        } else {
          box.innerHTML = data.dirs.map((d) => '<div class="row">📁 ' + escapeHTML(d) + "</div>").join("");
        }
      })
      .catch(() => toast("폴더 내용을 불러오지 못했습니다.", true));
  });

  // ---------- Minimal folder-browse modal (mirrors settings.js) ----------

  let browseState = { path: "" };
  let browseCallback = null;

  function openBrowseModal(startPath, onChoose) {
    browseCallback = onChoose;
    $("browse-modal-overlay").classList.add("open");
    loadBrowse(startPath);
  }
  function loadBrowseDir(path) {
    const url = "/api/browse" + (path ? "?path=" + encodeURIComponent(path) : "");
    return fetch(url).then((r) => r.json());
  }
  function loadBrowse(path) {
    loadBrowseDir(path).then((data) => {
      if (data.error) return toast(data.error, true);
      browseState = data;
      $("browse-current-path").textContent = data.path || "-";
      $("browse-drives").innerHTML = (data.drives || [])
        .map((d) => '<span class="drive-chip" data-drive="' + escapeHTML(d) + '">' + escapeHTML(d) + "</span>")
        .join("");
      $("browse-drives").querySelectorAll("[data-drive]").forEach((el) => el.addEventListener("click", () => loadBrowse(el.dataset.drive)));
      const list = $("browse-list");
      if (!data.dirs || data.dirs.length === 0) {
        list.innerHTML = '<div class="empty" style="padding:14px 0;">하위 폴더가 없습니다.</div>';
      } else {
        list.innerHTML = data.dirs.map((d) => '<div class="browse-row" data-dir="' + escapeHTML(d) + '">📁 ' + escapeHTML(d) + "</div>").join("");
        list.querySelectorAll("[data-dir]").forEach((el) => el.addEventListener("dblclick", () => loadBrowse(joinPath(data.path, el.dataset.dir))));
      }
    });
  }
  function joinPath(base, name) {
    if (!base) return name;
    const sep = base.includes("\\") ? "\\" : "/";
    return base.endsWith(sep) ? base + name : base + sep + name;
  }
  $("browse-up").addEventListener("click", () => { if (browseState.parent) loadBrowse(browseState.parent); });
  $("browse-cancel").addEventListener("click", () => $("browse-modal-overlay").classList.remove("open"));
  $("browse-select").addEventListener("click", () => {
    $("browse-modal-overlay").classList.remove("open");
    if (browseCallback) browseCallback(browseState.path || "");
  });

  // ---------- Settings patch helper (GET full DTO, mutate, POST back) ----------

  function patchSettings(patch) {
    return fetch("/api/settings")
      .then((r) => r.json())
      .then((dto) => {
        Object.assign(dto, patch);
        return fetch("/api/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(dto) });
      })
      .then(async (r) => {
        const body = await r.json();
        if (!r.ok || !body.ok) throw new Error(body.error || "설정을 저장하지 못했습니다.");
      })
      .catch((err) => { toast(err.message, true); throw err; });
  }

  function setServiceEnabled(key, enabled) {
    const fieldByKey = { tftp: "tftp_enabled", ftp: "ftp_enabled", syslog: "syslog_enabled", webui: "web_enabled" };
    const field = fieldByKey[key];
    if (!field) return Promise.resolve();
    return patchSettings({ [field]: enabled });
  }

  $("btn-stop").addEventListener("click", () => {
    setServiceEnabled(selected, false).then(() => toast("서비스를 정지했습니다.", false));
  });
  $("btn-restart").addEventListener("click", () => {
    setServiceEnabled(selected, false)
      .then(() => new Promise((res) => setTimeout(res, 400)))
      .then(() => setServiceEnabled(selected, true))
      .then(() => toast("서비스를 재시작했습니다.", false));
  });

  // ---------- Toast ----------

  function toast(message, isError) {
    let el = $("toast");
    if (!el) {
      el = document.createElement("div");
      el.id = "toast";
      el.className = "toast";
      document.body.appendChild(el);
    }
    el.textContent = message;
    el.className = "toast show" + (isError ? " error" : " ok");
    setTimeout(() => (el.className = "toast"), 3000);
  }

  // ---------- Detail header ----------

  function renderDetail() {
    const svc = servicesByKey[selected];
    const title = $("detail-title");
    const meta = $("detail-meta");
    const status = $("detail-status");
    if (!svc) return;
    title.textContent = svc.name;
    meta.textContent = svc.meta;
    status.textContent = svc.enabled ? "실행 중" : "중지됨";
    status.style.color = svc.enabled ? "var(--ok)" : "var(--text-dim)";

    const st = statsFor(selected);
    $("kpi-active").textContent = countActiveTransfers(selected);
    $("kpi-total").textContent = st.completed;
    $("kpi-errors").textContent = st.errors;
    const tp = throughputFor(selected) / (1024 * 1024);
    $("kpi-throughput").textContent = tp.toFixed(1);
  }

  function countActiveTransfers(source) {
    let n = 0;
    active.forEach((v) => { if (v.source === source && v.kind === "전송") n++; });
    return n;
  }

  // ---------- Active transfers table ----------

  function activeKeyFor(ev) {
    return ev.source + "|" + (ev.remote_addr || "") + "|" + (ev.fields && ev.fields.file ? ev.fields.file : "");
  }

  function renderActive() {
    const tbody = $("active-tbody");
    const rows = Array.from(active.values()).filter((v) => v.source === selected && v.kind === "전송");
    $("active-count").textContent = rows.length + " 세션";

    if (rows.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">활성 세션이 없습니다.</td></tr>';
      return;
    }
    rows.sort((a, b) => b.startedAt - a.startedAt);
    tbody.innerHTML = rows.map(renderActiveRow).join("");
  }

  function renderActiveRow(r) {
    const pct = r.bytesTotal > 0 ? Math.min(100, Math.round((r.bytesDone / r.bytesTotal) * 100)) : null;
    const progressHTML = pct === null
      ? '<span class="dir-tag">' + fmtBytes(r.bytesDone) + "</span>"
      : '<div class="progress-cell"><div class="progress-track"><div class="progress-fill" style="width:' + pct + '%"></div></div><span class="progress-pct">' + pct + "%</span></div>";
    const remaining = pct !== null && r.speed > 0 ? fmtDuration((r.bytesTotal - r.bytesDone) / r.speed) : "-";
    return (
      "<tr><td class=\"file\">" + escapeHTML(r.detail || "-") + "</td>" +
      "<td>" + escapeHTML(r.remoteAddr || "-") + "</td>" +
      "<td><span class=\"dir-tag\">" + escapeHTML(r.direction || "-") + "</span></td>" +
      "<td>" + progressHTML + "</td>" +
      "<td class=\"speed\">" + fmtSpeed(r.speed) + "</td>" +
      "<td class=\"remaining\">" + remaining + "</td></tr>"
    );
  }

  // ---------- Event log ----------

  function severityOf(ev) {
    if (ev.kind === "error") return "error";
    if (/warn|재시도|retransmit|timeout|타임아웃/i.test(ev.message || "")) return "warn";
    return "info";
  }

  function passesFilter(ev) {
    if (ev.source !== selected) return false;
    if (ev.fields && ev.fields.progress) return false; // progress ticks don't clutter the log
    return !!levelFilters[severityOf(ev)];
  }

  function renderLog() {
    const viewer = $("log-viewer");
    const rows = logBuffer.filter(passesFilter).slice(0, 300);
    if (rows.length === 0) {
      viewer.innerHTML = '<div class="empty">표시할 이벤트가 없습니다.</div>';
      return;
    }
    viewer.innerHTML = rows.map(renderLogRow).join("");
  }

  function renderLogRow(ev) {
    const sev = severityOf(ev);
    return (
      '<div class="log-row' + (sev !== "info" ? " severity-" + sev : "") + '">' +
      '<span class="log-time">' + fmtTime(ev.time) + "</span>" +
      '<span class="log-source">' + escapeHTML(ev.source) + "</span>" +
      '<span class="log-level ' + sev + '">' + sev.toUpperCase() + "</span>" +
      '<span class="log-msg">' + escapeHTML(ev.message || "") +
      (ev.remote_addr ? ' <span class="log-time">(' + escapeHTML(ev.remote_addr) + ")</span>" : "") +
      "</span></div>"
    );
  }

  function appendLogRowLive(ev) {
    if (!passesFilter(ev)) return;
    const viewer = $("log-viewer");
    const empty = viewer.querySelector(".empty");
    if (empty) empty.remove();
    viewer.insertAdjacentHTML("afterbegin", renderLogRow(ev));
    while (viewer.children.length > 300) viewer.removeChild(viewer.lastChild);
    if (autoscroll) viewer.scrollTop = 0;
  }

  $("log-filters").addEventListener("click", (e) => {
    const btn = e.target.closest(".filter-pill");
    if (!btn) return;
    const level = btn.dataset.level;
    levelFilters[level] = !levelFilters[level];
    btn.classList.toggle("active", levelFilters[level]);
    renderLog();
  });

  $("autoscroll-btn").addEventListener("click", () => {
    autoscroll = !autoscroll;
    $("autoscroll-btn").textContent = autoscroll ? "자동 스크롤 켜짐" : "자동 스크롤 꺼짐";
  });

  // ---------- Active-session bookkeeping from event kinds ----------
  // TFTP/FTP publish "... 시작"/"started" and "... 완료"/"complete" text in
  // Message (see internal/tftp/session.go, internal/ftp/permfs.go), plus
  // throttled "... 진행" progress events carrying bytes_done/bytes_total in
  // Fields; we key on remote+file rather than trying to parse a transfer id.

  function trackActivity(ev) {
    const key = activeKeyFor(ev);
    const msg = ev.message || "";
    const isStart = /started|시작/.test(msg);
    const isProgress = !!(ev.fields && ev.fields.progress);
    const isEnd = !isProgress && (/complete|완료|error|실패/.test(msg) || ev.kind === "error" || ev.kind === "disconnect");

    if (ev.kind === "connect") {
      active.set(ev.source + "|" + ev.remote_addr + "|", {
        source: ev.source, remoteAddr: ev.remote_addr, kind: "연결", direction: "", detail: "", startedAt: new Date(ev.time),
        bytesDone: 0, bytesTotal: 0, speed: 0,
      });
    } else if (ev.kind === "disconnect") {
      active.delete(ev.source + "|" + ev.remote_addr + "|");
    } else if (ev.kind === "transfer" && isStart) {
      const m = msg.match(/(GET|PUT|다운로드|업로드)/);
      let direction = "GET";
      if (m && (m[1] === "PUT" || m[1] === "업로드")) direction = "PUT";
      const file = (ev.fields && ev.fields.file) || msg.replace(/^.*?:\s*/, "");
      active.set(key, {
        source: ev.source, remoteAddr: ev.remote_addr, kind: "전송",
        direction, detail: file, startedAt: new Date(ev.time),
        bytesDone: 0, bytesTotal: 0, speed: 0, _prevBytes: 0, _prevTime: new Date(ev.time).getTime(),
      });
    } else if (ev.kind === "transfer" && isProgress) {
      const existing = active.get(key);
      const done = (ev.fields.bytes_done) || 0;
      const total = (ev.fields.bytes_total) || 0;
      const now = new Date(ev.time).getTime();
      const base = existing || {
        source: ev.source, remoteAddr: ev.remote_addr, kind: "전송", direction: "GET",
        detail: (ev.fields && ev.fields.file) || "", startedAt: new Date(ev.time), _prevBytes: 0, _prevTime: now,
      };
      const dt = (now - (base._prevTime || now)) / 1000;
      const delta = Math.max(0, done - (base._prevBytes || 0));
      const speed = dt > 0 ? delta / dt : base.speed || 0;
      if (base.direction === "PUT") rxTotal += delta; else txTotal += delta;
      active.set(key, Object.assign({}, base, { bytesDone: done, bytesTotal: total, speed, _prevBytes: done, _prevTime: now }));
    } else if (isEnd) {
      active.delete(key);
    }
  }

  function handleEvent(ev) {
    const st = statsFor(ev.source);
    st.lastActivity = ev.time;
    if (ev.kind === "error") st.errors++;
    if (/complete|완료/.test(ev.message || "") && ev.kind === "transfer") st.completed++;

    trackActivity(ev);

    if (!(ev.fields && ev.fields.progress)) {
      logBuffer.unshift(ev);
      if (logBuffer.length > MAX_LOG) logBuffer.length = MAX_LOG;
    }

    if (ev.source === selected) {
      renderActive();
      appendLogRowLive(ev);
      renderDetail();
    }
    renderSidebarRates();
    updateFooter();
  }

  function updateFooter() {
    $("fb-rx").textContent = fmtBytes(rxTotal);
    $("fb-tx").textContent = fmtBytes(txTotal);
    const svc = servicesByKey[selected];
    $("fb-status").textContent = svc ? svc.name + " " + (svc.enabled ? "정상 · 리스너 응답 중" : "중지됨") : "-";
  }

  function refreshServices() {
    fetch("/api/services")
      .then((r) => r.json())
      .then((data) => {
        data.services.forEach((s) => (servicesByKey[s.key] = s));
        renderSidebar();
        renderDetail();
        renderDirPanel();
        updateFooter();
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

  // Seed from the server-rendered snapshot (newest first already) so the
  // log/active/KPI views are populated before the first SSE frame arrives.
  (state.recent_events || []).slice().reverse().forEach((ev) => {
    const st = statsFor(ev.source);
    st.lastActivity = ev.time;
    if (ev.kind === "error") st.errors++;
    if (/complete|완료/.test(ev.message || "") && ev.kind === "transfer") st.completed++;
    trackActivity(ev);
    if (!(ev.fields && ev.fields.progress)) logBuffer.unshift(ev);
  });

  renderSidebar();
  renderDetail();
  renderActive();
  renderLog();
  renderDirPanel();
  renderSidebarRates();
  updateFooter();
})();
