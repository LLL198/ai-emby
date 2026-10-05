const WebPlayer = (() => {
  const preferenceKey = "ai-emby-web-player";
  const rates = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2, 2.5, 3];
  function preferences() {
    let saved = {};
    try { saved = JSON.parse(localStorage.getItem(preferenceKey)) || {}; } catch {}
    return {
      rate: rates.includes(saved.rate) ? saved.rate : 1,
      volume: Number.isFinite(saved.volume) ? Math.max(0, Math.min(1, saved.volume)) : 1,
      muted: saved.muted === true, autoNext: saved.autoNext !== false,
      fit: saved.fit === "cover" ? "cover" : "contain",
    };
  }
  function time(seconds) {
    seconds = Math.max(0, Math.floor(Number(seconds) || 0));
    const hours = Math.floor(seconds / 3600), minutes = Math.floor(seconds % 3600 / 60);
    return (hours ? hours + ":" + String(minutes).padStart(2, "0") : String(minutes)) + ":" + String(seconds % 60).padStart(2, "0");
  }
  const icons = {
    play: '<path d="m9 5 11 7-11 7Z"/>', pause: '<path d="M8 5v14M16 5v14"/>',
    back: '<path d="m15 5-7 7 7 7"/>', previous: '<path d="M5 5v14m14-14L8 12l11 7Z"/>', next: '<path d="M19 5v14M5 5l11 7-11 7Z"/>',
    rewind: '<path d="m11 5-8 7 8 7V5Zm10 0-8 7 8 7V5Z"/>', forward: '<path d="m3 5 8 7-8 7V5Zm10 0 8 7-8 7V5Z"/>',
    volume: '<path d="M4 9h4l5-4v14l-5-4H4ZM17 8a6 6 0 0 1 0 8m3-11a10 10 0 0 1 0 14"/>', mute: '<path d="M4 9h4l5-4v14l-5-4H4Zm13 0 5 6m0-6-5 6"/>',
    fullscreen: '<path d="M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5"/>', pip: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M11 11h8v7h-8Z"/>',
    settings: '<path d="M12 3v3m0 12v3M3 12h3m12 0h3M5.6 5.6l2.1 2.1m8.6 8.6 2.1 2.1M18.4 5.6l-2.1 2.1m-8.6 8.6-2.1 2.1"/><circle cx="12" cy="12" r="4"/>',
    episodes: '<path d="M8 6h13M8 12h13M8 18h13M3 6h1M3 12h1M3 18h1"/>', close: '<path d="m6 6 12 12M18 6 6 18"/>',
  };
  function button(label, icon, action, text = "") {
    const node = UI.el("button", {class:"watch-button", type:"button", "aria-label":label, title:label, onclick:run(action)});
    node.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[icon]}</svg>`;
    if (text) node.append(UI.el("span", {}, text));
    return node;
  }
  async function exitFullscreen(dialog) {
    if (dialog.classList.contains("watch-dialog") && document.fullscreenElement === document.documentElement)
      await document.exitFullscreen().catch(() => {});
  }
  function mount(dialog, video, item, options) {
    const state = preferences(), events = new AbortController();
    let disposed = false, finished = false, idleTimer, dragging = false, activePanel = null, sequence = 0;
    let seasons = [], episodes = [], episodeIndex = -1, episodesLoading = false;
    let playbackEpisodes = [], playbackSeasonIndex = -1;
    const on = (node, event, fn) => node.addEventListener(event, fn, {signal:events.signal});
    const root = UI.el("section", {class:"watch-player", "aria-label":"视频播放器", tabindex:"-1"});
    const stage = UI.el("div", {class:"watch-stage"}, video);
    const heading = UI.el("div", {class:"watch-heading"}, [
      UI.el("small", {}, item.SeriesName || (item.Type === "Movie" ? "正在播放 · 电影" : "正在播放")),
      UI.el("h2", {}, `${item.Type === "Episode" && item.IndexNumber != null ? "第 " + item.IndexNumber + " 集 · " : ""}${item.Name}`),
    ]);
    const back = button("返回详情", "back", options.onExit);
    const header = UI.el("header", {class:"watch-header"}, [back, heading]);
    const feedback = UI.el("div", {class:"watch-feedback", role:"status", "aria-live":"polite"}, "正在加载视频…");
    const centerPlay = button("开始播放", "play", () => toggle());
    centerPlay.classList.add("watch-center-play"); centerPlay.hidden = true;
    const failure = UI.el("div", {class:"watch-failure", hidden:"", role:"alert"}, [
      UI.el("strong", {}, "视频暂时无法播放"),
      UI.el("p", {}, "可重试或在播放设置中选择转码。浏览器无法解码的格式，也可以使用 Emby 客户端播放。"),
      UI.el("button", {type:"button", onclick:run(options.onRetry)}, "重试播放"),
    ]);
    video.webPlayerSignal = events.signal;
    video.webPlayerError = message => {
      if (disposed) return;
      failure.querySelector("p").textContent = message;
      feedback.hidden = centerPlay.hidden = true; failure.hidden = false; reveal();
    };
    const progress = UI.el("input", {class:"watch-seek", type:"range", min:0, max:0, step:0.1, value:0, "aria-label":"播放进度", disabled:""});
    const clock = UI.el("span", {class:"watch-time"}, "0:00 / 0:00");
    const play = button("播放", "play", () => toggle());
    const previous = button("上一集", "previous", () => navigate(-1));
    const next = button("下一集", "next", () => navigate(1));
    previous.disabled = next.disabled = true;
    previous.hidden = next.hidden = item.Type !== "Episode";
    const mute = button("静音", "volume", () => { video.muted = !video.muted; });
    const volume = UI.el("input", {type:"range", min:0, max:1, step:0.05, value:state.volume, "aria-label":"音量", class:"watch-volume"});
    const speed = UI.el("select", {"aria-label":"播放倍速", class:"watch-speed"}, rates.map(rate => UI.el("option", {value:rate}, rate + "×")));
    const fullscreen = button("全屏", "fullscreen", async () => {
      if (document.fullscreenElement === document.documentElement) await document.exitFullscreen();
      // Fullscreen API excludes dialog elements; keep the page fullscreen across episode changes.
      else if (document.documentElement.requestFullscreen) await document.documentElement.requestFullscreen();
      else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen();
      else toast("当前浏览器不支持全屏", {type:"info"});
    });
    const pip = button("画中画", "pip", async () => {
      if (document.pictureInPictureElement === video) await document.exitPictureInPicture();
      else {
        try { await video.requestPictureInPicture(); }
        catch { toast("当前浏览器或设备无法开启画中画", {type:"info"}); }
      }
    });
    pip.hidden = !document.pictureInPictureEnabled || !video.requestPictureInPicture;
    const settingsButton = button("播放设置", "settings", () => panel("settings"));
    const episodesButton = button("选集", "episodes", () => panel("episodes"), "选集");
    episodesButton.hidden = item.Type !== "Episode" || !item.SeriesId;
    const row = UI.el("div", {class:"watch-control-row"}, [
      play, previous, button("后退 10 秒", "rewind", () => seek(featurePlaybackPosition(video) - 10)),
      button("快进 10 秒", "forward", () => seek(featurePlaybackPosition(video) + 10)), next,
      UI.el("div", {class:"watch-volume-control"}, [mute, volume]), clock,
      UI.el("div", {class:"watch-control-spacer"}), speed, episodesButton, settingsButton, pip, fullscreen,
    ]);
    const footer = UI.el("footer", {class:"watch-footer"}, [progress, row]);
    const tools = UI.el("div", {"data-player-tools":"", class:"watch-feature-tools"});
    const autoNext = UI.el("input", {type:"checkbox", "aria-label":"自动播放下一集"});
    autoNext.checked = state.autoNext;
    const fit = UI.el("select", {"aria-label":"画面比例"}, [UI.el("option", {value:"contain"}, "适应窗口"), UI.el("option", {value:"cover"}, "填满窗口（裁剪边缘）")]);
    fit.value = state.fit;
    const settingsPanel = UI.el("section", {class:"watch-panel", "aria-label":"播放设置", hidden:""}, [
      UI.el("div", {class:"watch-panel-heading"}, [UI.el("h3", {}, "播放设置"), button("关闭播放设置", "close", () => panel(null))]),
      UI.el("label", {class:"watch-setting"}, ["画面比例", fit]),
      UI.el("label", {class:"watch-setting", hidden: item.Type === "Episode" ? undefined : ""}, ["自动播放下一集", autoNext]),
      tools,
      UI.el("p", {class:"watch-keyboard-help"}, "快捷键：空格播放 / 暂停 · ← → 跳转 10 秒 · ↑ ↓ 音量 · M 静音 · F 全屏"),
    ]);
    const versions = options.versions || [];
    if (versions.length > 1) {
      const select = UI.el("select", {"aria-label":"播放版本"}, versions.map((v, i) => UI.el("option", {value:v.Id}, `版本 ${i + 1} · ${v.Name}`)));
      select.value = options.selected;
      on(select, "change", run(() => options.onVersion(select.value)));
      settingsPanel.insertBefore(UI.el("label", {class:"watch-setting"}, ["播放版本", select]), tools);
    }
    const seasonSelect = UI.el("select", {"aria-label":"播放季", disabled:""});
    const episodeList = UI.el("div", {class:"watch-episodes", "aria-live":"polite"}, "正在加载剧集…");
    const episodePanel = UI.el("section", {class:"watch-panel", "aria-label":"选择剧集", hidden:""}, [
      UI.el("div", {class:"watch-panel-heading"}, [UI.el("h3", {}, "选择剧集"), button("关闭选集", "close", () => panel(null))]), seasonSelect, episodeList,
    ]);
    root.append(stage, header, feedback, centerPlay, failure, footer, settingsPanel, episodePanel);
    dialog.querySelector("#player").replaceChildren(root);
    dialog.classList.add("watch-dialog");
    document.documentElement.classList.add("watch-open");
    video.controls = false; video.volume = state.volume; video.muted = state.muted;
    video.defaultPlaybackRate = video.playbackRate = state.rate;
    video.style.objectFit = state.fit; speed.value = String(state.rate);
    const save = () => { try { localStorage.setItem(preferenceKey, JSON.stringify(state)); } catch {} };
    const total = () => Math.max(Number(options.source?.RunTimeTicks || item.RunTimeTicks || 0) / 1e7, Number.isFinite(video.duration) ? video.duration + Number(video.dataset.offset || 0) : 0);
    function reveal() {
      root.classList.remove("watch-idle"); clearTimeout(idleTimer);
      if (!video.paused && !activePanel) idleTimer = setTimeout(() => {
        if (!disposed && !video.paused && !root.querySelector(":focus-visible")) root.classList.add("watch-idle");
      }, 3500);
    }
    function panel(name) {
      activePanel = activePanel === name ? null : name;
      settingsPanel.hidden = activePanel !== "settings"; episodePanel.hidden = activePanel !== "episodes";
      settingsButton.setAttribute("aria-expanded", String(!settingsPanel.hidden));
      episodesButton.setAttribute("aria-expanded", String(!episodePanel.hidden));
      reveal();
    }
    async function toggle() {
      if (finished) { await options.onReplay(); return; }
      if (video.paused) {
        try { await video.play(); } catch (e) {
          if (e.name !== "AbortError") { centerPlay.hidden = false; toast("点击播放按钮开始观看", {type:"info"}); }
        }
      } else video.pause();
      reveal();
    }
    function seek(position) {
      if (disposed || finished) return;
      const offset = Number(video.dataset.offset || 0), duration = total();
      if (position < offset) { toast("当前转码从中途开始，请返回详情后从头播放", {type:"info"}); return; }
      if (video.readyState < 1 || !duration) return;
      video.currentTime = Math.max(0, Math.min(duration, position) - offset);
      reveal();
    }
    function update() {
      const position = featurePlaybackPosition(video), duration = total();
      progress.max = String(duration); progress.min = String(Number(video.dataset.offset || 0));
      progress.disabled = finished || !duration || video.readyState < 1;
      if (!dragging) { progress.value = String(position); clock.textContent = `${time(position)} / ${time(duration)}`; }
      progress.setAttribute("aria-valuetext", `${time(Number(progress.value))} / ${time(duration)}`);
      const paused = video.paused;
      play.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[paused ? "play" : "pause"]}</svg>`;
      const label = finished ? "重新播放" : paused ? "播放" : "暂停";
      play.setAttribute("aria-label", label); play.title = label;
      speed.value = String(video.playbackRate);
      pip.disabled = video.readyState < 1;
    }
    on(progress, "input", () => { dragging = true; clock.textContent = `${time(Number(progress.value))} / ${time(total())}`; reveal(); });
    on(progress, "change", () => { dragging = false; seek(Number(progress.value)); update(); });
    on(progress, "pointercancel", () => { dragging = false; update(); });
    on(volume, "input", () => { video.volume = Number(volume.value); video.muted = video.volume === 0; reveal(); });
    on(video, "volumechange", () => {
      state.volume = video.volume; state.muted = video.muted; volume.value = String(video.volume);
      mute.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[video.muted || !video.volume ? "mute" : "volume"]}</svg>`;
      mute.setAttribute("aria-label", video.muted ? "取消静音" : "静音"); mute.title = video.muted ? "取消静音" : "静音"; save();
    });
    on(speed, "change", () => { video.defaultPlaybackRate = video.playbackRate = Number(speed.value); reveal(); });
    on(video, "ratechange", () => { state.rate = video.playbackRate; save(); update(); });
    on(fit, "change", () => { state.fit = fit.value; video.style.objectFit = state.fit; save(); });
    on(autoNext, "change", () => { state.autoNext = autoNext.checked; save(); });
    for (const event of ["timeupdate", "durationchange", "loadedmetadata", "seeked"]) on(video, event, update);
    on(video, "loadedmetadata", () => { video.playbackRate = state.rate; });
    on(video, "waiting", () => { if (!video.error && video.dataset.taskStopped !== "true") { feedback.textContent = "正在缓冲…"; feedback.hidden = false; } });
    on(video, "seeking", () => { feedback.textContent = "正在跳转…"; feedback.hidden = false; });
    on(video, "playing", () => { feedback.hidden = failure.hidden = centerPlay.hidden = true; update(); reveal(); });
    on(video, "canplay", () => { feedback.hidden = true; centerPlay.hidden = !video.paused; update(); });
    on(video, "pause", () => { centerPlay.hidden = !!video.error || video.ended; update(); reveal(); });
    on(video, "error", () => { feedback.hidden = centerPlay.hidden = true; failure.hidden = false; reveal(); });
    on(root, "pointermove", reveal); on(root, "pointerdown", reveal); on(root, "focusin", reveal);
    on(stage, "click", run(() => { if (activePanel) panel(null); else return toggle(); }));
    on(stage, "dblclick", () => fullscreen.click());
    const updateFullscreen = () => {
      const label = document.fullscreenElement === document.documentElement ? "退出全屏" : "全屏";
      fullscreen.setAttribute("aria-label", label); fullscreen.title = label; reveal();
    };
    on(document, "fullscreenchange", updateFullscreen);
    const updatePictureInPicture = () => {
      const label = document.pictureInPictureElement === video ? "退出画中画" : "画中画";
      pip.setAttribute("aria-label", label); pip.title = label;
    };
    on(video, "enterpictureinpicture", updatePictureInPicture);
    on(video, "leavepictureinpicture", updatePictureInPicture);
    on(dialog, "keydown", event => {
      if (event.altKey || event.ctrlKey || event.metaKey || /^(INPUT|SELECT|TEXTAREA)$/.test(event.target.tagName) || event.target.isContentEditable) return;
      if (event.key === "Escape" && activePanel) { event.preventDefault(); panel(null); return; }
      // Buttons keep native Enter/Space activation instead of toggling twice.
      if (event.target.closest("button") && [" ", "Enter"].includes(event.key)) return;
      const keys = {" ":() => toggle(), ArrowLeft:() => seek(featurePlaybackPosition(video)-10), ArrowRight:() => seek(featurePlaybackPosition(video)+10), ArrowUp:() => { video.volume = Math.min(1, video.volume+0.05); video.muted = false; }, ArrowDown:() => { video.volume = Math.max(0, video.volume-0.05); }, m:() => mute.click(), f:() => fullscreen.click()};
      const action = keys[event.key] || keys[event.key.toLowerCase()];
      if (action) { event.preventDefault(); run(action)(); reveal(); }
    });
    function updateNavigation() {
      episodeIndex = playbackEpisodes.findIndex(e => e.Id === item.Id);
      previous.disabled = episodesLoading || episodeIndex < 0 || (episodeIndex === 0 && playbackSeasonIndex <= 0);
      next.disabled = episodesLoading || episodeIndex < 0 || (episodeIndex === playbackEpisodes.length-1 && playbackSeasonIndex === seasons.length-1);
    }
    async function loadEpisodes(index) {
      const request = ++sequence; episodesLoading = true; updateNavigation();
      episodeList.textContent = "正在加载剧集…";
      const result = await api.getEpisodes(item.SeriesId, {SeasonId:seasons[index].Id, SortBy:"IndexNumber", SortOrder:"Ascending"});
      if (disposed || request !== sequence) return null;
      episodes = result.Items || []; episodesLoading = false; seasonSelect.value = String(index);
      if (episodes.some(episode => episode.Id === item.Id)) { playbackEpisodes = episodes; playbackSeasonIndex = index; }
      updateNavigation();
      episodeList.replaceChildren(...episodes.map(episode => {
        const node = UI.el("button", {type:"button", class:"watch-episode", onclick:run(() => episode.Id === item.Id ? panel(null) : options.onEpisode(episode))}, [UI.el("span", {}, episode.IndexNumber != null ? String(episode.IndexNumber).padStart(2, "0") : "·"), UI.el("span", {}, episode.Name)]);
        if (episode.Id === item.Id) { node.classList.add("is-current"); node.setAttribute("aria-current", "true"); }
        return node;
      }));
      if (!episodes.length) episodeList.textContent = "本季暂无剧集";
      return episodes;
    }
    async function adjacent(direction) {
      if (disposed || episodesLoading || episodeIndex < 0) return null;
      if (playbackEpisodes[episodeIndex + direction]) return playbackEpisodes[episodeIndex + direction];
      const index = playbackSeasonIndex + direction;
      if (index < 0 || index >= seasons.length) return null;
      const result = await api.getEpisodes(item.SeriesId, {SeasonId:seasons[index].Id, SortBy:"IndexNumber", SortOrder:"Ascending"});
      if (disposed) return null;
      return direction > 0 ? result.Items?.[0] : result.Items?.at(-1);
    }
    async function navigate(direction) { const episode = await adjacent(direction); if (episode && !disposed) await options.onEpisode(episode); }
    const showEpisodeError = error => {
      if (disposed) return;
      episodesLoading = false; updateNavigation();
      episodeList.replaceChildren(UI.el("p", {}, "剧集加载失败：" + error.message), UI.el("button", {type:"button", onclick:run(loadSeries)}, "重试"));
    };
    async function loadSeries() {
      try {
        const result = await api.getSeasons(item.SeriesId, {SortOrder:"Ascending"});
        if (disposed) return;
        seasons = result.Items || [];
        seasonSelect.replaceChildren(...seasons.map((season, i) => UI.el("option", {value:i}, season.Name)));
        seasonSelect.disabled = !seasons.length;
        if (!seasons.length) { episodesLoading = false; episodeList.textContent = "暂无可选剧集"; return; }
        const index = seasons.findIndex(season => season.Id === item.SeasonId || season.IndexNumber === item.ParentIndexNumber);
        await loadEpisodes(Math.max(0, index));
      } catch (error) { showEpisodeError(error); }
    }
    on(seasonSelect, "change", () => loadEpisodes(Number(seasonSelect.value)).catch(showEpisodeError));
    const episodeReady = item.Type === "Episode" && item.SeriesId ? loadSeries() : Promise.resolve();
    function destroy() {
      disposed = true; ++sequence; clearTimeout(idleTimer); events.abort();
      document.documentElement.classList.remove("watch-open");
      video.webPlayerDispose = null;
      video.webPlayerError = null;
    }
    function finish() {
      if (disposed) return;
      finished = true; feedback.textContent = "播放完毕"; feedback.hidden = false;
      root.classList.add("watch-complete");
      centerPlay.hidden = false; centerPlay.setAttribute("aria-label", "重新播放"); centerPlay.title = "重新播放";
      update(); reveal();
    }
    video.webPlayerDispose = destroy;
    root.focus({preventScroll:true}); update(); updateFullscreen(); reveal();
    return {nextEpisode:async () => { await episodeReady; return state.autoNext ? adjacent(1) : null; }, finish, destroy};
  }
  return {mount, exitFullscreen};
})();
