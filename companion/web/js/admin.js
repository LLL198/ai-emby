let consoleTimer, consoleController;
function stopConsolePolling() {
  clearTimeout(consoleTimer);
  consoleTimer = null;
  consoleController?.abort();
  consoleController = null;
}
function adminSection(n) {
  stopConsolePolling();
  mediaGeneration++;
  mediaQueueRequest++;
  mediaBatchRequest++;
  clearTimeout(mediaTimer);
  clearTimeout(mediaBatchTimer);
  if (n === 3) {
    const section = document.querySelectorAll(".admin-section")[3];
    const loading = section?.querySelector("#media-loading");
    const content = section?.querySelector("#media-content");
    if (loading && content) {
      content.hidden = true;
      $("#media-settings").replaceChildren();
      $("#media-automation").replaceChildren();
      $("#media-queue").textContent = "";
      $("#media-concurrency").reset();
      loading.replaceChildren(UI.LoadingSkeleton());
      loading.hidden = false;
    }
  }
  document
    .querySelectorAll(".admin-section")
    .forEach((x, i) => (x.hidden = i !== n));
  document.querySelectorAll("#drawer button").forEach((b) => {
    if (
      (n === 0 && b.getAttribute("onclick") === "mediaPage()") ||
      b.getAttribute("onclick") === `adminSection(${n})`
    )
      b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  });
  if (document.body.classList.contains("drawer-open")) {
    toggleDrawer(false);
  }
  if ([4, 6, 7, 9, 10, 11].includes(n)) $("#drawer .drawer-submenu").open = true;
  if (n === 3) run(loadMediaSettings)();
  if (n === 4) run(loadEnhancements)();
  if (n === 5) run(loadKeys)();
  if (n === 6) run(loadTMDBSettings)();
  if (n === 7) run(loadTelegramSettings)();
  if (n === 8) run(loadScraperSettings)();
  if (n === 9) run(loadSubtitleSettings)();
  if (n === 10) run(loadIntroSettings)();
  if (n === 11) run(loadProxySettings)();
  if (n === 12) run(loadConsole)();
}
async function folderPicker(id, path = "/media", after = "") {
  let dialog = $("#folder-picker");
  if (!dialog) {
    dialog = document.createElement("dialog");
    dialog.id = "folder-picker";
    document.body.appendChild(dialog);
  }
  const b = await api(
    "/admin/directories?" + new URLSearchParams({ Path: path, After: after }),
  );
  dialog.innerHTML = `<h2>添加媒体文件夹</h2><div class="bar">${["/media", "/media1", "/media2", "/media3"].map((root) => `<button class="secondary media-root" data-root="${root}">${root}</button>`).join("")}</div><p>${esc(b.Path)}</p><div class="bar"><button id="folder-select">添加此目录</button><button id="folder-up" class="secondary">上一级</button>${b.Next ? '<button id="folder-next">下一页</button>' : ""}<button id="folder-close" class="secondary">取消</button></div><div class="directory-list">${b.Directories.map((d) => `<button class="secondary folder-dir" data-path="${esc(d.Path)}">📁 ${esc(d.Name)}</button>`).join("")}</div>`;
  dialog
    .querySelectorAll(".media-root")
    .forEach(
      (el) => (el.onclick = run(() => folderPicker(id, el.dataset.root))),
    );
  if (!dialog.open) dialog.showModal();
  $("#folder-close").onclick = () => dialog.close();
  $("#folder-up").onclick = run(() =>
    folderPicker(
      id,
      /^\/media\d*$/.test(b.Path)
        ? b.Path
        : b.Path.slice(0, b.Path.lastIndexOf("/")),
    ),
  );
  $("#folder-next")?.addEventListener(
    "click",
    run(() => folderPicker(id, b.Path, b.Next)),
  );
  dialog
    .querySelectorAll(".folder-dir")
    .forEach(
      (el) => (el.onclick = run(() => folderPicker(id, el.dataset.path))),
    );
  $("#folder-select").onclick = run(async () => {
    await api("/admin/library-folders", "POST", { ID: id, Path: b.Path });
    dialog.close();
    await admin();
    toast("媒体文件夹已添加，后台更新索引中");
  });
}
let logCategory = "scan",
  logTimer,
  logGeneration = 0;
const logNames = {
  subtitle: "字幕",
  intro_credits: "片头片尾",
  tmdb: "TMDB",
  scraper: "刮削",
  telegram: "Telegram Bot",
  scan: "扫描媒体",
  update: "更新媒体",
  probe: "媒体信息提取",
  playback: "用户播放",
  warning: "验证警告",
  error: "错误日志",
  proxy: "反代日志",
  redirect: "302日志",
};
const logStates = {
  paused: "已暂停",
  waiting: "等待扫描中",
  indexing: "索引元数据",
  cleaning: "清理索引",
  counting: "统计媒体数量",
  running: "处理中",
  complete: "完成",
  error: "失败",
  requested: "请求播放",
  login: "登录成功",
  cancelled: "已取消",
};
const scraperContentLabels = {
  NFO: 'NFO',
  Poster: '海报图',
  Backdrop: '背景图',
  Logo: 'Logo',
  Banner: '横幅图',
  Disc: '光盘图',
  Still: '缩略图',
};

function scraperLogText(value) {
  let text = String(value || '');
  for (const [name, label] of Object.entries(scraperContentLabels)) {
    text = text.replace(new RegExp(`\\b${name}\\b`, 'g'), label);
  }
  return text;
}

function renderScraperLog(x) {
  let current = x.Current || '';
  try {
    const fields = JSON.parse(current);
    current = ['phase', 'title', 'kind', 'directory', 'recognizer', 'scraper', 'reason']
      .map(key => fields[key])
      .filter(Boolean)
      .map(scraperLogText)
      .join(' · ');
  } catch {
    current = scraperLogText(current);
  }

  const error = scraperLogText(x.Error || '');

  return `<article class="log-entry"><strong>${esc(x.Name)}</strong> · ${esc(logStates[x.State] || x.State)}<p>${esc(new Date(x.Updated || x.Started).toLocaleString())}${x.Total ? ` · ${x.Done}/${x.Total}` : ''}</p><p>${esc(current)}</p>${error ? `<p role="status">失败原因：${esc(error)}</p>` : ''}</article>`;
}
function scanPercent(x) {
  return x.State === "complete"
    ? 100
    : x.State === "cleaning"
      ? 97
      : x.State === "indexing"
        ? 90
        : x.Total
          ? Math.min(85, Math.floor((x.Done / x.Total) * 85))
          : 0;
}
async function clearLogs() {
  const ok = await confirmDialog("清空日志", "确定要清空实时日志吗？此操作无法撤销。");
  if (!ok) return;
  await api(
    "/admin/logs" + (logCategory === "proxy" ? "?category=proxy" : ""),
    "DELETE",
  );
  logTab(logCategory);
}
function closeLogs() {
  clearTimeout(logTimer);
  logGeneration++;
  $("#logs").close();
}
function showLogs() {
  if (!user?.Policy?.IsAdministrator) return;
  $("#logs").showModal();
  logTab(logCategory);
}
function logTab(category) {
  logCategory = category;
  const pause = $("#scraper-pause");
  pause.hidden = category !== "scraper";
  pause.disabled = true;
  clearTimeout(logTimer);
  const generation = ++logGeneration;
  $(".log-tabs").innerHTML = Object.entries(logNames)
    .map(
      ([k, n]) =>
        `<button type="button" class="secondary log-tab" aria-selected="${k === category}" onclick="logTab('${k}')">${n}</button>`,
    )
    .join("");
  refreshLogs(generation);
}
async function refreshLogs(generation) {
  try {
    const data = await api(
      "/admin/logs" + (logCategory === "proxy" ? "?category=proxy" : ""),
    );
    if (generation !== logGeneration || !$("#logs").open) return;
    if (logCategory === 'scraper') {
      const state = await api('/admin/scraper');
      if (generation !== logGeneration || !$('#logs').open) return;
      const button = $('#scraper-pause');
      button.disabled = !state.Running;
      button.dataset.paused = String(!!state.Paused);
      button.setAttribute('aria-pressed', String(!!state.Paused));
      button.title = button.ariaLabel = state.Paused ? '继续刮削' : '暂停刮削';
      button.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${state.Paused ? '<path d="m8 4 12 8-12 8Z"/>' : '<path d="M8 4v16M16 4v16"/>'}</svg>`;
      button.onclick = run(async () => {
        button.disabled = true;
        try {
          await api('/admin/scraper/control', 'PUT', {Paused: !state.Paused});
          toast(state.Paused ? '刮削已恢复' : '已请求暂停，当前操作结束后等待');
        } finally { if (generation === logGeneration) logTab('scraper'); }
      });
    }
    $("#log-status").textContent =
      logCategory === "proxy"
        ? `反代调试${data.Enabled ? "已开启" : "已关闭"} · 最近100条，单条响应最多32 KiB；重启清空。仅记录到达本服务的请求，CDN直连及连接前的DNS/TLS故障不在捕获范围。`
        : logCategory === "redirect"
          ? "记录本服务重定向状态、目标和错误；成功表示已返回重定向，客户端后续 CDN 播放结果需由客户端确认。"
          : logCategory === "probe"
            ? `并发上限 ${data.ProbeConcurrency} · 正在提取 ${data.ProbeRunning} · 等待数量 ${data.ProbeWaiting}`
            : logCategory === "playback"
              ? `在线播放 ${data.OnlinePlayback} 台设备`
              : "最近刷新 " + new Date().toLocaleTimeString();
    const entries = (data.Entries || []).filter((x) =>
      logCategory === "error"
        ? x.Category === "error" || (x.State === "error" && x.Category !== "probe")
        : x.Category === logCategory,
    );
    const logHTML =
      entries
        .map((x) => {
          if (x.Category === 'scraper') return renderScraperLog(x);
          const pct = scanPercent(x);
          return `<article class="log-entry"><strong>${esc(x.Name)}</strong> · ${esc(logStates[x.State] || x.State)}<p>${esc(new Date(x.Started).toLocaleString())}</p>${["scan", "update"].includes(x.Category) ? `<progress max="100" value="${pct}"></progress><span>${pct}% · ${x.Done}/${x.Total} 部/集</span><p>${x.Current === "清理已删除条目" ? '<span class="inline-icon" title="清理已删除条目" aria-label="清理已删除条目"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M16 3l-6 10M8 11l7 4-4 7-9-5Z"/></svg></span>' : esc(x.Current)}</p>` : ""}${x.Category === "probe" && x.Name === "批量提取媒体信息" ? `<p>当前：${esc(x.Current || "等待开始")}</p><p>完成：${x.Done}${x.Total ? ` / ${x.Total}` : ""}</p>` : ""}${x.Category === "error" && x.Current ? `<p>当前：${esc(x.Current)}</p>` : ""}${x.Category === "subtitle" && x.Current ? `<p>${esc(x.Current)}</p>` : ""}${x.Category === "playback" && x.State !== "login" ? `<p>${x.Online ? "🟢 在线播放" : "离线 / 已停止"} · ${x.RunTimeTicks ? x.Progress.toFixed(1) + "%" : "已播放 " + Math.floor(x.PositionTicks / 1e7) + " 秒（总时长未知）"}<br>用户ID：${esc(x.UserID)}<br>视频ID：${esc(x.ItemID)}<br>用户名：${esc(x.Username)}<br>IP：${esc(x.IP)}<br>设备：${esc(x.Device)}<br>客户端：${esc(x.Client)}<br>请求视频：${esc(x.Name)}</p>` : ""}${x.State === "login" ? `<p>用户名：${esc(x.Username)} · 客户端：${esc(x.Client)} · 设备：${esc(x.Device)}</p>` : ""}${x.Category === "warning" ? `<p>用户名：${esc(x.Username || "未知")} · IP：${esc(x.IP)} · 设备：${esc(x.Device)}</p>` : ""}${["proxy", "redirect"].includes(x.Category) ? `<pre style="white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px">${esc(x.Current)}</pre>` : ""}${x.Error ? `<p role="alert">${esc(x.Error)}</p>` : ""}</article>`;
        })
        .join("") || "<p>暂无记录</p>";
    const content = $("#log-content");
    const selection = window.getSelection();
    if (!content.contains(selection?.anchorNode) || selection.isCollapsed) {
      if (content.dataset.rendered !== logHTML) {
        const top = content.scrollTop;
        content.innerHTML = logHTML;
        content.dataset.rendered = logHTML;
        content.scrollTop = top;
      }
    }
  } catch (e) {
    if (generation === logGeneration)
      $("#log-status").textContent = "日志读取失败：" + e.message;
  } finally {
    if (generation === logGeneration && $("#logs").open)
      logTimer = setTimeout(() => refreshLogs(generation), 1000);
  }
}
$("#logs").addEventListener("cancel", () => {
  clearTimeout(logTimer);
  logGeneration++;
});

let adminLibs = [];
async function uploadCover(id) {
  const input = document.createElement("input");
  input.type = "file";
  input.accept = "image/jpeg,image/png,image/webp";
  input.onchange = run(async () => {
    const f = input.files[0];
    if (!f) return;
    await api.uploadCover(id, f);
    toast("封面已保存");
  });
  input.click();
}
async function chooseDirectory(path = "/media", after = "") {
  const b = await api(
    "/admin/directories?" + new URLSearchParams({ Path: path, After: after }),
  );
  $("#directory").innerHTML =
    `<p>${esc(b.Path)}</p><div class="bar"><button type="button" id="selectDir">选择此目录</button><button type="button" id="upDir" class="secondary">上一级</button>${b.Next ? '<button type="button" id="nextDir">下一页目录</button>' : ""}</div><div class="directory-list">${b.Directories.map((x) => `<button type="button" class="secondary dir" data-path="${esc(x.Path)}">📁 ${esc(x.Name)}</button>`).join("")}</div>`;
  $("#selectDir").onclick = () => {
    $("#addlib [name=path]").value = b.Path;
    $("#directory").innerHTML = "";
  };
  $("#upDir").onclick = run(() =>
    chooseDirectory(
      b.Path === "/media"
        ? "/media"
        : b.Path.substring(0, b.Path.lastIndexOf("/")),
    ),
  );
  $("#nextDir")?.addEventListener(
    "click",
    run(() => chooseDirectory(b.Path, b.Next)),
  );
  document
    .querySelectorAll(".dir")
    .forEach((x) => (x.onclick = run(() => chooseDirectory(x.dataset.path))));
}
let adminGeneration = 0;
async function initializeAdmin(generation) {
  if (!user?.Policy?.IsAdministrator) return;
  view = "admin";
  nav();
  const [libs, users] = await Promise.all(
    ["/admin/libraries", "/admin/users"].map((p) => api(p)),
  );
  if (view !== "admin" || generation !== adminGeneration) return;
  adminLibs = libs;
  fmUsers = users;
  $("#app").innerHTML =
    `<section class="panel admin-section"></section><section class="panel admin-section" hidden><h2>用户管理</h2><details><summary>用户列表</summary>${users.map((x) => `<details class="user-entry"><summary>${esc(x.Name)}${x.FirstAdmin ? " · 首位管理员" : ""}</summary><div class="row" data-user="${x.Id}"><label>同时播放设备上限 <input class="max" type="number" min="1" max="100" value="${x.MaxDevices}"></label><label><input class="allow switch" role="switch" type="checkbox" ${x.Policy.EnableMediaPlayback ? "checked" : ""}>允许播放</label><button class="saveuser" data-id="${x.Id}" data-admin="${x.Policy.IsAdministrator}">保存</button><button class="resetpw" data-id="${x.Id}">修改用户密码</button>${!x.Policy.IsAdministrator && x.Id !== user.Id ? `<button class="danger deluser" data-id="${x.Id}">删除用户</button>` : ""}</div></details>`).join("")}</details><details><summary>添加用户</summary><form id="adduser" class="form"><input name="name" placeholder="用户名" required><input name="pw" type="password" placeholder="密码（可留空）"><input name="max" type="number" min="1" max="100" value="2"><button>添加用户</button></form></details><p>设备名额按播放心跳维持，停止播放后释放。</p></section><section class="panel admin-section" hidden><h2>媒体库排序</h2><p>按住 ☰ 拖动调整，支持鼠标和触屏；调整后保存，首页及客户端媒体库使用相同顺序。</p><div id="order"></div><button id="saveOrder">保存排序</button></section><section class="panel admin-section" hidden><h2>提取媒体信息设置</h2><div id="media-loading" role="status" aria-label="正在加载媒体信息设置" hidden></div><div id="media-content" hidden><form id="media-settings"></form><div id="media-automation"></div><form id="media-concurrency" class="media-concurrency-section"><div class="media-concurrency-row"><label class="media-concurrency-label">媒体信息提取并发 <input class="media-concurrency-input" name="Concurrency" type="number" inputmode="numeric" min="1" max="16" required></label><button type="submit">保存</button></div></form><p id="media-queue" aria-live="polite"></p></div></section><section class="panel admin-section" hidden><h2>增强功能</h2><form id="enhancements"></form></section><section class="panel admin-section" hidden><div class="bar"><h2>API管理</h2><button class="secondary icon-button round-add" title="新建API密钥" aria-label="新建API密钥" onclick="createKey()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg></button></div><div id="api-keys"></div></section><section class="panel admin-section" hidden><div class="fm-toolbar"><h2>TMDB 管理</h2></div><form id="tmdb-settings" class="form"></form></section><section class="panel admin-section" hidden><h2>Telegram Bot</h2><form id="telegram-settings" class="fm-form telegram-form"></form></section><section class="panel admin-section" hidden><h2>刮削管理</h2><div id="scraper-settings" class="tmdb-settings-form"></div></section><section class="panel admin-section" hidden><h2>字幕增强</h2><form id="subtitle-settings" class="form"></form></section><section class="panel admin-section" hidden><h2>片头片尾</h2><form id="intro-settings" class="tmdb-settings-form"></form></section><section class="panel admin-section" hidden><h2>代理设置</h2><form id="proxy-settings" class="tmdb-settings-form"></form></section><section class="panel admin-section" hidden><h2>控制台</h2><div id="admin-console" aria-live="polite"></div></section>`;
  function bind(sel, fn) {
    document
      .querySelectorAll(sel)
      .forEach((b) => (b.onclick = run(() => fn(b))));
  }
  $("#adduser").onsubmit = run(async (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    await api("/admin/users", "POST", {
      Name: f.get("name"),
      Password: f.get("pw"),
      MaxDevices: Number(f.get("max")),
    });
    await admin();
    adminSection(1);
  });
  bind(".saveuser", async (b) => {
    const row = b.closest(".row");
    await api("/admin/users", "PUT", {
      ID: b.dataset.id,
      MaxDevices: Number(row.querySelector(".max").value),
      Admin: b.dataset.admin === "true",
      AllowPlayback: row.querySelector(".allow").checked,
    });
    toast("用户设置已保存", {type:"success"});
  });
  bind(".resetpw", async (b) => {
    if (b.dataset.id === user.Id) {
      password();
      return;
    }
    const pw = prompt("新密码（普通用户可留空）");
    if (pw !== null) {
      await api("/Users/" + b.dataset.id + "/Password", "POST", { NewPw: pw });
      toast("密码已修改");
    }
  });
  bind(".deluser", async (b) => {
    if (await confirmDialog("删除用户", "确认删除该用户？此操作不可撤销。", "删除")) {
      await api("/admin/users", "DELETE", { ID: b.dataset.id });
      await admin();
      adminSection(1);
    }
  });
  renderOrder();
  $("#saveOrder").onclick = run(async () => {
    await api("/admin/order", "PUT", { IDs: adminLibs.map((x) => x.Id) });
    toast("排序已保存");
  });
}
async function favoriteCoverDialog() {
  const state = await api("/Users/me/FavoriteCover");
  const input = document.createElement("input");
  input.type="file";input.accept="image/jpeg,image/png,image/webp";input.hidden=true;
  document.body.append(input);
  input.onchange=()=>{
    input.remove();
    const file=input.files?.[0];if(!file)return;
    UI.ImageCropper(file, {
      variant:"favorite-cover", hasCover:state.HasCustomCover,
      outputWidth:1280, outputHeight:720,
      onUpload:async cropped=>{
        await api("/Users/me/FavoriteCover", "POST", cropped, {raw:true});
        document.querySelector(".favorite-cover-remove")?.removeAttribute("disabled");
        toast("封面已更新", {type:"info", message:"已保存到 images-sc"});
      },
      onRemove:async()=>{
        await api("/Users/me/FavoriteCover", "DELETE");
        document.querySelector(".favorite-cover-remove")?.setAttribute("disabled", "");
        toast("封面已移除", {type:"info"});
      }
    });
  };
  input.click();
}
async function removeFavoriteCover() {
  await api("/Users/me/FavoriteCover", "DELETE");
  document.querySelector(".favorite-cover-remove")?.setAttribute("disabled", "");
  toast("封面已移除", {type:"info"});
}

async function loadEnhancements() {
  const c = await api("/admin/enhancements");
  favoritesEnabled = c.EnableFavorites === true;
  favoritesPromise = Promise.resolve(favoritesEnabled);
  const f = $("#enhancements");
  if (!f) return;
  f.innerHTML = `<label class="row">服务器显示名称<input name="serverName" required maxlength="128" value="${esc(c.ServerName)}"></label><div class="favorite-cover-control"><label class="toggle-label"><input class="switch" role="switch" name="favorites" type="checkbox" ${c.EnableFavorites ? "checked" : ""}>开启收藏功能</label><button type="button" class="secondary favorite-cover-add">上传封面</button><button type="button" class="secondary favorite-cover-remove" disabled>移除封面</button></div><p>在我的媒体库显示“播放收藏”。关闭后保留每位用户的收藏数据。</p><label class="toggle-label"><input class="switch" role="switch" name="episodeCount" type="checkbox" ${c.ShowEpisodeCount !== false ? "checked" : ""}>海报显示剧集集数角标</label><p>默认开启，Web 显示总集数；客户端通过标准 Emby 集数与未看集数字段展示角标。</p><label class="toggle-label"><input class="switch" role="switch" name="hide" type="checkbox" ${c.HideMissingActorImages ? "checked" : ""}>隐藏没有图片的演员信息</label><p>默认开启：隐藏没有照片的演员；关闭后显示演员姓名和照片占位。保存后重新打开影视简介生效。</p><label class="toggle-label"><input class="switch" role="switch" name="mergeFolder" type="checkbox" ${c.MergeVersionsInFolder ? "checked" : ""}>媒体文件夹内合并多版本</label><label class="toggle-label"><input class="switch" role="switch" name="mergeLibraries" type="checkbox" ${c.MergeVersionsAcrossLibraries ? "checked" : ""}>跨媒体库合并多版本</label><p>默认开启。按 TMDB 编号识别影片，电视剧区分季、集；无编号的电影按片名和年份匹配。关闭跨库合并时，仅合并同一媒体文件夹内的版本。播放旁的版本按钮可选择版本，播放中选择会从头切换。</p><label class="toggle-label"><input class="switch" role="switch" name="initials" type="checkbox" ${c.SearchByInitials ? "checked" : ""}>按照首字母搜索视频</label><p>默认开启，支持中文片名和部分拼音首字母匹配。例如输入“流浪”、ll 或 lldq 均可搜索“流浪地球”，ll 也会返回其他匹配影片。</p><label class="toggle-label"><input class="switch" role="switch" name="watchEnabled" type="checkbox" ${c.WatchEnabled ? "checked" : ""}>监听文件变动自动刷新路径</label><label class="row">媒体变动延时（秒）<input name="watchDelay" type="number" min="10" max="86400" required value="${c.WatchDelaySeconds ?? 30}"></label><p>文件变动合并处理并刷新本地元数据，默认30秒，最小10秒。与刮削管理共用队列；两处均开启时由刮削管理接管。</p><fieldset><legend>播放模式</legend><fieldset class="enhancement-suboption" data-nanshare-fast><label class="toggle-label"><input class="switch" role="switch" name="nanShareFastPath" type="checkbox" ${c.NanShareFastPath ? "checked" : ""}>快速路径</label><p>开启后限时获取 STRM 源地址的重定向；失败直接返回原始 STRM。关闭时直接重定向，不额外请求。</p><label class="row">等待上限（秒）<input name="fastPathWaitSeconds" type="number" inputmode="numeric" min="1" max="20" step="1" required value="${c.FastPathWaitSeconds ?? 5}"></label><p>1-20 秒，默认 5 秒。成功立即返回，不会固定等满。</p></fieldset></fieldset><label class="toggle-label"><input class="switch" role="switch" name="proxyDebug" type="checkbox" ${c.ProxyDebug ? "checked" : ""}>反代调试</label><p><small>开发者专用</small></p><p>默认关闭。开启并保存后，在右上角实时日志的“反代日志”查看客户端请求和响应。</p><button>保存设置</button>`;
  f.querySelector(".favorite-cover-add").onclick = run(favoriteCoverDialog);
  f.querySelector(".favorite-cover-remove").onclick = run(removeFavoriteCover);
  api("/Users/me/FavoriteCover").then(state=>{
    if (f.isConnected) f.querySelector(".favorite-cover-remove").disabled=!state.HasCustomCover;
  }).catch(()=>{});
  const syncFastWaitDisabled = () => {
    f.elements.fastPathWaitSeconds.disabled = !f.elements.nanShareFastPath.checked;
  };
  f.elements.nanShareFastPath.onchange = syncFastWaitDisabled;
  syncFastWaitDisabled();
  f.onsubmit = run(async (e) => {
    e.preventDefault();
    await api("/admin/enhancements", "PUT", {
      EnableFavorites: f.elements.favorites.checked,
      ShowEpisodeCount: f.elements.episodeCount.checked,
      ProxyDebug: f.elements.proxyDebug.checked,
      ServerName: f.elements.serverName.value,
      WatchEnabled: f.elements.watchEnabled.checked,
      WatchDelaySeconds: Number(f.elements.watchDelay.value),
      SearchByInitials: f.elements.initials.checked,
      HideMissingActorImages: f.elements.hide.checked,
      MergeVersionsInFolder: f.elements.mergeFolder.checked,
      NanShareFastPath: f.elements.nanShareFastPath.checked,
      FastPathWaitSeconds: Number(f.elements.fastPathWaitSeconds.value),
      MergeVersionsAcrossLibraries: f.elements.mergeLibraries.checked,
    });
    favoritesEnabled = f.elements.favorites.checked;
    favoritesPromise = Promise.resolve(favoritesEnabled);
    toast("增强功能设置已保存", {type:"success"});
  });
}
let mediaTimer, mediaBatchTimer, mediaGeneration = 0, mediaQueueRequest = 0, mediaBatchRequest = 0;
function mediaActive(generation) {
  const section = document.querySelectorAll(".admin-section")[3];
  return generation === mediaGeneration && view === "admin" && section?.isConnected && !section.hidden;
}
const mediaBatchStates = { idle: '未运行', running: '运行中', stopping: '正在停止', stopped: '已停止', paused: '已暂停', complete: '已完成', completed: '已完成', error: '失败', failed: '失败' };
function mediaBatchIcon(name) {
  const paths = { start: '<path d="m8 5 11 7-11 7z"/>', stop: '<rect x="5" y="5" width="14" height="14" rx="2"/>' };
  return `<svg viewBox="0 0 24 24" aria-hidden="true">${paths[name]}</svg>`;
}
function mediaRootKey(root) { return JSON.stringify([root.Library, root.Root]); }
async function openMediaRootSettings(field) {
  const generation = mediaGeneration;
  // Fetch on every open; library roots may change while this page remains open.
  const data = await api('/admin/media-info/automation');
  if (!mediaActive(generation)) return;
  const roots = data.Roots || [];
  const selected = new Set((data.Settings?.[field] || []).map(mediaRootKey));
  const summary = field === 'MonitorRoots' ? '<p class="media-root-summary" data-root-summary></p>' : '';
  fmDialog(field === 'BatchRoots' ? '批量提取目录' : '实时监控目录',
    `${summary}<p>只选择当前媒体库的父目录，不展开子目录。媒体库目录变更后重新打开即可同步。</p><div class="media-root-list">${roots.length ? roots.map((root, i) => `<label class="media-root-option"><input type="checkbox" data-root-index="${i}" ${selected.has(mediaRootKey(root)) ? 'checked' : ''}><span>${esc(root.Name)} · ${esc(root.Root)}</span></label>`).join('') : '<p>当前没有可选的媒体库目录。</p>'}</div>`,
    async () => {
      const dialog = $('#modal');
      const values = [...dialog.querySelectorAll('[data-root-index]:checked')]
        .map(input => { const root = roots[Number(input.dataset.rootIndex)]; return { Library: root.Library, Root: root.Root }; });
      await api('/admin/media-info/automation', 'PUT', { [field]: values });
      const message = field === 'BatchRoots' ? '批量提取目录保存成功' : '实时监控目录保存成功';
      if (!mediaActive(generation)) return;
      const status = $('#media-automation')?.querySelector('[data-batch-action]');
      if (status) status.textContent = message;
      toast(message);
    });
  const dialog = $('#modal');
  dialog.classList.add('media-root-sheet');
  if (field === 'MonitorRoots') {
    const updateSummary = () => {
      const names = [...dialog.querySelectorAll('[data-root-index]:checked')].map(input => roots[Number(input.dataset.rootIndex)].Name);
      dialog.querySelector('[data-root-summary]').textContent = `已监控 ${names.length} 个目录${names.length ? '：' + names.join('、') : ''}`;
    };
    dialog.querySelectorAll('[data-root-index]').forEach(input => input.addEventListener('change', updateSummary));
    updateSummary();
  }
}
function renderMediaAutomation(data, batchResult, generation) {
  const host = $('#media-automation');
  host.innerHTML = `<div class="media-automation-line row"><span class="media-setting-label">批量提取媒体信息</span><div class="media-automation-actions"><button type="button" class="secondary icon-button media-automation-icon" data-batch-start title="开始批量提取" aria-label="开始批量提取">${mediaBatchIcon('start')}</button><button type="button" class="secondary icon-button media-automation-icon" data-batch-stop title="停止批量提取" aria-label="停止批量提取">${mediaBatchIcon('stop')}</button><button type="button" class="secondary icon-button media-automation-icon" data-batch-settings title="批量提取目录设置" aria-label="批量提取目录设置">${fmIcon('gear')}</button></div></div><p class="media-batch-action" data-batch-action role="status" aria-live="polite"></p><p class="media-batch-status" data-batch-status role="status" aria-live="polite"></p><div class="media-automation-line row"><label class="tmdb-switch-row"><input class="switch" role="switch" type="checkbox" data-monitor ${data.Settings?.MonitorEnabled ? 'checked' : ''}><span class="media-setting-label">实时监控入库</span></label><button type="button" class="secondary icon-button media-automation-icon" data-monitor-settings title="实时监控目录设置" aria-label="实时监控目录设置">${fmIcon('gear')}</button></div><p class="media-automation-help">实时监控仅处理选中的媒体库父目录；已有完整信息会跳过。</p>`;
  const monitor = host.querySelector('[data-monitor]');
  monitor.onchange = run(async () => {
    monitor.disabled = true;
    try {
      await api('/admin/media-info/automation', 'PUT', { MonitorEnabled: monitor.checked });
      if (mediaActive(generation)) toast(monitor.checked ? '实时监控已开启' : '实时监控已关闭');
    } catch (e) { if (mediaActive(generation)) monitor.checked = !monitor.checked; throw e; }
    finally { monitor.disabled = false; }
  });
  for (const field of ['BatchRoots', 'MonitorRoots']) {
    host.querySelector(field === 'BatchRoots' ? '[data-batch-settings]' : '[data-monitor-settings]')
      .onclick = run(() => openMediaRootSettings(field));
  }
  for (const action of ['start', 'stop']) {
    const button = host.querySelector(`[data-batch-${action}]`);
    button.onclick = run(async () => {
      button.disabled = true;
      try {
        await api(`/admin/media-info/batch/${action}`, 'POST', {});
        if (!mediaActive(generation)) return;
        host.querySelector('[data-batch-action]').textContent = action === 'start' ? '批量提取已开始' : '批量提取已停止';
        await refreshMediaBatchStatus(generation);
      } catch (e) {
        if (mediaActive(generation)) {
          host.querySelector('[data-batch-action]').textContent = `批量提取${action === 'start' ? '启动' : '停止'}失败：${e.message}`;
          button.disabled = false;
        }
        throw e;
      }
    });
  }
  const el = host.querySelector('[data-batch-status]');
  if (batchResult.status === 'fulfilled') renderMediaBatchStatus(batchResult.value, host, el);
  else el.textContent = '批量提取状态读取失败：' + batchResult.reason.message;
  mediaBatchTimer = setTimeout(() => refreshMediaBatchStatus(generation), 1000);
}
function renderMediaBatchStatus(b, host, el) {
  const state = String(b.State || 'idle').toLowerCase();
  el.textContent = `批量提取：${mediaBatchStates[state] || b.State} · 完成 ${b.Done ?? 0} · 跳过 ${b.Skipped ?? 0} · 失败 ${b.Failed ?? 0} · 等待 ${b.Waiting ?? 0} · 正在提取 ${b.Active ?? 0} · 并发 ${b.Concurrency ?? 0}`;
  host.querySelector('[data-batch-start]').disabled = state === 'running' || state === 'stopping';
  host.querySelector('[data-batch-stop]').disabled = state !== 'running';
}
async function refreshMediaBatchStatus(generation = mediaGeneration) {
  clearTimeout(mediaBatchTimer);
  const request = ++mediaBatchRequest;
  if (!mediaActive(generation)) return;
  const host = $('#media-automation');
  const el = host?.querySelector('[data-batch-status]');
  if (!el) return;
  try {
    const b = await api('/admin/media-info/batch/status');
    if (mediaActive(generation) && request === mediaBatchRequest && el.isConnected) renderMediaBatchStatus(b, host, el);
  } catch (e) {
    if (mediaActive(generation) && request === mediaBatchRequest && el.isConnected) el.textContent = '批量提取状态读取失败：' + e.message;
  } finally {
    if (mediaActive(generation) && request === mediaBatchRequest && el.isConnected)
      mediaBatchTimer = setTimeout(() => refreshMediaBatchStatus(generation), 1000);
  }
}
async function loadMediaSettings() {
  const generation = mediaGeneration;
  let c, automation;
  try {
    [c, automation] = await Promise.all([api("/admin/media-info"), api("/admin/media-info/automation")]);
  } catch (e) {
    if (mediaActive(generation)) $("#media-loading").textContent = "媒体信息设置读取失败：" + e.message;
    return;
  }
  if (!mediaActive(generation)) return;
  const [queueResult, batchResult] = await Promise.allSettled([
    api("/admin/media-info/status"), api("/admin/media-info/batch/status")
  ]);
  if (!mediaActive(generation)) return;
  const form = $("#media-settings");
  form.innerHTML =
    [
      ["Browse", "浏览简介时后台完整提取媒体信息"],
      ["PreloadNext", "播放剧集时，预加载下一集媒体信息"],
      ["Persistent", "媒体信息持久化"],
    ]
      .map(
        ([k, label]) =>
          `<label class="toggle-label"><input class="switch" role="switch" type="checkbox" name="${k}" ${c[k] ? "checked" : ""}>${label}</label>`,
      )
      .join("") +
    `<p>浏览简介立即返回已有信息，后台一次完整提取视频、音频和字幕信息，所有任务遵守媒体信息提取并发设置。开启下一集预加载时，播放开始后延迟在后台提取下一集；关闭后不再自动预加载。已有完整信息会跳过，同一任务合并去重，播放不等待提取。视频由客户端直连 CDN；仅后台提取产生少量媒体读取流量。</p><p>持久化关闭：影视文件删除后，自动清理对应媒体信息。开启：保留媒体信息供重新入库复用。</p><label class="row">媒体信息保存目录 <input name="Directory" value="${esc(c.Directory)}" required style="flex:1"><button type="button" class="secondary icon-button" title="恢复媒体信息" aria-label="恢复媒体信息" onclick="restoreMediaDialog()"><svg viewBox="0 0 24 24"><path d="M3 10a9 9 0 1 1 2 8M3 3v7h7M12 7v5l3 2"/></svg></button></label><p><small>要将神医助手或者Mediainfokeeper媒体信息放入应用的媒体信息目录中。</small></p><p>统一保存到数据目录下；更换目录会迁移已有媒体信息。</p>`;
  const concurrencyForm = $("#media-concurrency");
  const concurrency = concurrencyForm.elements.Concurrency;
  concurrency.value = c.Concurrency;
  concurrencyForm.onsubmit = run(async (e) => {
    e.preventDefault();
    if (!form.reportValidity() || !concurrency.reportValidity()) return;
    const b = {};
    for (const k of ["Browse", "PreloadNext", "Persistent"])
      b[k] = form.elements[k].checked;
    b.Directory = form.elements.Directory.value;
    b.Concurrency = Number(concurrency.value);
    await api("/admin/media-info", "PUT", b);
    if (!mediaActive(generation)) return;
    toast("媒体信息设置已保存", {type:"success"});
    await refreshMediaQueue(generation);
  });
  const validate = () =>
    concurrency.setCustomValidity(
      Number.isInteger(Number(concurrency.value)) &&
        Number(concurrency.value) >= 1 &&
        Number(concurrency.value) <= 16
        ? ""
        : "媒体信息提取并发必须为 1–16 的整数，不能设置为 0",
    );
  concurrency.addEventListener("input", validate);
  validate();
  renderMediaAutomation(automation, batchResult, generation);
  const queue = $("#media-queue");
  queue.textContent = queueResult.status === "fulfilled"
    ? mediaQueueText(queueResult.value)
    : "队列状态读取失败：" + queueResult.reason.message;
  $("#media-content").hidden = false;
  $("#media-loading").hidden = true;
  mediaTimer = setTimeout(() => refreshMediaQueue(generation), 1000);
}
function mediaQueueText(b) {
  return `等待提取数量：${b.ProbeWaiting} · 正在提取：${b.ProbeRunning} · 并发上限：${b.ProbeConcurrency}`;
}
async function refreshMediaQueue(generation = mediaGeneration) {
  clearTimeout(mediaTimer);
  const request = ++mediaQueueRequest;
  if (!mediaActive(generation)) return;
  const el = $("#media-queue");
  try {
    const b = await api("/admin/media-info/status");
    if (mediaActive(generation) && request === mediaQueueRequest && el.isConnected)
      el.textContent = mediaQueueText(b);
  } catch (e) {
    if (mediaActive(generation) && request === mediaQueueRequest && el.isConnected)
      el.textContent = "队列状态读取失败：" + e.message;
  } finally {
    if (mediaActive(generation) && request === mediaQueueRequest && el.isConnected)
      mediaTimer = setTimeout(() => refreshMediaQueue(generation), 1000);
  }
}
function renderOrder() {
  const box = $("#order");
  box.innerHTML = adminLibs
    .map(
      (x) =>
        `<div class="row" data-id="${esc(x.Id)}"><button class="drag-handle secondary" aria-label="拖动 ${x.Hidden ? "隐藏媒体库" : esc(x.Name)} 排序，键盘方向键调整" style="touch-action:none;cursor:grab">☰</button><strong class="info${x.Hidden ? " library-hidden" : ""}"><span class="library-name">${esc(x.Name)}</span></strong></div>`,
    )
    .join("");
  box.querySelectorAll(".drag-handle").forEach((h) => {
    h.onpointerdown = (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      const row = h.closest(".row"),
        before = [...box.children];
      box.setPointerCapture(e.pointerId);
      row.style.opacity = ".5";
      box.onpointermove = (e) => {
        const y = e.clientY;
        for (const other of [...box.children]) {
          if (other === row) continue;
          const r = other.getBoundingClientRect();
          if (y >= r.top && y <= r.bottom) {
            box.insertBefore(
              row,
              y < r.top + r.height / 2 ? other : other.nextSibling,
            );
            break;
          }
        }
        if (y < 80) window.scrollBy(0, -20);
        if (y > innerHeight - 80) window.scrollBy(0, 20);
      };
      const finish = (cancel) => {
        box.onpointermove = null;
        box.onpointerup = null;
        box.onpointercancel = null;
        row.style.opacity = "";
        if (cancel) before.forEach((x) => box.appendChild(x));
        adminLibs = [...box.children].map((el) =>
          adminLibs.find((x) => x.Id === el.dataset.id),
        );
      };
      box.onpointerup = () => finish(false);
      box.onpointercancel = () => finish(true);
    };
    h.onkeydown = (e) => {
      if (!["ArrowUp", "ArrowDown"].includes(e.key)) return;
      e.preventDefault();
      const row = h.closest(".row"),
        i = adminLibs.findIndex((x) => x.Id === row.dataset.id),
        j = i + (e.key === "ArrowUp" ? -1 : 1);
      if (j < 0 || j >= adminLibs.length) return;
      [adminLibs[i], adminLibs[j]] = [adminLibs[j], adminLibs[i]];
      renderOrder();
      box.children[j].querySelector("button").focus();
    };
  });
}

let fmLibrary = "",
  fmUsers = [],
  fmSort = "recent",
  fmTimer,
  fmScanSignature = "";
const fmIcons = {
 copy: '<rect x="8" y="8" width="13" height="13" rx="2"/><path d="M16 8V3H3v13h5"/>',
 download: '<path d="M12 3v12m-5-5 5 5 5-5M4 17v4h16v-4"/>',
  library:
    '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="M8 4v16M3 9h5M3 15h5M16 4v16M16 9h5M16 15h5"/>',
  scan: '<path d="M4 8V4h4M16 4h4v4M20 16v4h-4M8 20H4v-4M7 12h10"/>',
  refresh: '<path d="M20 7v5h-5M4 17v-5h5M6 7a7 7 0 0 1 12-1l2 6M4 12l2 6a7 7 0 0 0 12-1"/>',
  image: '<rect x="3" y="3" width="18" height="18" rx="3"/><circle cx="8" cy="8" r="1"/><path d="m3 17 6-6 4 4 3-3 5 5"/>',
  folder:
    '<path d="M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/>',
  gear: '<path d="m9 3-.5 2-2 1-2-.5-2 3 1.5 1.5v3L2.5 15l2 3 2-.5 2 1 .5 2h4l.5-2 2-1 2 .5 2-3-1.5-2v-3l1.5-1.5-2-3-2 .5-2-1L13 3Z"/><circle cx="11" cy="12" r="3"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>',
  hidden: '<path d="m3 3 18 18M10 5a12 12 0 0 1 12 7 17 17 0 0 1-4 5M6 6a17 17 0 0 0-4 6s3.5 7 10 7a12 12 0 0 0 4-1M10 10a3 3 0 0 0 4 4"/>',
  rename: '<path d="m15 4 5 5M4 20l5-1L21 7a2 2 0 0 0-4-4L5 15Z"/>',
  trash: '<path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/>',
  more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21v-2a8 8 0 0 1 16 0v2"/>',
};
function fmIcon(n) {
  return `<span class="fm-icon"><svg viewBox="0 0 24 24" aria-hidden="true">${fmIcons[n]}</svg></span>`;
}
function fmMenu(buttons) {
  return `<details class="fm-menu"><summary class="icon-button" aria-label="更多操作">${fmIcon("more")}</summary><div class="fm-pop">${buttons}</div></details>`;
}
function fmButton(label, action, danger = false, icon = "") {
  return `<button class="${danger ? "danger" : ""}" onclick="run(async()=>{${action}})()">${icon ? fmIcon(icon) : ""}${label}</button>`;
}
function fmDialog(title, html, save, action = "保存", actionsVariant = "") {
  const d = $("#modal");
  d.className = "settings-sheet";
  d.innerHTML = `<h2>${esc(title)}</h2><form class="fm-form">${html}<div class="bar form-actions${actionsVariant === "paired" ? " form-actions--paired" : ""}"><button type="button" class="secondary" onclick="closeModal()">${!save || actionsVariant === "paired" ? "关闭" : "取消"}</button>${save ? `<button type="submit"${actionsVariant === "paired" ? ' class="primary-action"' : ""}>${esc(action)}</button><span class="action-spinner" role="status" aria-label="正在处理" hidden></span>` : ""}</div></form>`;
  d.onclick = (e) => { if (e.target === d && outsideDialog(e, d)) closeModal(); };
  if (!d.open) d.showModal();
  d.querySelector("form").onsubmit = save ? run(async (e) => {
    e.preventDefault();
    const form = e.target,
      button = form.querySelector('[type="submit"]');
    if (button.disabled) return;
    button.disabled = true;
    const spinner = form.querySelector(".action-spinner");
    spinner.hidden = false;
    form.setAttribute("aria-busy", "true");
    try {
      const close = await save(new FormData(form));
      if (close !== false) await closeModal();
    } finally {
      button.disabled = false;
      spinner.hidden = true;
      form.removeAttribute("aria-busy");
    }
  }) : e => e.preventDefault();
}
async function admin() {
  if (!user?.Policy?.IsAdministrator) return;
  if (location.hash !== '#admin') history.pushState({page:'admin'}, '', '#admin');
  view = "admin";
  nav();
  $("#app").replaceChildren(UI.LoadingSkeleton());
  const generation = ++adminGeneration;
  await initializeAdmin(generation);
  if (view !== "admin" || generation !== adminGeneration) return;
  mediaPage();
  renderUsers();
  clearTimeout(fmTimer);
  pollScan();
}
function libraryStatus(x) {
  const state =
    x.Error || x.Status === "error"
      ? "error"
      : x.Status === "idle"
        ? "ready"
        : x.Status === "interrupted"
          ? "interrupted"
          : "busy";
  const label =
    { ready: "就绪", error: "失败", busy: "处理中", interrupted: "已中断" }[
      state
    ] + (x.Error ? "：" + x.Error : "");
  return `<span class="library-status ${state}" role="img" tabindex="0" aria-label="${esc(label)}" title="${esc(label)}">${state === "error" ? "!" : state === "busy" ? "↻" : state === "interrupted" ? "!" : "●"}</span>`;
}
function mediaPage(add = false) {
  fmLibrary = "";
  adminSection(0);
  const section = document.querySelectorAll(".admin-section")[0];
  section.innerHTML = `<div class="section-heading"><h2>媒体库管理</h2><button class="secondary icon-button" aria-label="媒体库设置" title="媒体库设置" onclick="run(librarySettingsDialog)()">${fmIcon("gear")}</button></div><p id="library-settings-status" class="muted library-settings-status"></p><div class="fm-toolbar minimal-toolbar"><div class="toolbar-actions"><button class="secondary icon-button round-add" title="新建媒体库" aria-label="新建媒体库" onclick="newLibrary()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg></button><button class="secondary icon-button " title="刷新全部媒体库" aria-label="刷新全部媒体库" onclick="run(scanAllMedia)()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 7v5h-5M4 17v-5h5M6 7a7 7 0 0 1 12-1l2 6M4 12l2 6a7 7 0 0 0 12-1"/></svg></button><button class="secondary icon-button" title="暂停刷新" aria-label="暂停刷新" onclick="run(()=>controlScan(true))()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5v14M16 5v14"/></svg></button><button class="secondary icon-button" title="恢复刷新" aria-label="恢复刷新" onclick="run(()=>controlScan(false))()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5l11 7-11 7Z"/></svg></button></div><div class="scan-circle" title="扫描进度" role="progressbar" aria-valuemin="0" aria-valuemax="100"><span>0%</span></div></div><div class="fm-list">${adminLibs.map((x) => `<div class="fm-row">${fmIcon("library")}<div class="fm-name${x.Hidden ? " library-hidden" : ""}"><button onclick="folderPage('${x.Id}')" ${x.Hidden ? 'aria-label="打开隐藏媒体库"' : ""}><span class="library-name" ${x.Hidden ? 'aria-hidden="true"' : ""}>${esc(x.Name)}</span></button>${x.Hidden ? '<span class="hidden-indicator" aria-label="已隐藏">' + fmIcon("hidden") + "</span>" : ""}</div>${libraryStatus(x)}${fmMenu(fmButton("全量扫描", `await scanMedia('${x.Id}','scan')`, false, "scan") + fmButton("刷新扫描", `await scanMedia('${x.Id}','update')`, false, "refresh") + fmButton("添加媒体文件夹", `await pickFolder('${x.Id}')`, false, "folder") + fmButton("插入媒体封面", `await uploadCover('${x.Id}')`, false, "image") + fmButton(x.Hidden ? "取消隐藏媒体库" : "隐藏媒体库", `await toggleLibraryHidden('${x.Id}')`, false, x.Hidden ? "eye" : "hidden") + fmButton("重命名媒体库", `renameLibrary('${x.Id}')`, false, "rename") + fmButton("删除媒体库", `await deleteLibrary('${x.Id}')`, true, "trash"))}</div>`).join("") || '<p class="empty">暂无媒体库，点击新建媒体库开始。</p>'}</div>`;
  void refreshLibrarySettingsStatus();
  if (add) newLibrary();
}
async function refreshLibrarySettingsStatus() {
  const el = $("#library-settings-status");
  if (!el) return;
  try {
    const settings = await api("/admin/library-settings");
    if (el.isConnected) el.textContent = "定时任务：" + (settings.Schedule.Enabled ? "开启" : "关闭") + " · 同时扫描：" + settings.ScanConcurrency + " · 同时更新：" + settings.UpdateConcurrency;
  } catch (_) {
    if (el.isConnected) el.textContent = "媒体库设置状态读取失败";
  }
}
async function refreshLibraries() {
  adminLibs = await api("/admin/libraries");
  if (fmLibrary && adminLibs.some((x) => x.Id === fmLibrary))
    folderPage(fmLibrary);
  else mediaPage();
}
async function deleteLibrary(id) {
  if (!(await confirmDialog("删除媒体库", "删除媒体库及索引？源文件保留。", "删除媒体库"))) return;
  await api("/admin/libraries", "DELETE", { ID: id });
  toast("媒体库已删除");
  await refreshLibraries();
}
async function scanMedia(id, mode) {
  if (
    !(await confirmScan(
      mode === "scan"
        ? "全量扫描此媒体库？将重新解析所有媒体信息。"
        : "刷新此媒体库？将只处理变化的内容。",
    ))
  )
    return;
  await api("/admin/scan", "POST", { ID: id, Mode: mode });
  document.querySelectorAll(".fm-menu[open]").forEach((x) => (x.open = false));
  toast("已加入队列，请在实时日志中查看");
}
function libraryPathRow() {
  return `<div class="row"><input name="path" required readonly role="button" aria-haspopup="dialog" aria-label="媒体目录" placeholder="点击选择媒体目录" onclick="run(()=>pickDirectory(p=>{this.value=p}))()"><button type="button" class="secondary" aria-label="添加目录" onclick="this.closest('#library-paths').insertAdjacentHTML('beforeend',libraryPathRow())">＋ 添加</button><button type="button" class="secondary" aria-label="移除目录" onclick="if(this.closest('#library-paths').children.length>1)this.parentElement.remove()">−</button></div>`;
}
function newLibrary() {
  fmDialog(
    "新建媒体库",
    `<label>媒体库类型<select name="kind"><option value="movies">电影</option><option value="tvshows">电视剧</option></select></label><label>媒体库名称<input name="name" required></label><div id="library-paths">${libraryPathRow()}</div>`,
    async (f) => {
      const paths = f.getAll("path");
      if (paths.some((p) => !p)) throw Error("请选择目录");
      await api("/admin/libraries", "POST", {
        Name: f.get("name"),
        Kind: f.get("kind"),
        Paths: paths,
      });
      await refreshLibraries();
      toast("已加入队列，请在实时日志中查看");
    },
  );
}
async function scanAllMedia() {
  if (!(await confirmScan("刷新全部媒体库？将更新新增、修改及删除的内容。")))
    return;
  await api("/admin/scan-all", "POST", {});
  toast("全部媒体库已加入队列，请在实时日志中查看");
}
async function toggleLibraryHidden(id) {
  const library = adminLibs.find(x => x.Id === id);
  await api("/admin/library-visibility", "PUT", {ID: id, Hidden: !library.Hidden});
  await refreshLibraries();
  toast(library.Hidden ? "媒体库已取消隐藏" : "媒体库已隐藏");
}
function renameLibrary(id) {
  const library = adminLibs.find(x => x.Id === id);
  fmDialog("重命名媒体库", `<label>显示名称<input name="name" required maxlength="256" value="${esc(library.Name)}"></label><p>仅修改显示名称，目录路径保持不变。</p>`, async f => {
    await api("/admin/library-visibility", "PUT", {ID: id, Name: f.get("name")});
    await refreshLibraries();
    toast("媒体库名称已保存");
  });
}
async function librarySettingsDialog() {
  const settings = await api("/admin/library-settings");
  const c = settings.Schedule;
  fmDialog("媒体库设置", `
    <label class="toggle-label">定时全量扫描<input class="switch" role="switch" name="enabled" type="checkbox" ${c.Enabled ? "checked" : ""}></label>
    <div class="schedule-options"><div class="segmented" role="group" aria-label="扫描周期">${[["daily","每日"],["weekly","每周"],["minutes","间隔"]].map(([v,label])=>`<button type="button" data-frequency="${v}" aria-pressed="${c.Frequency===v}">${label}</button>`).join("")}</div><input type="hidden" name="frequency" value="${c.Frequency}"></div>
    <label id="schedule-week">星期<select name="weekday">${["周日","周一","周二","周三","周四","周五","周六"].map((v,i)=>`<option value="${i}" ${c.Weekday===i ? "selected" : ""}>${v}</option>`).join("")}</select></label>
    <label id="schedule-time">时间（UTC）<input type="time" name="time" value="${esc(c.Time)}" required></label>
    <label id="schedule-minutes">间隔分钟<input type="number" name="minutes" min="1" max="10080" value="${c.Minutes}" required></label>
    <section class="library-concurrency-section">
      <h3>媒体库并发设置</h3>
      <label>同时扫描上限<input name="scanConcurrency" type="number" min="1" max="64" required value="${settings.ScanConcurrency}"></label>
      <label>同时更新上限<input name="updateConcurrency" type="number" min="1" max="64" required value="${settings.UpdateConcurrency}"></label>
      <p>超额任务排队；降低上限不会中断正在运行的任务。</p>
    </section>`, async () => {
      const fields = $("#modal form").elements;
      const result = await api("/admin/library-settings", "PUT", {
        Schedule: {
          Enabled: fields.enabled.checked, Frequency: fields.frequency.value,
          Time: fields.time.value, Weekday: Number(fields.weekday.value),
          Minutes: Number(fields.minutes.value),
        },
        ScanConcurrency: Number(fields.scanConcurrency.value),
        UpdateConcurrency: Number(fields.updateConcurrency.value),
      });
      await refreshLibrarySettingsStatus();
      toast("媒体库设置已保存", {type:"success"});
      return result;
    }, "保存", "paired");
  const form = $("#modal form");
  form.querySelectorAll("[data-frequency]").forEach(button => button.onclick = () => {
    form.elements.frequency.value = button.dataset.frequency;
    form.querySelectorAll("[data-frequency]").forEach(b => b.setAttribute("aria-pressed", b === button));
    scheduleFields(button.dataset.frequency);
  });
  scheduleFields(c.Frequency);
}
function scheduleFields(v) {
  $("#schedule-week").hidden = v !== "weekly";
  $("#schedule-time").hidden = v === "minutes";
  $("#schedule-minutes").hidden = v !== "minutes";
  const form = $("#modal form");
  form.elements.weekday.disabled = v !== "weekly";
  form.elements.time.disabled = v === "minutes";
  form.elements.minutes.disabled = v !== "minutes";
}

function folderPage(id) {
  fmLibrary = id;
  const lib = adminLibs.find((x) => x.Id === id);
  if (!lib) return;
  adminSection(0);
  document.querySelectorAll(".admin-section")[0].innerHTML =
    `<button class="secondary icon-button " title="媒体列表" aria-label="媒体列表" onclick="mediaPage()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 5l-7 7 7 7"/></svg></button><div class="fm-toolbar"><h2><span class="${lib.Hidden ? "library-hidden" : ""}"><span class="library-name">${esc(lib.Name)}</span></span></h2><button class="secondary icon-button round-add" title="新建媒体文件夹" aria-label="新建媒体文件夹" onclick="run(()=>pickFolder('${id}'))()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg></button><div class="scan-circle" title="扫描进度" role="progressbar" aria-valuemin="0" aria-valuemax="100"><span>0%</span></div></div><div class="fm-list">${(lib.Locations || []).map((p, i) => `<div class="fm-row">${fmIcon("folder")}<div class="fm-name">${esc(p.split("/").pop() || "media")}<small>${esc(p)}</small></div>${fmMenu(fmButton("修改目录", `await pickFolder('${id}',${i})`) + fmButton("移除目录", `await removeFolder('${id}',${i})`, true))}</div>`).join("") || '<p class="empty">暂无媒体文件夹</p>'}</div>`;
}
async function pickFolder(id, index) {
  const old =
    index === undefined
      ? ""
      : adminLibs.find((x) => x.Id === id).Locations[index];
  fmDialog(
    old ? "修改目录" : "新建媒体文件夹",
    `<label>目录<input name="path" id="fm-path" readonly required role="button" aria-haspopup="dialog" placeholder="点击选择媒体目录" value="${esc(old)}"></label>`,
    async (f) => {
      if (!f.get("path")) throw Error("请选择目录");
      await api("/admin/library-folders", old ? "PUT" : "POST", {
        ID: id,
        Path: f.get("path"),
        OldPath: old,
      });
      fmLibrary = id;
      await refreshLibraries();
      toast("已加入队列，请在实时日志中查看");
    },
  );
  $("#fm-path").onclick = run(() =>
    pickDirectory((p) => {
      $("#fm-path").value = p;
    }, old || "/media"),
  );
}
async function removeFolder(id, index) {
  const path = adminLibs.find((x) => x.Id === id).Locations[index];
  if (!confirm("移除此目录的索引？源文件保留。")) return;
  await api("/admin/library-folders", "DELETE", { ID: id, Path: path });
  await refreshLibraries();
}
async function pickDirectory(select, path = "/media", after = "") {
  let d = $("#fm-picker");
  if (!d) {
    d = document.createElement("dialog");
    d.id = "fm-picker";
    document.body.appendChild(d);
    d.addEventListener("click", (e) => {
      const r = d.getBoundingClientRect();
      if (
        e.target === d &&
        (e.clientX < r.left ||
          e.clientX > r.right ||
          e.clientY < r.top ||
          e.clientY > r.bottom)
      )
        d.close();
    });
  }
  const b = await api(
    "/admin/directories?" + new URLSearchParams({ Path: path, After: after }),
  );
  d.innerHTML = `<h2>选择目录</h2><p class="picker-path">${esc(b.Path)}</p><div class="directory-list">${b.Directories.map((x, i) => `<button class="secondary" data-index="${i}">${fmIcon("folder")}<span class="directory-name">${esc(x.Name)}</span><span aria-hidden="true">›</span></button>`).join("") || '<p class="empty">此目录没有子文件夹</p>'}${b.Next ? '<button id="fm-next" class="secondary">下一页</button>' : ""}</div><footer class="picker-actions"><button id="fm-up" class="secondary" ${b.Path === "/media" ? "disabled" : ""}>上一级</button><button id="fm-select">选择此目录</button></footer>`;
  if (!d.open) d.showModal();
  $("#fm-select").onclick = () => {
    select(b.Path);
    d.close();
  };
  $("#fm-up").onclick = run(() =>
    pickDirectory(select, b.Path.slice(0, b.Path.lastIndexOf("/")) || "/media"),
  );
  $("#fm-next")?.addEventListener(
    "click",
    run(() => pickDirectory(select, b.Path, b.Next)),
  );
  d.querySelectorAll("[data-index]").forEach(
    (el) =>
      (el.onclick = run(async () => {
        el.classList.add("selected");
        await pickDirectory(
          select,
          b.Directories[Number(el.dataset.index)].Path,
        );
      })),
  );
}

function loginAge(n) {
  if (!n) return "从未登录";
  const s = Math.max(0, Date.now() / 1000 - n);
  if (s >= 604800) return "一周前";
  if (s >= 86400) return Math.floor(s / 86400) + "天前";
  if (s >= 3600) return Math.floor(s / 3600) + "小时前";
  if (s >= 60) return Math.floor(s / 60) + "分钟前";
  return "刚刚";
}
function renderUsers() {
  const list = [...fmUsers].sort((a, b) =>
    fmSort === "name"
      ? a.Name.localeCompare(b.Name, "zh-CN")
      : fmSort === "oldest"
        ? a.LastLoginDate - b.LastLoginDate
        : b.LastLoginDate - a.LastLoginDate,
  );
  document.querySelectorAll(".admin-section")[1].innerHTML =
    `<div class="fm-toolbar"><h2>用户管理</h2><button class="secondary icon-button round-add" title="新建用户" aria-label="新建用户" onclick="editUser()"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg></button><select aria-label="排序" onchange="fmSort=this.value;renderUsers()"><option value="recent" ${fmSort === "recent" ? "selected" : ""}>最近登录优先</option><option value="oldest" ${fmSort === "oldest" ? "selected" : ""}>最久未登录优先</option><option value="name" ${fmSort === "name" ? "selected" : ""}>按名称排序</option></select></div><div class="fm-list">${list.map((x) => `<div class="fm-row">${fmIcon("user")}<div class="fm-name">${esc(x.Name)}<small>${x.FirstAdmin ? "首位管理员 · " : ""}${x.Policy.EnableMediaPlayback ? "允许播放" : "禁止播放"} · ${x.MaxDevices} 台设备</small></div><time class="fm-login" title="${x.LastLoginDate ? esc(new Date(x.LastLoginDate * 1000).toLocaleString()) : "从未登录"}">${loginAge(x.LastLoginDate)}</time>${fmMenu(fmButton("修改用户名和密码", `editUser('${x.Id}','identity')`) + fmButton("修改用户权限", `editUser('${x.Id}','permissions')`) + (!x.Policy.IsAdministrator && x.Id !== user.Id ? fmButton("删除用户", `await deleteUser('${x.Id}')`, true) : ""))}</div>`).join("")}</div>`;
}
async function refreshUsers() {
  fmUsers = await api("/admin/users");
  renderUsers();
  adminSection(1);
}
function editUser(id, mode) {
  const x = fmUsers.find((x) => x.Id === id),
    identity = !id || mode === "identity",
    permissions = !id || mode === "permissions";
  fmDialog(
    !id ? "新建用户" : identity ? "修改用户名和密码" : "修改用户权限",
    `${identity ? `<label>名称<input name="name" required value="${esc(x?.Name || "")}"></label><label>密码<input name="pw" type="password" ${id ? 'placeholder="留空保留原密码"' : ""} autocomplete="new-password"></label>` : ""}${permissions ? `<label>播放设备限制<input name="max" type="number" min="1" max="100" required value="${x?.MaxDevices || 2}"></label><label class="toggle-label"><input class="switch" role="switch" name="allow" type="checkbox" ${!x || x.Policy.EnableMediaPlayback ? "checked" : ""}>允许播放</label><label class="toggle-label">允许观看隐藏媒体库<input class="switch" role="switch" name="hiddenLibraries" type="checkbox" ${x?.Policy.CanViewHiddenLibraries ? "checked" : ""}></label>` : ""}`,
    async (f) => {
      if (!id) {
        await api("/admin/users", "POST", {
          Name: f.get("name"),
          Password: f.get("pw"),
          MaxDevices: Number(f.get("max")),
          AllowPlayback: f.has("allow"),
          CanViewHiddenLibraries: f.has("hiddenLibraries"),
        });
      } else if (identity) {
        await api("/admin/user-identity", "PUT", {
          ID: id,
          Name: f.get("name"),
          Password: f.get("pw"),
        });
        if (id === user.Id) {
          user.Name = f.get("name");
          sessionStorage.user = JSON.stringify(user);
          if (f.get("pw")) {
            token = "";
            sessionStorage.clear();
            login();
            toast("密码已修改，请重新登录");
            return;
          }
          nav();
        }
      } else {
        await api("/admin/users", "PUT", {
          ID: id,
          MaxDevices: Number(f.get("max")),
          Admin: x.Policy.IsAdministrator,
          AllowPlayback: f.has("allow"),
          CanViewHiddenLibraries: f.has("hiddenLibraries"),
        });
      }
      await refreshUsers();
      toast("用户已保存");
    },
  );
}
async function deleteUser(id) {
  if (!await confirmDialog("删除用户", "确认删除该用户？此操作不可撤销。", "删除")) return;
  await api("/admin/users", "DELETE", { ID: id });
  await refreshUsers();
}
async function pollScan() {
  if (view !== "admin") return;
  try {
    const b = await api(
      "/admin/logs" + (logCategory === "proxy" ? "?category=proxy" : ""),
    );
    const jobs = b.Entries.filter(
      (x) =>
        ["scan", "update"].includes(x.Category) &&
        (!fmLibrary || x.ItemID === fmLibrary),
    );
    const signature = jobs.map((x) => x.ID + ":" + x.State).join("|");
    if (signature !== fmScanSignature && !$("#modal").open) {
      fmScanSignature = signature;
      adminLibs = await api("/admin/libraries");
      if (!document.querySelectorAll(".admin-section")[0].hidden) {
        if (fmLibrary) folderPage(fmLibrary);
        else mediaPage();
      }
    }
    const active = jobs.filter((x) => !["complete", "error"].includes(x.State));
    const j = active.find((x) => x.State === "running") || active[0] || jobs[0];
    const pct = j ? scanPercent(j) : 0;
    document.querySelectorAll(".scan-circle").forEach((el) => {
      el.style.setProperty("--pct", pct + "%");
      el.setAttribute("aria-valuenow", pct);
      el.title = j
        ? `${logStates[j.State] || j.State} ${j.Done}/${j.Total} ${j.Error || ""}`
        : "暂无扫描任务";
      el.firstElementChild.textContent = pct + "%";
    });
    if (j?.State === "error")
      document
        .querySelectorAll(".scan-circle span")
        .forEach((el) => (el.textContent = "!"));
  } catch (e) {
    document
      .querySelectorAll(".scan-circle")
      .forEach((el) => (el.title = "进度读取失败：" + e.message));
  } finally {
    if (view === "admin") fmTimer = setTimeout(pollScan, 1500);
  }
}
document.addEventListener("keydown", (e) => {
  if (
    e.target.matches("input[readonly][name=path]") &&
    ["Enter", " "].includes(e.key)
  ) {
    e.preventDefault();
    e.target.click();
  }
});
document.addEventListener("click", (e) =>
  document.querySelectorAll(".fm-menu[open]").forEach((d) => {
    if (!d.contains(e.target)) d.open = false;
  }),
);

let serverNameRequest=0;
async function loadServerName() {
  const request=++serverNameRequest;
  let name="AI Emby";
  try {
    const c = await api("/System/Info/Public");
    name = serverDisplayName(c.ServerName);
  } catch {}
  if(request!==serverNameRequest)return;
  serverName=name;
  const logo=document.querySelector(".logo");
  if(logo)logo.textContent=name;
  document.title=name+" · 影库";
  if(view==="login")renderLoginMark();
}

async function loadKeys() {
  const keys = await api("/admin/keys");
  const target = $("#api-keys");
  if (!target) return;
  target.innerHTML =
    keys
      .map(
        (k) =>
          `<div class="row"><div class="info"><strong>${esc(k.Name)}</strong><p><code>${esc(k.MaskedKey)}</code></p></div><span class="muted">${new Date(k.Created * 1000).toLocaleString()}</span><button class="danger" data-key="${esc(k.Id)}">删除</button></div>`,
      )
      .join("") || "<p>暂无API密钥</p>";
  target.querySelectorAll("[data-key]").forEach(
    (b) =>
      (b.onclick = run(async () => {
        if (confirm("删除此API密钥？使用它的客户端将无法继续访问。")) {
          await api("/admin/keys", "DELETE", { ID: b.dataset.key });
          await loadKeys();
        }
      })),
  );
}
function createKey() {
  fmDialog(
    "新建API密钥",
    `<label>密钥名称<input name="name" placeholder="输入应用或设备名称" required maxlength="100" autocomplete="off"></label>`,
    async (f) => {
      const result = await api("/admin/keys", "POST", { Name: f.get("name") });
      $("#modal").innerHTML =
        `<h2>API密钥已创建</h2><div class="fm-form"><p>完整密钥仅显示这一次，请立即复制保存。关闭后只能查看脱敏信息。</p><code id="new-key-secret" style="overflow-wrap:anywhere;user-select:all">${esc(result.Token)}</code><div class="bar form-actions"><button type="button" onclick="closeModal()">已保存，关闭</button></div></div>`;
      await loadKeys();
      return false;
    },
  );
}

function confirmScan(message) {
  return new Promise((resolve) => {
    let d = document.createElement("dialog");
    d.innerHTML = `<h2>确认操作</h2><p>${esc(message)}</p><div class="bar"><button data-confirm>确认</button><button class="secondary" data-cancel>取消</button></div>`;
    document.body.appendChild(d);
    let answer = false;
    d.querySelector("[data-confirm]").onclick = () => {
      answer = true;
      d.close();
    };
    d.querySelector("[data-cancel]").onclick = () => d.close();
    d.onclose = () => {
      d.remove();
      resolve(answer);
    };
    d.showModal();
  });
}
async function controlScan(paused) {
  if (
    paused &&
    !(await confirmScan("暂停所有刷新和扫描任务？恢复后将继续处理。"))
  )
    return;
  await api("/admin/scan-control", "PUT", { Paused: paused });
  toast(paused ? "扫描已请求暂停" : "扫描已恢复");
}

function restoreMediaDialog() {
  fmDialog(
    "恢复媒体信息",
    "<p>是否恢复神医助手或者 Mediainfokeeper 的媒体信息？请先保存设置，并将 JSON 放入媒体信息保存目录。已有完整信息会跳过，同名匹配不唯一时跳过。</p>",
    async () => {
      const r = await api("/admin/media-info/restore", "POST", {});
      toast(
        `恢复完成：成功 ${r.Imported}，跳过 ${r.Skipped}，失败 ${r.Failed}`,
      );
    },
  );
}
let favoritesEnabled = null;
let favoritesPromise = null;
function getFavoritesEnabled() {
  if (favoritesEnabled !== null) return Promise.resolve(favoritesEnabled);
  if (!favoritesPromise) favoritesPromise = api("/enhancements")
    .then(c => {
      if (favoritesEnabled === null) favoritesEnabled = c.EnableFavorites === true;
      return favoritesEnabled;
    })
    .catch(() => { favoritesPromise = null; return true; });
  return Promise.race([favoritesPromise, new Promise(resolve => setTimeout(() => resolve(true), 650))]);
}
function bindPosterMenus() {
  document.querySelectorAll("#app .card:not([data-resume-card])").forEach(el => {
    if (el.dataset.menuBound) return;
    el.dataset.menuBound = "true";
    bindResumeMenu(el, run(() => posterMenu(el)));
  });
}
async function posterMenu(el, extraActions = []) {
  const id = el.dataset.id, name = el.querySelector("strong")?.textContent || "";
  if (id === "favorites" || !el.isConnected || document.querySelector("dialog.bottom-sheet[open]")) return;
  let favorite = null;
  if (["Movie", "Series"].includes(el.dataset.type) && await getFavoritesEnabled()) {
    favorite = el.dataset.favorite === "true" ? true : el.dataset.favorite === "false" ? false : null;
    if (favorite === null) {
      const state = await Promise.race([
        api.getItem(id).then(item => item?.UserData?.IsFavorite).catch(() => null),
        new Promise(resolve => setTimeout(() => resolve(null), 650)),
      ]);
      favorite = typeof state === "boolean" ? state : false;
      if (typeof state === "boolean") el.dataset.favorite = String(state);
    }
  }
  if (!el.isConnected || document.querySelector("dialog.bottom-sheet[open]")) return;
  const actions = [];
  let sheet;
  const close = action => async () => { sheet.close(); await action(); };
  // Settings and favorite details may fail or be slow; they must not block the menu.
  if (user?.Policy?.IsAdministrator) {
    actions.push({label:"重命名", action:close(() => {
      fmDialog("重命名显示名称", `<label>显示名称<input name="name" required maxlength="256" value="${esc(name)}"></label>`, async f => {
        await api("/admin/media-item", "PUT", {ID:id, Name:f.get("name")});
        await refreshBrowse(); toast("显示名称已保存");
      });
    })});
    if (["Movie", "Series"].includes(el.dataset.type))
      actions.push({label:"刮削元数据", action:close(() => openPosterScraper(id))});
  }
  if (favorite !== null) actions.push({label:favorite ? "取消收藏" : "收藏", icon:"star", action:close(async () => {
    await api(`/emby/Users/${encodeURIComponent(user.Id)}/FavoriteItems/${encodeURIComponent(id)}`, favorite ? "DELETE" : "POST");
    el.dataset.favorite = String(!favorite);
    toast(favorite ? "已取消收藏" : "已收藏");
    await refreshBrowse();
  })});
  actions.push(...extraActions.map(a => ({...a, action:close(a.action)})));
  if (user?.Policy?.IsAdministrator) {
    actions.push({label:"删除", danger:true, action:close(() => {
      const directory = UI.el("input", {type:"checkbox"});
      const content = UI.el("div", {}, [UI.el("p", {}, `确认删除 ${name}？将删除媒体索引及对应 STRM 文件。`),
        UI.el("label", {class:"toggle-label"}, ["连同 STRM 所在的目录一起删除", directory])]);
      const confirm = UI.ActionSheet("删除媒体", [
        {label:"删除", danger:true, action:async () => {
          await api("/admin/media-item", "DELETE", {ID:id, DeleteDirectory:directory.checked});
          confirm.close(); toast("删除成功");
          try { await refreshBrowse(); } catch (e) { toast("删除成功，但刷新页面失败：" + e.message); }
        }},
        {label:"取消", action:() => confirm.close()},
      ], content);
    })});
  }
  actions.push({label:"取消", action:() => sheet.close()});
  sheet = UI.ActionSheet(name, actions);
}

function secretField(name, label, placeholder) {
  return `<div class="secret-field"><input name="${name}" id="secret-${name}" type="password" autocomplete="new-password" spellcheck="false" autocapitalize="off" aria-label="${esc(label)}" placeholder="${esc(placeholder)}"><button type="button" class="secondary icon-button" data-reveal aria-controls="secret-${name}" aria-label="显示 ${esc(label)}" aria-pressed="false">${fmIcon('eye')}</button></div>`;
}
function bindSecretField(input, endpoint, label, hasSaved) {
  const button = input.parentElement.querySelector('[data-reveal]');
  const mask = '••••••••';
  let stored = hasSaved, dirty = false, version = 0;
  const updateButton = show => {
    button.innerHTML = fmIcon(show ? 'hidden' : 'eye');
    button.setAttribute('aria-pressed', String(show));
    button.setAttribute('aria-label', `${show ? '隐藏' : '显示'} ${label}`);
  };
  const reset = hasValue => {
    version++; stored = hasValue; dirty = false;
    input.type = 'password'; input.value = stored ? mask : '';
    updateButton(false);
  };
  input.onfocus = () => { if (stored && !dirty && input.type === 'password') input.select(); };
  input.oninput = () => { version++; dirty = true; };
  button.onclick = run(async () => {
    const show = input.type === 'password', currentVersion = version;
    if (show && stored && !dirty) {
      button.disabled = true;
      try {
        const result = await api(endpoint, 'POST', {});
        if (version !== currentVersion || !input.isConnected) return;
        input.value = result.Token;
      } finally { button.disabled = false; }
    }
    input.type = show ? 'text' : 'password';
    if (!show && stored && !dirty) input.value = mask;
    updateButton(show);
  });
  reset(hasSaved);
  return { value: () => dirty ? input.value : '', reset };
}

function confirmDialog(title, message, action = "确认清空") {
  return new Promise(resolve => {
    const d = UI.ActionSheet(title, [
      {label:action, danger:true, action:() => { resolve(true); d.close(); }},
      {label:"取消", action:() => d.close()},
    ], UI.el("p", {}, message));
    d.querySelector(".action-sheet-item--danger").dataset.confirm = "";
    d.querySelector(".action-sheet-item--cancel").dataset.cancel = "";
    d.addEventListener("close", () => resolve(false), {once:true});
  });
}

function outsideDialog(event, dialog) {
  const r = dialog.getBoundingClientRect();
  return event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom;
}

async function loadTelegramSettings() {
  const c = await api("/admin/telegram");
  const f = $("#telegram-settings");
  if (!f) return;
  f.innerHTML = `<label class="toggle-label">启用 Telegram Bot<input class="switch" name="enabled" role="switch" type="checkbox" ${c.telegram_enabled ? "checked" : ""}></label>
    <div class="telegram-field"><label for="secret-token">Bot Token</label><a href="https://t.me/BotFather" target="_blank" rel="noopener noreferrer">@BotFather ↗</a></div>
    ${secretField('token', 'Bot Token', '输入 Bot Token')}
    <div class="telegram-field"><label for="telegram-chat">User / Chat ID</label><a href="https://t.me/userinfobot" target="_blank" rel="noopener noreferrer">获取 User ID ↗</a></div>
    <input id="telegram-chat" name="chat" maxlength="128" value="${esc(c.telegram_chat_id)}" autocomplete="off">
    <div class="telegram-notify"><label class="toggle-label">启用通知<input class="switch" name="notify" role="switch" type="checkbox" ${c.telegram_notify_enabled ? "checked" : ""}></label><button type="button" class="secondary icon-button" data-notifications title="通知类型" aria-label="选择通知类型">${fmIcon("gear")}</button></div>
    <p>先与机器人开始对话。测试发送会保存当前设置；留空 Token 会保留已保存的值。</p>
    <div class="bar form-actions"><button type="button" class="secondary" data-test>测试发送</button><button type="submit">保存设置</button></div>`;
  let newMedia = c.telegram_notify_new_media, playback = c.telegram_notify_playback;
  const toggle = () => {
    f.elements.notify.disabled = !f.elements.enabled.checked;
    f.querySelector('[data-notifications]').disabled = !f.elements.enabled.checked || !f.elements.notify.checked;
    f.querySelector('[data-test]').disabled = !f.elements.enabled.checked;
  };
  f.elements.enabled.onchange = toggle;
  f.elements.notify.onchange = toggle;
  toggle();
  const secret = bindSecretField(f.elements.token, '/admin/telegram/secret', 'Bot Token', !!c.telegram_token);
  f.querySelector('[data-notifications]').onclick = () => fmDialog("通知类型", `<label class="toggle-label">新入库通知<input class="switch" name="newMedia" role="switch" type="checkbox" ${newMedia ? "checked" : ""}></label><label class="toggle-label">用户播放通知<input class="switch" name="playback" role="switch" type="checkbox" ${playback ? "checked" : ""}></label>`, data => {
    newMedia = data.has("newMedia"); playback = data.has("playback");
  });
  const save = async () => {
    const saved = await api("/admin/telegram", "PUT", {
      telegram_enabled: f.elements.enabled.checked,
      telegram_token: secret.value(),
      telegram_chat_id: f.elements.chat.value,
      telegram_notify_enabled: f.elements.notify.checked,
      telegram_notify_new_media: newMedia,
      telegram_notify_playback: playback,
    });
    secret.reset(!!saved.telegram_token);
  };
  f.onsubmit = run(async e => { e.preventDefault(); await save(); toast("设置已保存"); });
  f.querySelector('[data-test]').onclick = async e => {
    const button = e.currentTarget; button.disabled = true;
    try { await save(); await api("/admin/telegram/test", "POST", {}); toast("发送成功"); }
    catch (error) { toast("发送失败：" + (error.message || "请稍后重试")); }
    finally { toggle(); }
  };
}

async function loadSubtitleSettings() {
  const c = await api('/admin/subtitle');
  const f = $('#subtitle-settings');
  if (!f) return;
  f.className = 'tmdb-settings-form';
  f.innerHTML = `<label class="tmdb-switch-row"><input class="switch" role="switch" name="enabled" type="checkbox" ${c.Enabled ? 'checked' : ''}><span>自动匹配中文字幕（优先简体）</span></label>
    <label class="tmdb-switch-row"><input class="switch" role="switch" name="clean" type="checkbox" ${c.AutoClean ? 'checked' : ''}><span>播放结束自动清除字幕缓存</span></label>
    <label class="tmdb-switch-row"><input class="switch" role="switch" name="beside" type="checkbox" ${c.SaveBesideMedia ? 'checked' : ''}><span>外挂字幕保存到视频所在目录</span></label>
    <div class="tmdb-field tmdb-token-field"><label for="secret-assrt">ASSRT Token</label>${secretField('assrt', 'ASSRT Token', '输入 ASSRT API Token')}</div>
    <label class="tmdb-field tmdb-inline-field"><span>字幕缓存目录</span><input name="directory" value="${esc(c.Directory)}" required></label>
    <p class="tmdb-help">字幕服务由 <a href="https://assrt.net" target="_blank" rel="noopener noreferrer">assrt.net</a> 提供。优先简体中文，其次繁体中文；没有可信中文候选时跳过。后台下载完成后，客户端重新请求媒体信息即可发现字幕。</p>
    <div class="tmdb-actions"><button>保存</button><button type="button" class="danger" id="clear-subtitles">清除字幕缓存</button></div>`;
  const secret = bindSecretField(f.elements.assrt, '/admin/subtitle/secret', 'ASSRT Token', c.TokenConfigured);
  f.onsubmit = run(async e => {
    e.preventDefault();
    const saved = await api('/admin/subtitle', 'PUT', {Enabled:f.elements.enabled.checked, AutoClean:f.elements.clean.checked, SaveBesideMedia:f.elements.beside.checked, Directory:f.elements.directory.value, Token:secret.value()});
    secret.reset(saved.TokenConfigured); toast('字幕增强设置已保存');
  });
  $('#clear-subtitles').onclick = run(async () => {
    if (!(await confirmDialog('清除字幕缓存', '确定清除全部外挂字幕缓存吗？\n此操作不会删除视频文件和内封字幕。', '清除字幕缓存'))) return;
    await api('/admin/subtitle/cache', 'DELETE');
    toast('字幕缓存已清除');
  });
}

async function loadIntroSettings() {
  const c=await api('/admin/intro-credits'), f=$('#intro-settings');
  if (!f) return;
  f.innerHTML=`<label class="tmdb-switch-row"><input class="switch" role="switch" name="enabled" type="checkbox" ${c.Enabled?'checked':''}><span>开启片头片尾</span></label>
    <label class="tmdb-switch-row"><input class="switch" role="switch" name="intro" type="checkbox" ${c.AutoIntro?'checked':''}><span>自动学习片头</span></label>
    <label class="tmdb-switch-row"><input class="switch" role="switch" name="credits" type="checkbox" ${c.AutoCredits?'checked':''}><span>自动学习片尾</span></label>
    <label class="tmdb-switch-row"><input class="switch" role="switch" name="skipIntro" type="checkbox" ${c.AutoSkipIntro?'checked':''}><span>自动跳过片头</span></label>
    <label class="tmdb-switch-row"><input class="switch" role="switch" name="skipCredits" type="checkbox" ${c.AutoSkipCredits?'checked':''}><span>自动跳过片尾</span></label>
    <label class="tmdb-field tmdb-inline-field"><span>片头探测窗口（分钟）</span><input name="window" type="number" min="1" max="30" required value="${c.WindowMinutes}"></label>
    <label class="tmdb-field tmdb-inline-field"><span>最小有效样本</span><input name="samples" type="number" min="2" max="20" required value="${c.MinSamples}"></label>
    <label class="tmdb-field tmdb-inline-field"><span>候选误差（秒）</span><input name="tolerance" type="number" min="1" max="120" required value="${c.ToleranceSeconds}"></label>
    <label class="tmdb-field tmdb-inline-field"><span>片头片尾数据目录</span><input name="directory" type="text" required value="${esc(c.Directory)}"></label>
    <p class="tmdb-help">仅从剧集播放和跳转行为学习。同一季多集候选稳定后，向客户端返回标准章节标记。片头自动跳过使用 Emby 的 AutoSkip 模式；片尾是否自动跳过取决于客户端，Emby 原生客户端通常显示“下一集”按钮。</p>
    <div class="tmdb-actions"><button>保存</button><button type="button" class="danger" data-clear>清除学习记录</button></div>`;
  f.onsubmit=run(async e=>{e.preventDefault();await api('/admin/intro-credits','PUT',{Enabled:f.elements.enabled.checked,AutoIntro:f.elements.intro.checked,AutoCredits:f.elements.credits.checked,AutoSkipIntro:f.elements.skipIntro.checked,AutoSkipCredits:f.elements.skipCredits.checked,Directory:f.elements.directory.value,WindowMinutes:Number(f.elements.window.value),MinSamples:Number(f.elements.samples.value),ToleranceSeconds:Number(f.elements.tolerance.value)});toast('片头片尾设置已保存')});
  f.querySelector('[data-clear]').onclick=run(async()=>{if(!await confirmDialog('清除学习记录','确定清除全部已确认的片头片尾标记和当前学习记录吗？','清除学习记录'))return;await api('/admin/intro-credits/records','DELETE');toast('学习记录已清除')});
}
async function loadProxySettings() {
  const c=await api('/admin/proxy-settings'),f=$('#proxy-settings');
  if(!f)return;
  const scopes=[['tmdb','TMDB'],['subtitle','字幕'],['update','更新检测'],['license','授权服务'],['generic','其它外部 HTTP']];
  f.innerHTML=`<label class="tmdb-switch-row"><input class="switch" role="switch" name="enabled" type="checkbox" ${c.Enabled?'checked':''}><span>启用代理</span></label>
    <label class="tmdb-field tmdb-inline-field"><span>代理类型</span><select name="type"><option>HTTP</option><option>HTTPS</option><option>SOCKS5</option></select></label>
    <label class="tmdb-field tmdb-inline-field"><span>代理地址</span><input name="url" type="url" placeholder="http://127.0.0.1:7890" value="${esc(c.URL)}"></label>
    <label class="tmdb-field tmdb-inline-field"><span>用户名（可选）</span><input name="username" autocomplete="off" value="${esc(c.Username)}"></label>
    <label class="tmdb-field tmdb-inline-field"><span>密码（可选）</span><input name="password" type="password" autocomplete="new-password" placeholder="${c.PasswordConfigured?'已设置，留空保留':'留空则不使用密码'}"></label>
    <fieldset><legend>代理范围</legend>${scopes.map(([key,label])=>`<label class="tmdb-switch-row"><input class="switch" role="switch" type="checkbox" name="scope-${key}" ${c.Scopes?.[key]?'checked':''}><span>${label}</span></label>`).join('')}</fieldset>
    <p class="tmdb-help">仅影响服务主动发出的外部 API 请求。播放源和视频流继续使用原有连接。</p>
    <div class="tmdb-actions"><button>保存</button><button type="button" class="secondary" data-test>测试连接</button></div><p data-result role="status" aria-live="polite"></p>`;
  f.elements.type.value=c.Type||'HTTP';
  const values=()=>({Enabled:f.elements.enabled.checked,Type:f.elements.type.value,URL:f.elements.url.value,Username:f.elements.username.value,Password:f.elements.password.value,Scopes:Object.fromEntries(scopes.map(([key])=>[key,f.elements[`scope-${key}`].checked]))});
  f.onsubmit=run(async e=>{e.preventDefault();const saved=await api('/admin/proxy-settings','PUT',values());f.elements.password.value='';f.elements.password.placeholder=saved.PasswordConfigured?'已设置，留空保留':'留空则不使用密码';toast('代理设置已保存',{type:'success'})});
  f.querySelector('[data-test]').onclick=async e=>{const button=e.currentTarget,result=f.querySelector('[data-result]');button.disabled=true;result.textContent='正在测试连接…';try{const b=await api('/admin/proxy-settings/test','POST',values());result.textContent=`连接成功 · ${b.LatencyMs} ms`}catch(err){result.textContent=`连接失败：${err.message||'请检查代理设置'}`}finally{button.disabled=false}};
}

async function loadConsole() {
  const host = document.querySelector('#admin-console');
  if (!host || view !== 'admin' || !host.closest('.admin-section') || host.closest('.admin-section').hidden) return;
  const controller = new AbortController();
  consoleController = controller;
  try {
    const data = await api('/admin/dashboard', 'GET', undefined, {signal:controller.signal});
    if (controller.signal.aborted || !host.isConnected || host.closest('.admin-section').hidden || view !== 'admin') return;
    const duration = seconds => `${Math.floor(seconds/86400)}天 ${Math.floor(seconds%86400/3600)}小时`;
    const stat = (label, value, note='') => `<div class="dashboard-stat"><span>${label}</span><strong>${value}</strong>${note ? `<small>${note}</small>` : ''}</div>`;
    host.innerHTML = `<div class="dashboard-status"><span class="dashboard-status-dot" aria-hidden="true"></span>服务正常 · 运行 ${duration(data.uptimeSeconds)}</div><div class="dashboard-grid">${stat('电影',data.movieCount)}${stat('电视剧',data.seriesCount)}${stat('剧集',data.episodeCount)}${stat('用户',data.userCount)}</div><div class="dashboard-grid">${stat('CPU',`${Number(data.cpuPercent||0).toFixed(1)}%`,'AI Emby 进程')}${stat('内存',`${Math.round(data.memoryBytes/1048576)} MB`,'AI Emby 进程占用')}${stat('运行时长',duration(data.uptimeSeconds))}${stat('正在播放',data.activePlaybackCount)}</div><section class="dashboard-playing"><h3>正在播放</h3>${data.activePlayback.length ? data.activePlayback.map(x => `<article class="dashboard-playing-item"><div><strong>${esc(x.username||'未知用户')}</strong><span>${esc(x.mediaName||'未知媒体')}</span></div><small>${esc([x.device,x.client].filter(Boolean).join(' · '))}</small><div class="dashboard-playing-progress"><progress max="100" value="${Math.max(0,Math.min(100,Number(x.progressPercent)||0))}"></progress><span>${Math.round(x.progressPercent||0)}%</span></div></article>`).join('') : '<p class="dashboard-empty">当前没有正在播放</p>'}</section>`;
  } catch (e) { if (!controller.signal.aborted && host.isConnected) host.textContent = '控制台读取失败：' + e.message; }
  if (consoleController === controller) consoleController = null;
  if (!controller.signal.aborted && host.isConnected && !host.closest('.admin-section').hidden && view === 'admin') consoleTimer = setTimeout(loadConsole, 3000);
}
