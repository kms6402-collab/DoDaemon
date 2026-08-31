(function () {
  // Must stay in sync with internal/webui/settings.go's permsToString/stringToPerms
  // comment, which documents the same "elradfmwMT" code set from internal/auth.
  const PERM_CODES = [
    { code: "e", label: "디렉터리 이동" },
    { code: "l", label: "목록 조회" },
    { code: "r", label: "다운로드" },
    { code: "a", label: "이어받기" },
    { code: "d", label: "삭제" },
    { code: "f", label: "이름변경" },
    { code: "m", label: "폴더생성" },
    { code: "w", label: "업로드" },
    { code: "M", label: "권한변경" },
    { code: "T", label: "시간변경" },
  ];
  const PERM_FULL = PERM_CODES.map((p) => p.code);
  const PERM_READONLY = ["l", "r"];

  let users = []; // [{username, new_password, home_dir, permissions: []}]
  let editingIndex = -1;
  let browseTargetId = null;
  let browseState = { path: "", drives: [] };

  function $(id) { return document.getElementById(id); }
  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }

  // ---------- Tabs ----------

  document.querySelectorAll(".settings-tab").forEach((tab) => {
    tab.addEventListener("click", () => {
      document.querySelectorAll(".settings-tab").forEach((t) => t.classList.toggle("active", t === tab));
      const page = tab.dataset.tab;
      document.querySelectorAll(".settings-page").forEach((p) => p.classList.toggle("active", p.dataset.page === page));
    });
  });

  // ---------- Toast ----------

  function toast(message, isError) {
    const el = $("toast");
    el.textContent = message;
    el.className = "toast show" + (isError ? " error" : " ok");
    setTimeout(() => (el.className = "toast"), 3000);
  }

  // ---------- FTP user list ----------

  function permsSummary(codes) {
    if (codes.length === PERM_FULL.length) return "전체 권한";
    return codes.join("");
  }

  function renderUsers() {
    const box = $("ftp-user-list");
    if (users.length === 0) {
      box.innerHTML = '<div class="empty" style="padding:14px 0;">등록된 계정이 없습니다.</div>';
      return;
    }
    box.innerHTML = users
      .map(
        (u, i) =>
          '<div class="user-row"><div><div class="u-name">' + escapeHTML(u.username) +
          '</div><div class="u-home">' + escapeHTML(u.home_dir) + " · " + escapeHTML(permsSummary(u.permissions)) + '</div></div>' +
          '<div class="u-actions"><button type="button" class="btn small" data-edit="' + i + '">편집</button>' +
          '<button type="button" class="btn small danger" data-del="' + i + '">삭제</button></div></div>'
      )
      .join("");
    box.querySelectorAll("[data-edit]").forEach((btn) => btn.addEventListener("click", () => openUserModal(parseInt(btn.dataset.edit, 10))));
    box.querySelectorAll("[data-del]").forEach((btn) =>
      btn.addEventListener("click", () => {
        users.splice(parseInt(btn.dataset.del, 10), 1);
        renderUsers();
      })
    );
  }

  function renderPermGrid(selected) {
    const box = $("user-modal-perm-grid");
    box.innerHTML = PERM_CODES.map(
      (p) =>
        '<label class="perm-check"><input type="checkbox" value="' + p.code + '"' +
        (selected.includes(p.code) ? " checked" : "") + '> ' + p.code + " · " + p.label + "</label>"
    ).join("");
  }
  function readPermGrid() {
    return Array.from($("user-modal-perm-grid").querySelectorAll("input:checked")).map((el) => el.value);
  }

  function openUserModal(index) {
    editingIndex = index;
    const isNew = index < 0;
    $("user-modal-title").textContent = isNew ? "FTP 계정 추가" : "FTP 계정 편집";
    $("user-modal-pw-label").textContent = isNew ? "비밀번호" : "비밀번호 (변경하려면 입력, 비워두면 유지)";
    const u = isNew ? { username: "", home_dir: "", permissions: PERM_FULL.slice() } : users[index];
    $("user-modal-username").value = u.username;
    $("user-modal-password").value = "";
    $("user-modal-homedir").value = u.home_dir;
    renderPermGrid(u.permissions);
    $("user-modal-overlay").classList.add("open");
  }
  function closeUserModal() { $("user-modal-overlay").classList.remove("open"); }

  $("ftp-user-add").addEventListener("click", () => openUserModal(-1));
  $("user-modal-cancel").addEventListener("click", closeUserModal);
  $("user-modal-full").addEventListener("click", () => renderPermGrid(PERM_FULL));
  $("user-modal-ro").addEventListener("click", () => renderPermGrid(PERM_READONLY));

  $("user-modal-ok").addEventListener("click", () => {
    const username = $("user-modal-username").value.trim();
    const homeDir = $("user-modal-homedir").value.trim();
    const password = $("user-modal-password").value;
    if (!username) return toast("사용자 이름을 입력하세요.", true);
    if (!homeDir) return toast("홈 디렉터리를 입력하세요.", true);
    if (editingIndex < 0 && !password) return toast("새 계정은 비밀번호를 입력해야 합니다.", true);

    const permissions = readPermGrid();
    if (permissions.length === 0) return toast("최소 하나 이상의 권한을 선택하세요.", true);

    const entry = { username, home_dir: homeDir, permissions, new_password: password };
    if (editingIndex < 0) users.push(entry);
    else users[editingIndex] = entry;
    renderUsers();
    closeUserModal();
  });

  // ---------- Folder browse modal ----------

  function openBrowse(targetId) {
    browseTargetId = targetId;
    const start = $(targetId).value.trim();
    $("browse-modal-overlay").classList.add("open");
    loadBrowse(start);
  }
  function closeBrowse() { $("browse-modal-overlay").classList.remove("open"); }

  function loadBrowse(path) {
    const url = "/api/browse" + (path ? "?path=" + encodeURIComponent(path) : "");
    fetch(url)
      .then((r) => r.json())
      .then((data) => {
        if (data.error) return toast(data.error, true);
        browseState = data;
        $("browse-current-path").textContent = data.path || "-";
        $("browse-drives").innerHTML = (data.drives || [])
          .map((d) => '<span class="drive-chip" data-drive="' + escapeHTML(d) + '">' + escapeHTML(d) + "</span>")
          .join("");
        $("browse-drives").querySelectorAll("[data-drive]").forEach((el) =>
          el.addEventListener("click", () => loadBrowse(el.dataset.drive))
        );
        const list = $("browse-list");
        if (!data.dirs || data.dirs.length === 0) {
          list.innerHTML = '<div class="empty" style="padding:14px 0;">하위 폴더가 없습니다.</div>';
        } else {
          list.innerHTML = data.dirs
            .map((d) => '<div class="browse-row" data-dir="' + escapeHTML(d) + '">📁 ' + escapeHTML(d) + "</div>")
            .join("");
          list.querySelectorAll("[data-dir]").forEach((el) =>
            el.addEventListener("dblclick", () => loadBrowse(joinPath(data.path, el.dataset.dir)))
          );
        }
      })
      .catch(() => toast("폴더 목록을 불러오지 못했습니다.", true));
  }
  function joinPath(base, name) {
    if (!base) return name;
    const sep = base.includes("\\") ? "\\" : "/";
    return base.endsWith(sep) ? base + name : base + sep + name;
  }

  $("browse-up").addEventListener("click", () => {
    if (browseState.parent) loadBrowse(browseState.parent);
  });
  $("browse-cancel").addEventListener("click", closeBrowse);
  $("browse-select").addEventListener("click", () => {
    if (browseTargetId) $(browseTargetId).value = browseState.path || "";
    closeBrowse();
  });

  document.querySelectorAll("[data-browse]").forEach((btn) => {
    btn.addEventListener("click", () => openBrowse(btn.dataset.browse));
  });

  // ---------- Load / Save ----------

  function setField(id, value) {
    const el = $(id);
    if (el.type === "checkbox") el.checked = !!value;
    else el.value = value == null ? "" : value;
  }
  function getField(id) {
    const el = $(id);
    return el.type === "checkbox" ? el.checked : el.value;
  }
  function getInt(id) {
    const v = parseInt($(id).value, 10);
    return isNaN(v) ? 0 : v;
  }
  function setList(id, list) {
    $(id).value = (list || []).join("\n");
  }
  function getList(id) {
    return $(id).value.split("\n").map((s) => s.trim()).filter((s) => s !== "");
  }

  function setTftpPermMode(allowRead, allowWrite) {
    const mode = allowRead && allowWrite ? "rw" : allowWrite ? "wo" : "ro";
    document.querySelectorAll('input[name="tftp_perm_mode"]').forEach((r) => (r.checked = r.value === mode));
  }
  function getTftpPermMode() {
    const checked = document.querySelector('input[name="tftp_perm_mode"]:checked');
    const mode = checked ? checked.value : "ro";
    return { allowRead: mode === "rw" || mode === "ro", allowWrite: mode === "rw" || mode === "wo" };
  }

  function load() {
    fetch("/api/settings")
      .then((r) => r.json())
      .then((dto) => {
        setField("data_dir", dto.data_dir);
        setField("autostart_enabled", dto.autostart_enabled);
        if (!dto.autostart_available) {
          $("autostart_enabled").disabled = true;
          $("autostart-desc").textContent = "이 플랫폼에서는 지원되지 않습니다 (Windows 전용).";
        }

        setField("ftp_enabled", dto.ftp_enabled);
        setField("ftp_listen", dto.ftp_listen);
        setField("ftp_passive_lo", dto.ftp_passive_lo);
        setField("ftp_passive_hi", dto.ftp_passive_hi);
        setField("ftp_max_connections", dto.ftp_max_connections);
        setField("ftp_allow_anonymous", dto.ftp_allow_anonymous);
        setField("ftp_anonymous_home_dir", dto.ftp_anonymous_home_dir);
        setField("ftp_tls_enabled", dto.ftp_tls_enabled);
        setField("ftp_tls_cert", dto.ftp_tls_cert);
        setField("ftp_tls_key", dto.ftp_tls_key);
        setField("ftp_sftp_enabled", dto.ftp_sftp_enabled);
        setField("ftp_sftp_listen", dto.ftp_sftp_listen);
        setList("ftp_ip_allowlist", dto.ftp_ip_allowlist);
        users = (dto.ftp_users || []).map((u) => ({ username: u.username, home_dir: u.home_dir, permissions: u.permissions || [], new_password: "" }));
        renderUsers();

        setField("tftp_enabled", dto.tftp_enabled);
        setField("tftp_listen", dto.tftp_listen);
        setField("tftp_root_dir", dto.tftp_root_dir);
        setTftpPermMode(dto.tftp_allow_read, dto.tftp_allow_write);
        setField("tftp_max_blksize", dto.tftp_max_blksize);
        setField("tftp_timeout_sec", dto.tftp_timeout_sec);
        setField("tftp_max_retries", dto.tftp_max_retries);

        setField("syslog_enabled", dto.syslog_enabled);
        setField("syslog_udp_listen", dto.syslog_udp_listen);
        setField("syslog_tcp_listen", dto.syslog_tcp_listen);
        setField("syslog_log_dir", dto.syslog_log_dir);
        setField("syslog_max_size_mb", dto.syslog_max_size_mb);
        setField("syslog_max_age_day", dto.syslog_max_age_day);
        setField("syslog_compress", dto.syslog_compress);
        setField("syslog_tls_enabled", dto.syslog_tls_enabled);
        setField("syslog_tls_cert", dto.syslog_tls_cert);
        setField("syslog_tls_key", dto.syslog_tls_key);
        setList("syslog_ip_allowlist", dto.syslog_ip_allowlist);

        setField("web_enabled", dto.web_enabled);
        setField("web_listen", dto.web_listen);
        setField("web_username", dto.web_username);
        setList("web_ip_allowlist", dto.web_ip_allowlist);
      })
      .catch(() => toast("설정을 불러오지 못했습니다.", true));
  }

  $("save-btn").addEventListener("click", () => {
    const dto = {
      data_dir: getField("data_dir"),
      autostart_enabled: getField("autostart_enabled"),

      ftp_enabled: getField("ftp_enabled"),
      ftp_listen: getField("ftp_listen"),
      ftp_passive_lo: getInt("ftp_passive_lo"),
      ftp_passive_hi: getInt("ftp_passive_hi"),
      ftp_max_connections: getInt("ftp_max_connections"),
      ftp_allow_anonymous: getField("ftp_allow_anonymous"),
      ftp_anonymous_home_dir: getField("ftp_anonymous_home_dir"),
      ftp_tls_enabled: getField("ftp_tls_enabled"),
      ftp_tls_cert: getField("ftp_tls_cert"),
      ftp_tls_key: getField("ftp_tls_key"),
      ftp_sftp_enabled: getField("ftp_sftp_enabled"),
      ftp_sftp_listen: getField("ftp_sftp_listen"),
      ftp_ip_allowlist: getList("ftp_ip_allowlist"),
      ftp_users: users,

      tftp_enabled: getField("tftp_enabled"),
      tftp_listen: getField("tftp_listen"),
      tftp_root_dir: getField("tftp_root_dir"),
      tftp_allow_read: getTftpPermMode().allowRead,
      tftp_allow_write: getTftpPermMode().allowWrite,
      tftp_max_blksize: getInt("tftp_max_blksize"),
      tftp_timeout_sec: getInt("tftp_timeout_sec"),
      tftp_max_retries: getInt("tftp_max_retries"),

      syslog_enabled: getField("syslog_enabled"),
      syslog_udp_listen: getField("syslog_udp_listen"),
      syslog_tcp_listen: getField("syslog_tcp_listen"),
      syslog_log_dir: getField("syslog_log_dir"),
      syslog_max_size_mb: getInt("syslog_max_size_mb"),
      syslog_max_age_day: getInt("syslog_max_age_day"),
      syslog_compress: getField("syslog_compress"),
      syslog_tls_enabled: getField("syslog_tls_enabled"),
      syslog_tls_cert: getField("syslog_tls_cert"),
      syslog_tls_key: getField("syslog_tls_key"),
      syslog_ip_allowlist: getList("syslog_ip_allowlist"),

      web_enabled: getField("web_enabled"),
      web_listen: getField("web_listen"),
      web_username: getField("web_username"),
      web_new_password: getField("web_new_password"),
      web_ip_allowlist: getList("web_ip_allowlist"),
    };

    fetch("/api/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(dto) })
      .then(async (r) => {
        const body = await r.json();
        if (!r.ok || !body.ok) throw new Error(body.error || "저장에 실패했습니다.");
        toast("설정이 저장되었습니다.", false);
        $("web_new_password").value = "";
        load();
      })
      .catch((err) => toast(err.message, true));
  });

  load();
})();
