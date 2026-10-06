const Features = (() => {
  const names = {
    14: "任务中心",
    15: "定时任务",
    16: "媒体库设置",
    17: "播放与记录",
    18: "缓存管理",
    19: "登录设备",
    20: "资料导入",
    21: "访问与网络",
    22: "封面展示",
    23: "网盘挂载",
    24: "追新索引",
    25: "资源搬运",
    26: "防盗保护",
  };
  let timer,
    sequence = 0,
    discoveryRequest = 0;
  const endpoint = (name) => "/admin/features/" + name;
  const field = (name, label, value, type = "text", extra = "") =>
    `<label>${label}<input name="${name}" type="${type}" value="${esc(value ?? "")}" ${extra}></label>`;
  const check = (name, label, value) =>
    `<label class="feature-check"><input name="${name}" type="checkbox" ${value ? "checked" : ""}>${label}</label>`;
  const select = (name, label, value, options) =>
    `<label>${label}<select name="${name}">${options.map(([id, label]) => `<option value="${esc(id)}" ${String(id) === String(value) ? "selected" : ""}>${esc(label)}</option>`).join("")}</select></label>`;
  const parseForm = (form, base) => {
    const data = new FormData(form),
      out = { ...base };
    for (const input of form.elements) {
      if (!input.name) continue;
      out[input.name] =
        input.type === "checkbox"
          ? input.checked
          : input.type === "number"
            ? Number(input.value)
            : data.get(input.name);
    }
    return out;
  };
  const seconds = (n) =>
    n >= 3600 ? `${(n / 3600).toFixed(1)} 小时` : `${Math.round(n / 60)} 分钟`;
  const bytes = (n) => `${((n || 0) / 1024 / 1024 / 1024).toFixed(2)} GB`;
  const date = (v) =>
    v ? new Date(typeof v === "number" ? v * 1000 : v).toLocaleString() : "—";
  const states = {
    waiting: "等待",
    queued: "队列中",
    running: "处理中",
    counting: "读取目录",
    cleaning: "整理索引",
    paused: "暂停",
    complete: "完成",
    error: "失败",
    cancelled: "取消",
    interrupted: "已中断",
    "reading-disc": "读取光盘目录",
    caching: "完整镜像下载中",
    extracting: "提取正片中",
    probing: "检查媒体格式",
    remuxing: "封装播放视频",
    transcoding: "转码中",
    stopping: "停止中",
    stopped: "已停止",
    deleting: "删除中",
    "delete-failed": "删除失败",
    failed: "失败",
  };
  function ensureSections() {
    const app = $("#app");
    if (!app?.querySelector(".admin-section")) return;
    for (const [index, title] of Object.entries(names)) {
      if (!app.querySelector(`[data-feature-page="${index}"]`)) {
        const section = UI.el("section", {
          class: "panel admin-section feature-section",
          hidden: "",
          "data-feature-page": index,
        });
        app.append(section);
      }
    }
  }
  function table(headers, rows) {
    return `<div class="feature-table-wrap"><table class="feature-table"><thead><tr>${headers.map((x) => `<th>${x}</th>`).join("")}</tr></thead><tbody>${rows.length ? rows.join("") : `<tr><td colspan="${headers.length}" class="empty">暂无记录</td></tr>`}</tbody></table></div>`;
  }
  function saveForm(host, html, submit) {
    host.innerHTML = `<form class="feature-form">${html}<div class="feature-actions"><button>保存设置</button></div></form>`;
    const form = host.querySelector("form");
    form.onsubmit = run(async (e) => {
      e.preventDefault();
      const button = form.querySelector(
        'button[type="submit"],.feature-actions button',
      );
      button.disabled = true;
      try {
        await submit(form);
        toast("设置已保存");
      } finally {
        button.disabled = false;
      }
    });
  }
  async function load(index) {
    clearTimeout(timer);
    const serial = ++sequence,
      host = $(`[data-feature-page="${index}"]`);
    if (!host) return;
    host.replaceChildren(UI.LoadingSkeleton());
    const still = () =>
      serial === sequence &&
      host.isConnected &&
      view === "admin" &&
      !host.hidden;
    try {
      if (index === 26) {
        await antiTheft(host, still);
        return;
      }
      if (index === 25) {
        await CloudTransfers.load(host, still);
        return;
      }
      if (index === 24) {
        await Tracking.load(host, still);
        return;
      }
      if (index === 23) {
        await CloudMounts.load(host, still);
        return;
      }
      if (index === 14) {
        await tasks(host, still);
        return;
      }
      if (index === 15) {
        const c = await api(endpoint("schedule"));
        if (!still()) return;
        saveForm(
          host,
          `<div class="feature-grid">${check("Enabled", "启用定时任务", c.Enabled)}${check("DeferPlayback", "播放期间推迟执行", c.DeferPlayback)}${select(
            "Mode",
            "执行内容",
            c.Mode,
            [
              ["update", "刷新扫描"],
              ["scan", "全量扫描"],
            ],
          )}${select("Frequency", "周期", c.Frequency, [
            ["daily", "每天"],
            ["weekly", "每周"],
            ["minutes", "分钟间隔"],
          ])}${field("Time", "时间（UTC）", c.Time, "time", "required")}${select(
            "Weekday",
            "星期",
            c.Weekday,
            [
              [0, "星期日"],
              [1, "星期一"],
              [2, "星期二"],
              [3, "星期三"],
              [4, "星期四"],
              [5, "星期五"],
              [6, "星期六"],
            ],
          )}${field("Minutes", "间隔分钟", c.Minutes, "number", 'min="1" max="10080" required')}${field("MaxDeferralMinutes", "最多推迟分钟", c.MaxDeferralMinutes, "number", 'min="0" max="1440"')}${check("Cleanup", "定时清理过期播放缓存", c.Cleanup)}</div><p class="feature-inline">下次执行：${date(c.Next)}${c.DeferredSince ? " · 正在等待播放结束" : ""}</p><button type="button" class="secondary" id="feature-run-schedule">立即执行</button>`,
          (f) => api(endpoint("schedule"), "PUT", parseForm(f, c)),
        );
        host.querySelector("#feature-run-schedule").onclick = run(async () => {
          const b = await api(endpoint("schedule"), "POST", {});
          toast(`已加入 ${b.Queued} 个媒体库`);
        });
      }
      if (index === 16) {
        const libs = await api(endpoint("libraries"));
        if (!still()) return;
        host.innerHTML = table(
          ["媒体库", "自动处理", "扫描并发", "操作"],
          libs.map(
            (lib) =>
              `<tr><td><strong>${esc(lib.Name)}</strong><small>${lib.Locations.map(esc).join("<br>")}</small></td><td>${
                [
                  ["Watch", "目录监控"],
                  ["AutoMetadata", "资料补全"],
                  ["Probe", "媒体信息"],
                  ["Capture", "缺图截帧"],
                  ["Intro", "片头片尾章节"],
                ]
                  .filter(([key]) => lib.Policy[key])
                  .map(([, label]) => label)
                  .join(" · ") || "手动处理"
              }</td><td>${lib.Policy.FileConcurrency || "使用全局"}</td><td><button class="secondary" data-policy="${lib.Id}">设置</button><button class="secondary" data-job="${lib.Id}">执行任务</button></td></tr>`,
          ),
        );
        host
          .querySelectorAll("[data-policy]")
          .forEach(
            (b) =>
              (b.onclick = run(() =>
                libraryPolicy(libs.find((x) => x.Id === b.dataset.policy)),
              )),
          );
        host
          .querySelectorAll("[data-job]")
          .forEach((b) => (b.onclick = () => libraryActions(b.dataset.job)));
      }
      if (index === 17) {
        const b = await api(endpoint("playback"));
        if (!still()) return;
        saveForm(
          host,
          `<p class="muted">播放保护已移至「防盗保护」模块。</p><div class="feature-grid">${check("Transcode", "启用浏览器转码", b.Settings.Transcode)}${field("Threads", "每个任务的编码线程", b.Settings.Threads, "number", 'min="1" max="32" required')}${field("Concurrency", "同时转码任务", b.Settings.Concurrency, "number", 'min="1" max="8" required')}${field("Bitrate", "视频码率（Kbps）", b.Settings.Bitrate, "number", 'min="500" max="50000" required')}${field("CacheGB", "播放缓存上限（GB）", b.Settings.CacheGB, "number", 'min="1" max="2000" required')}${field("RetentionDays", "保留天数", b.Settings.RetentionDays, "number", 'min="1" max="365" required')}${check("DanmakuEnabled", "启用弹幕服务", b.Settings.DanmakuEnabled)}${field("DanmakuURL", "弹幕 XML 地址模板", b.Settings.DanmakuURL, "text", 'placeholder="https://服务地址/{tmdb}/{season}/{episode}.xml"')}</div>`,
          (f) => api(endpoint("playback"), "PUT", parseForm(f, b.Settings)),
        );
        host.insertAdjacentHTML(
          "beforeend",
          `<h3>播放记录</h3>${table(
            ["用户", "作品", "日期", "观看时长", "播放次数"],
            b.History.map(
              (x) =>
                `<tr><td>${esc(x.User)}</td><td>${esc(x.Name || "已移除作品")}</td><td>${esc(x.Day)} UTC</td><td>${seconds(x.Seconds)}</td><td>${x.Count}</td></tr>`,
            ),
          )}`,
        );
      }
      if (index === 18) {
        const b = await api(endpoint("cache"));
        if (!still()) return;
        host.innerHTML = `<div class="feature-stats"><article><small>播放缓存</small><strong>${bytes(b.Size)}</strong><span>上限 ${bytes(b.Limit)}</span></article><article><small>作品资料缓存</small><strong>${bytes(b.MetadataSize)}</strong></article><article><small>本地弹幕</small><strong>${bytes(b.DanmakuSize)}</strong></article></div><div class="feature-actions"><button class="secondary" id="feature-clean-cache">清理空闲播放缓存</button><button class="secondary" onclick="Features.load(18)">刷新</button></div>${table(
          ["作品", "缓存大小", "处理方式", "最近使用", "状态", "操作"],
          b.Items.map(
            (x) =>
              `<tr><td>${esc(x.Name || x.ID)}${x.Error ? `<small>${esc(x.Error)}</small>` : ""}</td><td>${bytes(x.Size)}${x.State === "caching" && x.Total > 0 ? `<small>镜像下载 ${Math.min(100, Math.floor(x.Downloaded / x.Total * 100))}%</small>` : ""}</td><td>${["range", "range-bluray"].includes(x.SourceMode) ? "按需读取，无需完整下载" : ["local", "local-bluray"].includes(x.SourceMode) ? "本地镜像按需读取" : x.SourceMode === "cache" ? "完整镜像缓存" : "网页播放缓存"}${x.Stream ? "<small>实时输出，不缓存视频</small>" : ""}${x.FallbackReason ? `<small>${esc(x.FallbackReason)}</small>` : ""}</td><td>${date(x.Updated)}</td><td>${x.Paused ? "已暂停" : states[x.State] || (x.Active ? "使用中" : "空闲")}</td><td><div class="feature-actions">${!x.Done && x.State && !["interrupted", "stopping", "deleting"].includes(x.State) ? `<button class="secondary" data-playback-control="${x.ID}" data-action="${x.Paused ? "resume" : "pause"}">${x.Paused ? "继续" : "暂停"}</button><button class="secondary" data-playback-control="${x.ID}" data-action="stop">停止</button>` : ""}${x.State ? `<button class="secondary" data-playback-control="${x.ID}" data-action="delete" ${x.State === "deleting" ? "disabled" : ""}>删除任务和缓存</button>` : `<button class="secondary" data-cache="${x.ID}" ${x.Active ? "disabled" : ""}>删除缓存</button>`}</div></td></tr>`,
          ),
        )}`;
        const clean = async (data) => {
          const r = await api(endpoint("cache"), "DELETE", data);
          toast(`清理 ${r.Removed} 项，保护 ${r.Protected} 项`);
          await load(18);
        };
        host.querySelector("#feature-clean-cache").onclick = run(() =>
          clean({ All: true }),
        );
        host
          .querySelectorAll("[data-cache]")
          .forEach(
            (b) => (b.onclick = run(() => clean({ IDs: [b.dataset.cache] }))),
          );
        host.querySelectorAll("[data-playback-control]").forEach(button => {
          button.onclick = run(async () => {
            button.disabled = true;
            try {
              await api(endpoint("playback-control"), "POST", {ID:button.dataset.playbackControl, Action:button.dataset.action});
              toast({pause:"后台任务已暂停", resume:"后台任务已继续", stop:"正在停止任务", delete:"正在删除任务和缓存"}[button.dataset.action]);
              await load(18);
            } finally { button.disabled = false; }
          });
        });
        if (b.Items.some(x => x.State === "deleting" || x.State === "stopping"))
          timer = setTimeout(() => { if (still()) load(18); }, 1000);
      }
      if (index === 19) {
        const b = await api(endpoint("devices"));
        if (!still()) return;
        host.innerHTML = table(
          ["用户", "设备", "客户端", "最近播放", "操作"],
          b.Items.map(
            (x, i) =>
              `<tr><td>${esc(x.User)}</td><td>${esc(x.Name)}<small>${x.Persistent ? "保持登录" : "普通登录"}${x.Playing ? " · 正在播放" : ""}</small></td><td>${esc(x.Client || "—")}</td><td>${date(x.LastSeen)}</td><td><button class="secondary" data-device="${i}">重命名</button><button class="danger" data-revoke="${i}">注销设备</button></td></tr>`,
          ),
        );
        host.querySelectorAll("[data-device]").forEach(
          (el) =>
            (el.onclick = () => {
              const x = b.Items[el.dataset.device];
              fmDialog("设备名称", field("Name", "名称", x.Name), async (f) => {
                await api(endpoint("devices"), "PUT", {
                  ...x,
                  Name: f.get("Name"),
                });
                await load(19);
              });
            }),
        );
        host.querySelectorAll("[data-revoke]").forEach(
          (el) =>
            (el.onclick = run(async () => {
              const x = b.Items[el.dataset.revoke];
              if (
                await confirmDialog(
                  "注销设备",
                  `注销 ${x.User} 的 ${x.Name}？该设备需要重新登录。`,
                  "注销",
                )
              ) {
                await api(endpoint("devices"), "DELETE", x);
                await load(19);
              }
            })),
        );
      }
      if (index === 20) {
        importPage(host);
      }
      if (index === 21) {
        const c = await api(endpoint("network"));
        if (!still()) return;
        saveForm(
          host,
          `${field("PublicURL", "外部访问地址", c.PublicURL, "url", 'placeholder="https://media.example.com"')}<label>允许的访问主机（每行一个，留空不限）<textarea name="AllowedHosts" rows="4" placeholder="media.example.com\n192.168.1.10:8097">${esc((c.AllowedHosts || []).join("\n"))}</textarea></label><label>允许的跨域来源（每行一个）<textarea name="AllowedOrigins" rows="4" placeholder="https://app.example.com">${esc((c.AllowedOrigins || []).join("\n"))}</textarea></label>`,
          (f) => {
            const c = parseForm(f, {});
            c.AllowedHosts = c.AllowedHosts.split("\n")
              .map((x) => x.trim())
              .filter(Boolean);
            c.AllowedOrigins = c.AllowedOrigins.split("\n")
              .map((x) => x.trim())
              .filter(Boolean);
            return api(endpoint("network"), "PUT", c);
          },
        );
      }
      if (index === 22) {
        const [c, libs] = await Promise.all([
          api(endpoint("covers")),
          api(endpoint("libraries")),
        ]);
        if (!still()) return;
        saveForm(
          host,
          `<div class="feature-grid">${select("Font", "标题字体", c.Font, [
            ["system-ui", "系统字体"],
            ["serif", "衬线字体"],
            ["sans-serif", "无衬线字体"],
          ])}${field("Size", "标题字号", c.Size, "number", 'min="12" max="48" required')}${check("Wrap", "标题允许换行", c.Wrap)}${field("Blur", "背景模糊", c.Blur, "number", 'min="0" max="30"')}</div><div class="feature-cover-preview"><span style="font-family:${esc(c.Font)};font-size:${c.Size}px">你的电影与剧集</span></div>`,
          async (f) => {
            await api(endpoint("covers"), "PUT", parseForm(f, c));
            await applyAppearance();
          },
        );
        host.insertAdjacentHTML(
          "beforeend",
          `<h3>生成媒体库拼贴封面</h3>${table(
            ["媒体库", "操作"],
            libs.map(
              (x) =>
                `<tr><td>${esc(x.Name)}</td><td><button class="secondary" data-cover="${x.Id}">生成封面</button></td></tr>`,
            ),
          )}`,
        );
        host.querySelectorAll("[data-cover]").forEach(
          (b) =>
            (b.onclick = run(async () => {
              b.disabled = true;
              try {
                await api(endpoint("covers"), "POST", {
                  Library: b.dataset.cover,
                });
                toast("媒体库封面已生成");
              } finally {
                b.disabled = false;
              }
            })),
        );
      }
    } catch (error) {
      if (still()) {
        host.innerHTML = `<div role="alert"><p>${esc(error.message)}</p><button class="secondary" onclick="Features.load(${index})">重新加载</button></div>`;
      }
    }
  }
  async function antiTheft(host, still) {
    const b = await api(endpoint("anti-theft"));
    if (!still()) return;
    const c = b.Settings, actions = {warn:"警告",temporary:"临时封禁",permanent:"永久封禁",unban:"解除封禁"};
    host.innerHTML = `<div data-protection-settings></div><h3>账号上限与封禁</h3><p class="muted">留空使用全局上限；0 表示该账号不限次数。管理员免于自动限频和封禁，便于管理与恢复。</p><div data-protection-accounts></div><div class="feature-actions"><button type="button" class="secondary" data-protection-refresh>刷新账号与记录</button></div><h3>防盗保护记录</h3><p class="muted">展示最近 100 条记录。警告模式允许继续播放，同一账号每分钟最多记录一次警告。</p><div data-protection-events></div>`;
    saveForm(host.querySelector('[data-protection-settings]'),
      `${check("ProtectCloudPlayback", "开启播放链接保护", c.ProtectCloudPlayback)}<p class="muted">默认开启。本服务生成的网盘 STRM 需通过登录后的播放入口访问，已有文件无需重新生成。网盘配置为 302 时继续直连播放；最终网盘链接仍受网盘自身有效期限制。</p><h3>播放请求频率限制</h3><div class="feature-grid">${check("RateLimitEnabled", "开启账号请求频率限制", c.RateLimitEnabled)}${field("RequestsPerMinute", "每个账号每分钟请求上限", c.RequestsPerMinute, "number", 'min="1" max="10000" required')}${select("Action", "超限处理", c.Action, [["warn","仅警告，允许继续播放"],["temporary","临时封禁账号"],["permanent","永久封禁账号"]])}${field("TemporaryBanMinutes", "临时封禁时长（分钟）", c.TemporaryBanMinutes, "number", 'min="1" max="43200" required')}</div><p class="muted">统计任意连续 60 秒内的 GET / HEAD 播放入口请求，同一账号的所有设备合并计数。海报、扫库、播放信息查询、进度上报与服务器内部媒体任务不计数。临时封禁到期自动恢复；永久封禁需在下方手动解除。关闭频率限制不会解除已有封禁。</p>`,
      async form => { await api(endpoint("anti-theft"), "PUT", parseForm(form, c)); await load(26); }
    );
    const settingsForm = host.querySelector('[data-protection-settings] form');
    const syncDuration = () => {
      settingsForm.elements.TemporaryBanMinutes.closest('label').hidden = settingsForm.elements.Action.value !== 'temporary';
    };
    settingsForm.elements.Action.addEventListener('change', syncDuration);
    syncDuration();
    host.querySelector('[data-protection-accounts]').innerHTML = table(["账号","每分钟上限","状态","操作"], b.Accounts.map((account, index) => {
      const state = account.Admin ? "管理员免限频" : account.Banned ? account.Permanent ? "永久封禁" : `临时封禁至 ${date(account.BlockedUntil)}` : "正常";
      return `<tr><td>${esc(account.Name)}</td><td>${account.Admin ? "—" : `<input type="number" data-account-limit="${index}" min="0" max="10000" placeholder="全局 ${c.RequestsPerMinute}" value="${account.RequestsPerMinute ?? ''}" aria-label="${esc(account.Name)}每分钟请求上限">`}</td><td>${esc(state)}</td><td>${account.Admin ? "—" : `<button type="button" class="secondary" data-account-save="${index}">保存上限</button> ${account.Banned ? `<button type="button" class="secondary" data-account-unban="${index}">解除封禁</button>` : ''}`}</td></tr>`;
    }));
    host.querySelector('[data-protection-events]').innerHTML = table(["时间","账号","处理","触发次数 / 上限","恢复时间"], b.Events.map(event => `<tr><td>${date(event.Created)}</td><td>${esc(event.Name)}</td><td>${esc(actions[event.Action] || event.Action)}</td><td>${event.Action === 'unban' ? '—' : `${event.Count} / ${event.Limit}`}</td><td>${event.Action === 'permanent' ? '需手动解除' : date(event.BlockedUntil)}</td></tr>`));
    host.querySelector('[data-protection-refresh]').onclick = run(() => load(26));
    host.querySelectorAll('[data-account-save]').forEach(button => button.onclick = run(async () => {
      const index = button.dataset.accountSave, input = host.querySelector(`[data-account-limit="${index}"]`);
      if (!input.reportValidity()) return;
      await api(endpoint('anti-theft/account'), 'PUT', {UserID:b.Accounts[index].UserID, RequestsPerMinute:input.value === '' ? null : Number(input.value)});
      toast('账号上限已保存');
    }));
    host.querySelectorAll('[data-account-unban]').forEach(button => button.onclick = run(async () => {
      button.disabled = true;
      try {
        await api(endpoint('anti-theft/account'), 'POST', {UserID:b.Accounts[button.dataset.accountUnban].UserID});
        toast('账号封禁已解除');
        await load(26);
      } finally { button.disabled = false; }
    }));
  }

  async function tasks(host, still) {
    const filter = host.dataset.filter || "";
    const b = await api(
      endpoint("tasks") +
        "?" +
        new URLSearchParams({ State: filter, Limit: 200 }),
    );
    if (!still()) return;
    host.innerHTML = `<div class="feature-actions">${[
      ["", "全部"],
      ["active", "运行中"],
      ["problem", "失败与中断"],
    ]
      .map(
        ([id, label]) =>
          `<button class="${filter === id ? "" : "secondary"}" data-task-filter="${id}">${label}</button>`,
      )
      .join(
        "",
      )}<button class="secondary" id="feature-task-refresh">刷新</button></div><div class="feature-stats">${[
      ["running", "处理中"],
      ["queued", "排队"],
      ["complete", "完成"],
      ["error", "失败"],
    ]
      .map(
        ([key, name]) =>
          `<article><small>${name}</small><strong>${b.Counts[key] || 0}</strong></article>`,
      )
      .join("")}</div><div class="feature-task-list">${
      b.Items.map((x) => {
        const e = x.Entry;
        return `<article class="feature-task"><div><strong>${esc(e.Name)}</strong><span class="feature-badge ${["error", "interrupted"].includes(e.State) ? "problem" : ""}">${esc(states[e.State] || e.State)}</span></div><p>${esc(e.Current)}</p><progress max="${Math.max(e.Total || 0, 1)}" value="${e.Done || 0}"></progress><small>${e.Done || 0} / ${e.Total || 0} · ${seconds(x.Duration)} · ${date(e.Started)}</small>${e.Error ? `<p class="feature-error">${esc(e.Error)}</p>` : ""}<details><summary>阶段记录</summary>${(e.Phases || []).map((p) => `<div class="feature-phase"><span>${esc(states[p.Name] || p.Name)}</span><span>${p.Done || 0}/${p.Total || 0}</span><small>${date(p.Started)}</small></div>`).join("")}</details><div class="feature-actions">${e.Category==='cloud-transfer'?`<button class="secondary" data-transfer-detail="${esc(e.ItemID)}">查看搬运任务</button>`:''}${e.Source === 'worker' && ['cloud-strm','tracking'].includes(e.Category) && x.Active ? `<button class="secondary" data-task="${x.ID}" data-action="cancel">取消</button>` : ''}${(e.Source === "worker" && ["scan", "update"].includes(e.Category)) || (e.Source === "worker" && e.Category.startsWith("library-")) ? (!x.Active ? `<button class="secondary" data-task="${x.ID}" data-action="retry">重新执行</button>` : e.Category.startsWith("library-") ? `<button class="secondary" data-task="${x.ID}" data-action="cancel">取消</button>` : "") : ""}</div></article>`;
      }).join("") || '<p class="empty">暂无任务</p>'
    }</div>`;
    host.querySelectorAll("[data-task-filter]").forEach(
      (b) =>
        (b.onclick = run(async () => {
          host.dataset.filter = b.dataset.taskFilter;
          await load(14);
        })),
    );
    host.querySelector("#feature-task-refresh").onclick = run(() => load(14));
    host.querySelectorAll("[data-transfer-detail]").forEach(button=>button.onclick=run(()=>CloudTransfers.details(button.dataset.transferDetail)));
    host.querySelectorAll("[data-task]").forEach(
      (b) =>
        (b.onclick = run(async () => {
          await api(endpoint("task-action"), "POST", {
            ID: b.dataset.task,
            Action: b.dataset.action,
          });
          await load(14);
        })),
    );
    timer = setTimeout(() => {
      if (still()) run(() => tasks(host, still))();
    }, 5000);
  }
  async function libraryPolicy(lib) {
    const users = await api.getUsers();
    const p = lib.Policy;
    fmDialog(
      lib.Name + " · 设置",
      `<div class="feature-grid">${check("Watch", "监控目录变化", p.Watch)}${check("Participate", "参与定时任务", p.Participate)}${check("AutoMetadata", "扫描后补全作品资料", p.AutoMetadata)}${check("Probe", "扫描后提取媒体信息", p.Probe)}${check("Capture", "为缺图媒体截帧", p.Capture)}${check("Intro", "提取片头片尾章节", p.Intro)}${check("RefreshMissingImages", "每天补全缺失图片", p.RefreshMissingImages)}${check("WriteNFO", "将补全资料写入 NFO", p.WriteNFO)}${check("WriteArtwork", "将下载图片写入目录", p.WriteArtwork)}${field("FileConcurrency", "单库文件并发（0 使用全局）", p.FileConcurrency || 0, "number", 'min="0" max="32"')}${select(
        "SortBy",
        "默认排序",
        p.SortBy,
        [
          ["DateCreated", "添加时间"],
          ["SortName", "名称"],
          ["ProductionYear", "年份"],
          ["PremiereDate", "首映日期"],
        ],
      )}${select("SortOrder", "顺序", p.SortOrder, [
        ["Descending", "降序"],
        ["Ascending", "升序"],
      ])}</div>${check("RestrictUsers", "仅允许所选用户访问", p.RestrictUsers)}<div class="feature-user-grid">${users
        .filter((x) => !x.Policy?.IsAdministrator)
        .map((u) =>
          check("Users", esc(u.Name), (p.Users || []).includes(u.Id)).replace(
            'name="Users"',
            `name="Users" value="${u.Id}"`,
          ),
        )
        .join("")}</div>`,
      async (f) => {
        const policy = { ...p };
        for (const k of [
          "Watch",
          "Participate",
          "AutoMetadata",
          "Probe",
          "Capture",
          "Intro",
          "RefreshMissingImages",
          "WriteNFO",
          "WriteArtwork",
          "RestrictUsers",
        ])
          policy[k] = f.has(k);
        policy.Users = f.getAll("Users");
        policy.FileConcurrency = Number(f.get("FileConcurrency"));
        policy.SortBy = f.get("SortBy");
        policy.SortOrder = f.get("SortOrder");
        await api(endpoint("libraries"), "PUT", { ID: lib.Id, Policy: policy });
        await load(16);
      },
      "保存",
    );
  }
  function libraryActions(id) {
    const actions = [
      ["update", "刷新扫描"],
      ["scan", "全量扫描"],
      ["metadata", "补全资料与图片"],
      ["probe", "提取媒体信息"],
      ["capture", "补全封面截帧"],
      ["intro", "读取片头片尾章节"],
    ];
    let sheet;
    sheet = UI.ActionSheet(
      "执行媒体库任务",
      actions.map(([action, label]) => ({
        label,
        action: run(async () => {
          sheet.close();
          await api(endpoint("library-action"), "POST", {
            ID: id,
            Action: action,
          });
          toast("任务已加入队列");
        }),
      })),
    );
  }
  async function metadata(id) {
    await stop();
    const m = await api(endpoint("metadata") + "?ID=" + encodeURIComponent(id));
    fmDialog(
      "编辑媒体资料",
      `<div class="feature-grid">${field("Title", "标题", m.Title, "text", 'required maxlength="1024"')}${field("OriginalTitle", "原始标题", m.OriginalTitle)}${field("Year", "年份", m.Year, "number", 'min="0" max="9999"')}${field("Season", "季编号", m.Season, "number", 'min="0"')}${field("Episode", "集编号", m.Episode, "number", 'min="0"')}${field("Rating", "评分", m.Rating, "number", 'min="0" max="10" step="0.1"')}${field("Premiered", "首映日期", m.Premiered, "date")}${field("TMDB", "TMDB ID", m.TMDB)}${field("IMDB", "IMDb ID", m.IMDB)}${field("MPAA", "分级", m.MPAA)}${field("Genres", "类型（逗号分隔）", (m.Genres || []).join("，"))}${field("Countries", "国家/地区（逗号分隔）", (m.Countries || []).join("，"))}${field("Tags", "标签（逗号分隔）", (m.Tags || []).join("，"))}${field("Tagline", "标语", m.Tagline)}</div><label>简介<textarea name="Plot" rows="6">${esc(m.Plot)}</textarea></label>${check("Locked", "保护资料，自动补全时保留修改", true)}${check("WriteNFO", "同时写入 NFO", false)}`,
      async (f) => {
        const data = { ...m };
        for (const key of [
          "Title",
          "OriginalTitle",
          "Premiered",
          "TMDB",
          "IMDB",
          "MPAA",
          "Tagline",
          "Plot",
        ])
          data[key] = f.get(key);
        for (const key of ["Year", "Season", "Episode", "Rating"])
          data[key] = Number(f.get(key));
        for (const key of ["Genres", "Countries", "Tags"])
          data[key] = f
            .get(key)
            .split(/[,，]/)
            .map((x) => x.trim())
            .filter(Boolean);
        data.Locked = f.has("Locked");
        await api(endpoint("metadata"), "PUT", {
          ID: id,
          Metadata: data,
          WriteNFO: f.has("WriteNFO"),
        });
        toast("媒体资料已保存");
      },
      "保存",
    );
    const reset = UI.el(
      "button",
      {
        type: "button",
        class: "secondary",
        onclick: run(async () => {
          if (
            !(await confirmDialog(
              "恢复本地资料",
              "清除这部作品的已保存资料，并重新扫描所在媒体库？",
              "恢复并扫描",
            ))
          )
            return;
          await api(
            endpoint("metadata") + "?ID=" + encodeURIComponent(id),
            "DELETE",
          );
          await closeModal();
          toast("已加入扫描队列");
        }),
      },
      "恢复本地 NFO 资料",
    );
    $("#modal .form-actions").prepend(reset);
  }
  async function identify(id) {
    const item = await api.getItem(id);
    const type = item.Type === "Series" ? "tv" : "movie";
    const dialog = document.createElement("dialog");
    dialog.className = "feature-dialog";
    dialog.innerHTML = `<div class="feature-dialog-heading"><h2>重新识别 · ${esc(item.Name)}</h2><button class="secondary" data-close>关闭</button></div><form class="feature-search">${select("Type", "类型", type, [[type, type === "tv" ? "剧集" : "电影"]])}<label>标题或 TMDB ID<input name="Query" required value="${esc(item.Name)}"></label><button>搜索</button></form><div class="feature-candidates" aria-live="polite"></div>`;
    document.body.append(dialog);
    dialog.showModal();
    dialog.querySelector("[data-close]").onclick = () => dialog.close();
    dialog.onclose = () => dialog.remove();
    const form = dialog.querySelector("form"),
      results = dialog.querySelector(".feature-candidates");
    form.onsubmit = run(async (e) => {
      e.preventDefault();
      results.textContent = "正在搜索…";
      const q = new URLSearchParams(new FormData(form));
      const b = await api(endpoint("identify") + "?" + q);
      results.innerHTML =
        b.Items.map(
          (x, i) =>
            `<article><img src="${esc(x.Poster || "")}" alt="" loading="lazy"><div><strong>${esc(x.Name)}</strong><small>${esc(x.Date)} · TMDB ${x.ID}</small><p>${esc(x.Overview || "")}</p><button data-match="${i}">使用此匹配</button></div></article>`,
        ).join("") || "<p>没有找到候选，请尝试原名或 TMDB ID。</p>";
      results.querySelectorAll("[data-match]").forEach(
        (button) =>
          (button.onclick = run(async () => {
            const x = b.Items[button.dataset.match];
            if (
              !(await confirmDialog(
                "确认匹配",
                `将「${item.Name}」识别为「${x.Name}」 ${x.Date || ""}（TMDB ${x.ID}）？`,
                "使用此匹配",
              ))
            )
              return;
            button.disabled = true;
            try {
              await api(endpoint("identify"), "POST", {
                ID: id,
                TMDB: String(x.ID),
                Type: x.Type,
              });
              dialog.close();
              toast("新匹配已保存");
            } finally {
              button.disabled = false;
            }
          })),
      );
    });
  }
  async function artwork(id) {
    const dialog = document.createElement("dialog");
    dialog.className = "feature-dialog";
    dialog.innerHTML = `<div class="feature-dialog-heading"><h2>管理图片</h2><button class="secondary" data-close>关闭</button></div><div class="feature-actions"><select aria-label="图片类型" id="feature-image-kind"><option value="Primary">海报 / 单集缩略图</option><option value="Backdrop">背景图</option><option value="Logo">标志</option></select><label class="feature-check"><input type="checkbox" id="feature-image-disk">同时写入媒体目录</label></div><img class="feature-current-art" alt="当前图片"><div class="feature-actions"><button class="secondary" data-upload>上传图片</button><button class="secondary" data-online>选择在线图片</button><button class="secondary" data-reset>恢复默认图片</button><button class="secondary" data-danmaku>上传弹幕 XML</button></div><input type="file" accept="image/jpeg,image/png,image/webp" hidden data-file><input type="file" accept=".xml" hidden data-xml><div class="feature-art-options"></div>`;
    document.body.append(dialog);
    dialog.showModal();
    dialog.querySelector("[data-close]").onclick = () => dialog.close();
    dialog.onclose = () => dialog.remove();
    const kind = dialog.querySelector("select"),
      preview = dialog.querySelector(".feature-current-art"),
      query = () =>
        "?" +
        new URLSearchParams({
          ID: id,
          Kind: kind.value,
          WriteDisk: String(
            dialog.querySelector("#feature-image-disk").checked,
          ),
        });
    const refresh = () => {
      preview.src = api.image(id, kind.value, 600) + "&v=" + Date.now();
    };
    refresh();
    kind.onchange = () => {
      refresh();
      dialog.querySelector(".feature-art-options").replaceChildren();
    };
    const file = dialog.querySelector("[data-file]");
    dialog.querySelector("[data-upload]").onclick = () => file.click();
    file.onchange = run(async () => {
      if (!file.files[0]) return;
      await api(endpoint("artwork") + query(), "POST", file.files[0], {
        raw: true,
      });
      refresh();
      toast("图片已保存");
    });
    dialog.querySelector("[data-reset]").onclick = run(async () => {
      await api(endpoint("artwork") + query(), "DELETE");
      refresh();
      toast("已恢复默认图片");
    });
    dialog.querySelector("[data-online]").onclick = run(async () => {
      const b = await api(endpoint("artwork") + query() + "&Online=true"),
        host = dialog.querySelector(".feature-art-options");
      host.innerHTML =
        b.Candidates.map(
          (x, i) =>
            `<button data-art="${i}" class="secondary"><img src="${esc(x.URL)}" alt="${esc(x.Language || "无语言")} · ${x.Width}×${x.Height}" loading="lazy"></button>`,
        ).join("") || "<p>没有可用图片</p>";
      host.querySelectorAll("[data-art]").forEach(
        (button) =>
          (button.onclick = run(async () => {
            await api(endpoint("artwork") + query(), "POST", {
              URL: b.Candidates[button.dataset.art].URL,
            });
            refresh();
            toast("图片已保存");
          })),
      );
    });
    const xml = dialog.querySelector("[data-xml]");
    dialog.querySelector("[data-danmaku]").onclick = () => xml.click();
    xml.onchange = run(async () => {
      if (xml.files[0]) {
        await api(
          "/features/danmaku?ID=" + encodeURIComponent(id),
          "POST",
          xml.files[0],
          { raw: true },
        );
        toast("弹幕已保存");
      }
    });
  }
  async function importPage(host) {
    const users = await api.getUsers();
    host.innerHTML = `<form class="feature-form" id="feature-import-connect"><div class="feature-grid">${field("URL", "Emby / Jellyfin 地址", "", "url", 'required placeholder="http://192.168.1.10:8096"')}${field("Key", "访问密钥", "", "password", 'required autocomplete="off"')}</div><button>读取媒体库</button></form><div id="feature-import-preview"></div>`;
    const form = host.querySelector("form");
    form.onsubmit = run(async (e) => {
      e.preventDefault();
      const request = parseForm(form, {}),
        b = await api(endpoint("import"), "POST", request),
        preview = host.querySelector("#feature-import-preview");
      preview.innerHTML = `<form class="feature-form"><h3>媒体库路径映射</h3>${b.Libraries.map((lib, i) => `<div class="feature-import-row">${check("Selected", "导入 " + esc(lib.Name), false).replace('name="Selected"', `name="Selected" value="${i}"`)}${field("RemotePath" + i, "来源路径", lib.Locations?.[0] || "")}${field("LocalPath" + i, "本地挂载路径", b.LocalLibraries.find((x) => x.Name === lib.Name)?.Locations?.[0] || "", "text", 'placeholder="/media/电影"')}</div>`).join("")}<div class="feature-grid">${check("Metadata", "导入资料与媒体信息", true)}${check("Progress", "导入较新的播放进度与收藏", false)}${select(
        "RemoteUser",
        "来源用户",
        "",
        (b.Users || []).map((x) => [x.Id, x.Name]),
      )}${select(
        "LocalUser",
        "本地用户",
        user.Id,
        users.map((x) => [x.Id, x.Name]),
      )}</div><button>开始导入</button></form>`;
      preview.querySelector("form").onsubmit = run(async (event) => {
        event.preventDefault();
        const f = new FormData(event.target),
          mappings = f.getAll("Selected").map((index) => {
            const lib = b.Libraries[index];
            return {
              RemoteID: lib.Id || lib.ItemId,
              Name: lib.Name,
              Kind: lib.CollectionType,
              RemotePath: f.get("RemotePath" + index),
              LocalPath: f.get("LocalPath" + index),
            };
          });
        if (!mappings.length) throw Error("请选择媒体库");
        await api(endpoint("import"), "POST", {
          ...request,
          Apply: true,
          Mappings: mappings,
          RemoteUser: f.get("RemoteUser"),
          LocalUser: f.get("LocalUser"),
          Metadata: f.has("Metadata"),
          Progress: f.has("Progress"),
        });
        form.querySelector('[name="Key"]').value = "";
        toast("导入任务已启动");
        navigateAdminSection(14);
      });
    });
  }
  const discoveryFacet = (key, label, values = []) => `<div class="discovery-field"><span>${esc(label)}</span><details class="discovery-facet" data-facet="${key}"><summary aria-label="选择${esc(label)}"><span data-facet-value>全部</span><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg></summary><div class="discovery-facet-menu"><input type="search" data-facet-search placeholder="搜索${esc(label)}" aria-label="搜索${esc(label)}" autocomplete="off"><div class="discovery-facet-options">${values.map(value => `<label class="discovery-facet-option"><input type="checkbox" name="${key}" value="${esc(value)}"><span>${esc(value)}</span></label>`).join('')}<p class="discovery-facet-empty" ${values.length ? 'hidden' : ''}>${values.length ? '没有匹配选项' : '暂无选项'}</p></div><div class="discovery-facet-footer"><span data-facet-count>未选择</span><button type="button" class="text-button" data-facet-clear>清空</button></div></div></details></div>`;
  document.addEventListener('pointerdown', event => {
    document.querySelectorAll('.discovery-facet[open]').forEach(facet => {
      if (!facet.contains(event.target)) facet.open = false;
    });
  });
  async function discover() {
    view = "browse";
    nav();
    history.replaceState({}, "", "#discover");
    const serial = ++discoveryRequest;
    $("#app").innerHTML =
      `<section class="feature-discovery"><div class="feature-dialog-heading"><h1>发现</h1><button class="secondary" onclick="browseRoot()">返回影库</button></div><form class="feature-filter" aria-label="作品筛选"></form><div class="discovery-results-heading"><span data-discovery-status role="status">正在加载…</span></div><div class="grid feature-discovery-grid" aria-busy="true"></div><div class="feature-actions feature-pagination"></div></section>`;
    const [facets, views] = await Promise.all([
      api("/features/facets"),
      api.getViews(),
    ]);
    if (serial !== discoveryRequest || !$("#app .feature-filter")) return;
    const form = $("#app .feature-filter");
    form.innerHTML = `<div class="discovery-filter-primary"><div class="discovery-search-field">${field("Search", "搜索", "", "search", 'placeholder="搜索作品名称" autocomplete="off"')}</div>${select("Library", "媒体库", "", [["", "全部媒体库"], ...(views.Items || []).filter(x => facets.Libraries.includes(x.Id)).map(x => [x.Id, x.Name])])}${select("Type", "作品类型", "", [["", "全部"], ["Movie", "电影"], ["Series", "剧集"]])}</div><div class="discovery-filter-secondary">${select("Year", "年份", "", [["", "全部"], ...facets.Years.map(x => [x, x])])}${select("Sort", "排序", "", [["", "添加时间"], ["name", "名称"], ["year", "年份"], ["rating", "评分"], ["premiere", "首映日期"]])}${select("Order", "顺序", "Descending", [["Descending", "降序"], ["Ascending", "升序"]])}${discoveryFacet("Genre", "题材", facets.Genres)}${discoveryFacet("Country", "地区", facets.Countries)}${discoveryFacet("Tag", "标签", facets.Tags)}</div><div class="discovery-filter-footer"><div class="discovery-filter-chips" aria-label="当前筛选条件"></div><div class="discovery-filter-actions"><button type="button" class="secondary" data-discovery-reset>重置</button><button type="submit" data-discovery-apply>筛选作品</button></div></div>`;
    const chips = form.querySelector('.discovery-filter-chips');
    const updateFacets = () => {
      form.querySelectorAll('.discovery-facet').forEach(facet => {
        const selected = [...facet.querySelectorAll('input[type="checkbox"]:checked')].map(input => input.value);
        const summary = facet.querySelector('summary');
        const value = selected.length ? selected.length === 1 ? selected[0] : `已选 ${selected.length} 项` : '全部';
        facet.querySelector('[data-facet-value]').textContent = value;
        facet.querySelector('[data-facet-count]').textContent = selected.length ? `已选 ${selected.length} 项` : '未选择';
        summary.classList.toggle('has-selection', selected.length > 0);
        summary.title = selected.join('、');
        summary.setAttribute('aria-label', '选择' + facet.parentElement.firstElementChild.textContent + '，' + value);
      });
      chips.replaceChildren();
      for (const input of form.elements) {
        if (!['Search', 'Library', 'Type', 'Year', 'Genre', 'Country', 'Tag'].includes(input.name) || !input.value || input.type === 'checkbox' && !input.checked) continue;
        const value = input.tagName === 'SELECT' ? input.selectedOptions[0].textContent : input.value;
        const chip = UI.el('button', {type:'button',class:'discovery-filter-chip','aria-label':`移除筛选：${value}`}, [UI.el('span',{},value), UI.el('span',{'aria-hidden':'true'},'×')]);
        chip.onclick = () => { if(input.type === 'checkbox') input.checked=false; else input.value=''; updateFacets(); };
        chips.append(chip);
      }
    };
    form.querySelectorAll('.discovery-facet').forEach(facet => {
      const search = facet.querySelector('[data-facet-search]');
      const filterOptions = () => {
        const query = search.value.trim().toLocaleLowerCase();
        let visible = 0;
        facet.querySelectorAll('.discovery-facet-option').forEach(option => {
          option.hidden = !option.textContent.toLocaleLowerCase().includes(query);
          if(!option.hidden) visible++;
        });
        facet.querySelector('.discovery-facet-empty').hidden = visible > 0;
      };
      search.oninput = filterOptions;
      search.onkeydown = event => {if(event.key === 'Enter') event.preventDefault();};
      facet.addEventListener('toggle', () => {
        if(facet.open) form.querySelectorAll('.discovery-facet').forEach(other => {if(other !== facet) other.open=false;});
      });
      facet.addEventListener('focusout', event => {
        if(event.relatedTarget && !facet.contains(event.relatedTarget)) facet.open=false;
      });
      facet.addEventListener('keydown', event => {
        if(event.key === 'Escape') {event.preventDefault();event.stopPropagation();facet.open=false;facet.querySelector('summary').focus();}
      });
      facet.querySelector('[data-facet-clear]').onclick = () => {
        facet.querySelectorAll('input[type="checkbox"]').forEach(input => {input.checked=false;});
        updateFacets();
      };
    });
    form.addEventListener('change', updateFacets);
    form.querySelector('[name="Search"]').addEventListener('input', updateFacets);
    let start = 0, applied = new URLSearchParams(new FormData(form));
    const fetchPage = async () => {
      const request = ++discoveryRequest, params = new URLSearchParams(applied);
      params.set("Start", start);
      params.set("Limit", "48");
      const host = form.closest('.feature-discovery');
      const grid = host.querySelector('.feature-discovery-grid');
      const status = host.querySelector('[data-discovery-status]');
      const button = form.querySelector('[data-discovery-apply]');
      grid.setAttribute('aria-busy','true');status.textContent='正在加载…';button.disabled=true;
      try {
        const result = await api("/features/catalog?" + params);
        if (request !== discoveryRequest || !form.isConnected) return;
        grid.replaceChildren(...result.Items.map(x => UI.PosterCard(x, item => detail(item.Id))));
        if (!result.Items.length) grid.innerHTML = '<p class="empty">没有符合条件的作品</p>';
        bindPosterMenus();
        status.textContent = `共 ${Number(result.TotalRecordCount).toLocaleString()} 部作品`;
        const controls = host.querySelector('.feature-pagination');
        controls.innerHTML = `<button class="secondary" data-prev ${start === 0 ? "disabled" : ""}>上一页</button><span>${result.Items.length ? start + 1 : 0}–${start + result.Items.length} / ${result.TotalRecordCount}</span><button class="secondary" data-next ${start + 48 >= result.TotalRecordCount ? "disabled" : ""}>下一页</button>`;
        controls.querySelector('[data-prev]').onclick = run(async () => {start=Math.max(0,start-48);await fetchPage();});
        controls.querySelector('[data-next]').onclick = run(async () => {start+=48;await fetchPage();});
      } catch(error) {
        if(request === discoveryRequest && form.isConnected) status.textContent='读取失败：'+error.message;
        throw error;
      } finally {
        if(request === discoveryRequest && form.isConnected) {grid.setAttribute('aria-busy','false');button.disabled=false;}
      }
    };
    form.onsubmit = run(async event => {
      event.preventDefault();
      form.querySelectorAll('.discovery-facet').forEach(facet => {facet.open=false;});
      applied=new URLSearchParams(new FormData(form));start=0;await fetchPage();
    });
    form.querySelector('[data-discovery-reset]').onclick = run(async () => {
      form.reset();
      form.querySelectorAll('.discovery-facet').forEach(facet => {facet.open=false;facet.querySelector('[data-facet-search]').dispatchEvent(new Event('input'));});
      updateFacets();applied=new URLSearchParams(new FormData(form));start=0;await fetchPage();
    });
    updateFacets();
    await fetchPage();
  }
  async function collections(id = "") {
    view = "browse";
    nav();
    history.replaceState({}, "", "#collections" + (id ? "/" + id : ""));
    const host = $("#app");
    host.innerHTML =
      '<section class="feature-discovery"><div class="feature-dialog-heading"><h1>合集</h1><div class="feature-actions"><button class="secondary" onclick="Features.newCollection()">新建合集</button><button class="secondary" onclick="browseRoot()">返回影库</button></div></div><div class="grid feature-collections"></div></section>';
    const result = await api("/features/collections");
    if (!host.querySelector(".feature-collections")) return;
    if (id) {
      const collection = result.Items.find((x) => x.ID === id),
        b = await api(
          "/features/collection-items?ID=" + encodeURIComponent(id),
        );
      if (!host.querySelector(".feature-collections")) return;
      host.querySelector("h1").textContent = collection?.Name || "合集";
      const grid = host.querySelector(".feature-collections");
      for (const x of b.Items) {
        const card = UI.PosterCard(x, (item) => detail(item.Id));
        if (collection?.Editable) {
          const button = UI.el(
            "button",
            {
              class: "secondary feature-remove",
              onclick: run(async (e) => {
                e.stopPropagation();
                await api("/features/collection-items", "DELETE", {
                  ID: id,
                  Items: [x.Id],
                });
                card.remove();
              }),
            },
            "移出",
          );
          card.append(button);
        }
        grid.append(card);
      }
      if (!b.Items.length)
        grid.innerHTML =
          '<p class="empty">在作品菜单中选择「加入合集」添加内容。</p>';
    } else {
      host.querySelector(".feature-collections").innerHTML =
        result.Items.map(
          (x) =>
            `<article class="feature-collection"><button class="feature-collection-open" data-open="${x.ID}"><span>${esc(x.Name)}</span><small>${x.Count} 部作品 · ${x.Public ? "共享" : "仅自己"}</small></button>${x.Editable ? `<button class="secondary" data-edit="${x.ID}">编辑</button><button class="secondary" data-delete="${x.ID}">删除</button>` : ""}</article>`,
        ).join("") || '<p class="empty">创建一个合集，整理喜欢的作品。</p>';
      host
        .querySelectorAll("[data-open]")
        .forEach((b) => (b.onclick = run(() => collections(b.dataset.open))));
      host
        .querySelectorAll("[data-edit]")
        .forEach(
          (b) =>
            (b.onclick = () =>
              newCollection(result.Items.find((x) => x.ID === b.dataset.edit))),
        );
      host.querySelectorAll("[data-delete]").forEach(
        (b) =>
          (b.onclick = run(async () => {
            if (
              await confirmDialog("删除合集", "删除合集？作品会保留。", "删除")
            ) {
              await api("/features/collections", "DELETE", {
                ID: b.dataset.delete,
              });
              await collections();
            }
          })),
      );
    }
  }
  function newCollection(c) {
    fmDialog(
      c ? "编辑合集" : "新建合集",
      field("Name", "名称", c?.Name || "", "text", 'required maxlength="256"') +
        check("Public", "其他用户也可查看", c?.Public),
      async (f) => {
        await api("/features/collections", c ? "PUT" : "POST", {
          ID: c?.ID,
          Name: f.get("Name"),
          Public: f.has("Public"),
        });
        await collections();
      },
    );
  }
  async function addToCollection(id) {
    const b = await api("/features/collections"),
      choices = b.Items.filter((x) => x.Editable);
    if (!choices.length) {
      newCollection();
      toast("请先创建合集，然后再添加作品");
      return;
    }
    fmDialog(
      "加入合集",
      select(
        "ID",
        "选择合集",
        choices[0].ID,
        choices.map((x) => [x.ID, x.Name]),
      ),
      async (f) => {
        await api("/features/collection-items", "POST", {
          ID: f.get("ID"),
          Items: [id],
        });
        toast("已加入合集");
      },
      "添加",
    );
  }
  async function preferences() {
    const b = await api("/features/playback-settings"),
      c = b.Preference;
    fmDialog(
      "播放偏好",
      field(
        "AudioLanguage",
        "优先音轨语言",
        c.AudioLanguage,
        "text",
        'placeholder="zho / jpn / eng"',
      ) +
        field(
          "SubtitleLanguage",
          "优先字幕语言",
          c.SubtitleLanguage,
          "text",
          'placeholder="zho / eng"',
        ) +
        check("Subtitles", "默认显示字幕", c.Subtitles) +
        check("Danmaku", "默认显示弹幕", c.Danmaku),
      async (f) => {
        await api("/features/playback-settings", "PUT", {
          AudioLanguage: f.get("AudioLanguage"),
          SubtitleLanguage: f.get("SubtitleLanguage"),
          Subtitles: f.has("Subtitles"),
          Danmaku: f.has("Danmaku"),
        });
      },
    );
  }
  async function share(id) {
    const b = await api("/features/link?ID=" + encodeURIComponent(id)),
      url =
        (b.PublicURL || location.origin).replace(/\/$/, "") + "/" + b.Fragment;
    try {
      await navigator.clipboard.writeText(url);
      toast("作品链接已复制");
    } catch {
      fmDialog("作品链接", field("URL", "链接", url, "text", "readonly"), null);
    }
  }
  async function applyAppearance() {
    if (!token) return;
    const c = await api("/features/appearance");
    document.documentElement.style.setProperty(
      "--feature-title-size",
      c.Size + "px",
    );
    document.documentElement.style.setProperty("--feature-title-font", c.Font);
    document.documentElement.style.setProperty(
      "--feature-backdrop-blur",
      c.Blur + "px",
    );
    document.body.classList.toggle("feature-title-wrap", c.Wrap);
  }
  return {
    share,
    load,
    ensureSections,
    libraryPolicy,
    libraryActions,
    metadata,
    identify,
    artwork,
    discover,
    collections,
    newCollection,
    addToCollection,
    preferences,
    applyAppearance,
  };
})();
