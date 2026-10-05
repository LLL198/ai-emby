function mediaInfo(x) {
  const m = { ...x, ...(x.MediaSources?.[0] || {}) };
  const yes = (v) => (v === true ? "是" : v === false ? "否" : "未知");
  const value = (v) => (v === undefined || v === null || v === "" ? "未知" : v);
  const rows = (fields) =>
    `<dl>${fields.map(([k, v]) => `<div><dt>${esc(k)}</dt><dd>${esc(value(v))}</dd></div>`).join("")}</dl>`;
  return `<details class="media-info" aria-label="媒体信息"><summary>媒体信息</summary>${rows(
    [
      ["容器", m.Container],
      ["视频大小", m.Size ? (m.Size / 1073741824).toFixed(2) + " GiB" : null],
      ["总码率", m.Bitrate ? Math.round(m.Bitrate / 1000) + " kbps" : null],
      [
        "时长",
        m.RunTimeTicks ? Math.round(m.RunTimeTicks / 6e8) + " 分钟" : null,
      ],
    ],
  )}${(m.MediaStreams || [])
    .map(
      (v) =>
        `<article><h4>${v.Type === "Video" ? "视频" : v.Type === "Audio" ? "音频" : "字幕"}</h4>${rows(
          [
            ["标题", v.DisplayTitle],
            ["编码", v.Codec],
            ...(v.Type === "Video"
              ? [
                  [
                    "分辨率",
                    v.Width && v.Height ? v.Width + " × " + v.Height : null,
                  ],
                  ["帧率", v.AverageFrameRate ?? v.RealFrameRate],
                  ["动态范围", v.VideoRange],
                  ["配置", v.Profile],
                  ["等级", v.Level],
                  ["长宽比", v.AspectRatio],
                  ["交错", yes(v.IsInterlaced)],
                  ["位深", v.BitDepth],
                  ["像素格式", v.PixelFormat],
                ]
              : [
                  ["布局", v.ChannelLayout],
                  ["声道", v.Channels],
                  [
                    "比特率",
                    v.BitRate ? Math.round(v.BitRate / 1000) + " kbps" : null,
                  ],
                  ["采样率", v.SampleRate ? v.SampleRate + " Hz" : null],
                ]),
            ["外部", yes(v.IsExternal)],
            ["默认", yes(v.IsDefault)],
          ],
        )}</article>`,
    )
    .join(
      "",
    )}<small>未知项表示尚未提取或源站未提供媒体数据。</small></details>`;
}

let detailGeneration = 0;
async function detail(id, options = {}) {
  const generation = ++detailGeneration;
  await stop();
  if (generation !== detailGeneration) return;
  const d = $("#modal");
  // Only open the dialog once its complete content is ready.
  const switching = d.open;
  if (switching) d.setAttribute("aria-busy", "true");
  let item;
  try {
    item = await api.getItem(id);
  } catch (e) {
    if (generation === detailGeneration) {
      d.removeAttribute("aria-busy");
      if (!switching) await closeModal();
      else {
        d.replaceChildren(UI.el("div", {class:"detail-body"}, [
          UI.el("p", {}, "详情加载失败：" + e.message),
          UI.el("button", {onclick:run(() => detail(id, options))}, "重试"),
          UI.el("button", {class:"secondary", onclick:() => closeModal()}, "关闭")
        ]));
      }
    }
    throw e;
  }
  if (generation !== detailGeneration) return;
  d.className = "detail-dialog";
  const versions = item.MediaSources || [];
  let selected = id,
    starting = false;
  d.removeAttribute("aria-busy");
  d.setAttribute("aria-label", item.Name);
  d.innerHTML = `<div class="detail-body"><div class="detail-actions"><button id="play" class="media-primary">▶ ${item.UserData?.PlaybackPositionTicks > 0 ? "继续播放" : "播放"}</button><button id="restart" class="secondary">从头播放</button>${versions.length > 1 ? `<button id="versions" class="secondary" aria-expanded="false">版本 ${versions.length}</button><select id="version-list" aria-label="选择视频版本" hidden>${versions.map((v, n) => `<option value="${esc(v.Id)}">版本 ${n + 1} · ${esc(v.Name)}</option>`).join("")}</select>` : ""}</div><div id="player"></div><div class="detail-meta">${esc([item.ProductionYear, item.CommunityRating ? "★ " + item.CommunityRating : "", ...(item.Genres || [])].filter(Boolean).join(" · "))}</div><p class="detail-overview">${esc(item.Overview || "暂无简介")}</p><section aria-label="演员"><h3>演员</h3><div class="actors media-shelf" aria-label="演员列表"></div></section><section id="related-section"><h3>相关推荐</h3><div class="related media-shelf" aria-label="相关推荐"></div></section>${mediaInfo(item)}</div>`;
  const close = UI.IconButton("关闭详情", "close", () => closeModal());
  close.classList.add("detail-close");
  d.prepend(close, UI.DetailHero(item));
  if (!d.open) d.showModal();
  d.scrollTop = 0;
  close.focus({preventScroll:true});
  const isSeries = item.Type === "Series";
  if (isSeries) {
    const button = d.querySelector("#play");
    button.textContent = "选择剧集";
    button.setAttribute("aria-expanded", "false");
    button.setAttribute("aria-controls", "episode-picker");
    d.querySelector("#restart").hidden = true;
    const picker = UI.el("section", {id:"episode-picker", class:"episode-picker", "aria-label":"选择季和集", hidden:""});
    d.querySelector(".detail-actions").after(picker);
    let loaded = false, request = 0;
    const loadEpisodes = async seasonId => {
      const sequence = ++request;
      const list = picker.querySelector(".episode-list");
      list.textContent = "正在加载剧集…";
      try {
        const result = await api.getEpisodes(id, {SeasonId:seasonId, Fields:"IndexNumber,ParentIndexNumber", SortBy:"IndexNumber", SortOrder:"Ascending"});
        if (generation !== detailGeneration || sequence !== request) return;
        const episodes = result.Items || [];
        list.replaceChildren(...episodes.map(episode => UI.el("button", {class:"secondary episode-choice", onclick:run(() => detail(episode.Id, {play:true}))}, `${episode.IndexNumber != null ? '第 ' + episode.IndexNumber + ' 集 · ' : ''}${episode.Name}`)));
        if (!episodes.length) list.textContent = "本季暂无剧集";
      } catch (e) {
        if (generation !== detailGeneration || sequence !== request) return;
        list.replaceChildren(UI.el("p", {}, "剧集加载失败：" + e.message), UI.el("button", {class:"secondary", onclick:run(() => loadEpisodes(seasonId))}, "重试"));
      }
    };
    button.onclick = run(async () => {
      picker.hidden = !picker.hidden;
      button.setAttribute("aria-expanded", String(!picker.hidden));
      if (picker.hidden || loaded) return;
      loaded = true;
      picker.textContent = "正在加载季…";
      try {
        const result = await api.getSeasons(id, {SortBy:"IndexNumber", SortOrder:"Ascending"});
        if (generation !== detailGeneration) return;
        const seasons = result.Items || [];
        const select = UI.el("select", {"aria-label":"选择季", onchange:run(e => loadEpisodes(e.target.value))}, seasons.map(season => UI.el("option", {value:season.Id}, season.Name)));
        picker.replaceChildren(UI.el("label", {}, ["选择季 ", select]), UI.el("div", {class:"episode-list", "aria-live":"polite"}));
        if (seasons.length) await loadEpisodes(seasons[0].Id);
        else picker.textContent = "暂无可选季";
      } catch (e) { if (generation === detailGeneration) { loaded = false; picker.textContent = "季加载失败，请收起后重试：" + e.message; } }
    });
  }
  const overview = d.querySelector(".detail-overview");
  const fullOverview = item.Overview || "暂无简介";
  if (fullOverview.length > 160) {
    overview.textContent = fullOverview.slice(0, 160) + "…";
    const toggle = UI.el("button", {class:"text-button", "aria-expanded":"false", onclick:() => {
      const expanded = toggle.getAttribute("aria-expanded") !== "true";
      toggle.setAttribute("aria-expanded", String(expanded));
      toggle.textContent = expanded ? "收起简介" : "展开简介";
      overview.textContent = expanded ? fullOverview : fullOverview.slice(0, 160) + "…";
    }}, "展开简介");
    overview.after(toggle);
  }

  const actors = d.querySelector(".actors");
  for (const person of (item.People || []).filter(
    (p) =>
      p.Type === "Actor" && (!item.HideMissingActorImages || p.PrimaryImageTag),
  )) {
    const picture = person.PrimaryImageTag
      ? UI.el("img", {
          src: api.image(person.Id, "Primary", 200),
          loading: "lazy",
          decoding: "async",
          alt: person.Name,
        })
      : UI.el("div", { class: "actor-placeholder" });
    actors.append(
      UI.el("figure", { class: "actor" }, [
        picture,
        UI.el("figcaption", {}, [
          person.Name,
          UI.el("small", {}, person.Role || ""),
        ]),
      ]),
    );
  }
  if (!actors.children.length) actors.parentElement.hidden = true;
  else { const host = actors.parentElement; host.append(UI.bindMediaShelf(actors)); }
  async function startPlayback(fromBeginning = false) {
    if (starting) return;
    starting = true;
    const button = d.querySelector("#play");
    button.disabled = true;
    const restart = d.querySelector("#restart");
    restart.disabled = true;
    try {
      const playingVideo = $("#player video");
      if (current === selected && playingVideo && Number.isFinite(playingVideo.currentTime)) {
        item.UserData = {...item.UserData, PlaybackPositionTicks: Math.floor(featurePlaybackPosition(playingVideo) * 1e7)};
      }
      await stop();
      if (generation !== detailGeneration || !d.open) return;
      const playback = await api.getPlaybackInfo(selected);
      if (generation !== detailGeneration || !d.open) {
        await api("/Sessions/Playing/Stopped", "POST", {
          ItemId: selected,
          PositionTicks: 0,
        });
        return;
      }
      const source =
        playback.MediaSources?.find((v) => v.Id === selected) ||
        playback.MediaSources?.[0];
      if (!source?.DirectStreamUrl) throw Error("未返回可播放的视频源");
      current = selected;
      const playbackItem = selected;
      const video = UI.el("video", {
        controls: "",
        controlsList: "nodownload",
        autoplay: "",
        playsinline: "",
      });
      d.querySelector("#player").replaceChildren(video);
      const switchVersion = async value => {
        if (value === selected) return;
        item.UserData = {...item.UserData, PlaybackPositionTicks:Math.floor(featurePlaybackPosition(video) * 1e7)};
        await stop();
        selected = value;
        const list = d.querySelector("#version-list");
        if (list) list.value = value;
        await startPlayback();
      };
      const webPlayer = WebPlayer.mount(d, video, item, {
        source, versions, selected,
        onEpisode: episode => detail(episode.Id, {play:true}),
        onVersion:switchVersion,
        onRetry:() => startPlayback(false),
        onReplay:() => startPlayback(true),
        onExit:async () => {
          await WebPlayer.exitFullscreen(d);
          await stop();
          d.classList.remove("watch-dialog");
          d.querySelector("#player").replaceChildren();
          d.querySelector("#play").focus({preventScroll:true});
        },
      });
      const progress = () => {
        if (current !== playbackItem || video.dataset.stopping === 'true') return Promise.resolve();
        item.UserData = {...item.UserData, PlaybackPositionTicks:Math.floor(featurePlaybackPosition(video) * 1e7)};
        return api("/Sessions/Playing/Progress", "POST", {
          ItemId: playbackItem,
          PositionTicks: Math.floor(featurePlaybackPosition(video) * 1e7),
          IsPaused:video.paused,
          PlaybackRate:video.playbackRate,
          RunTimeTicks: source.RunTimeTicks || item.RunTimeTicks || (Number.isFinite(video.duration)
            ? Math.floor((video.duration + Number(video.dataset.offset || 0)) * 1e7)
            : 0),
        });
      };
      video.addEventListener(
        "playing",
        () =>
          progress().catch((e) => {
            video.pause();
            toast(e.message);
          }),
        { once: true },
      );
      // The source is the authenticated redirect URL. The browser follows its 302 to the CDN.
      const resume = fromBeginning ? 0 : (item.UserData?.PlaybackPositionTicks || 0);
      video.dataset.resume=String(resume/1e7);
      if (resume > 0)
        video.addEventListener(
          "loadedmetadata",
          () => {
            video.currentTime = Math.max(0,resume/1e7-Number(video.dataset.offset||0));
          },
          { once: true },
        );
      // Register resume metadata handlers BEFORE assigning the media source.
      video.src = source.DirectStreamUrl;
      featurePlayer(playbackItem,video,source).catch(e=>toast(e.message,{type:'error'}));
      heartbeat = setInterval(
        () =>
          progress().catch((e) => {
            video.pause();
            toast(e.message);
          }),
        30000,
      );
      video.onended = async () => {
        if (video.dataset.stopping === 'true' || generation !== detailGeneration) return;
        const finished = playbackItem;
        let nextEpisode;
        try { nextEpisode = await webPlayer.nextEpisode(); } catch (e) { toast(e.message, {type:'error'}); }
        if (video.dataset.stopping === 'true' || generation !== detailGeneration) return;
        await stop({keepPlayer:!nextEpisode});
        if (!nextEpisode) webPlayer.finish();
        try { await api(`/Users/${encodeURIComponent(user.Id)}/PlayedItems/${encodeURIComponent(finished)}`, 'POST'); }
        catch (e) { toast(e.message, {type:'error'}); }
        if (nextEpisode && generation === detailGeneration && d.open) await detail(nextEpisode.Id, {play:true});
      };
    } catch (error) {
      if (generation === detailGeneration && d.open) {
        await WebPlayer.exitFullscreen(d);
        await stop();
        if (generation === detailGeneration && d.open) {
          d.classList.remove("watch-dialog");
          d.querySelector("#player").replaceChildren(UI.el("p", {role:"alert"}, "播放加载失败：" + error.message));
          button.focus({preventScroll:true});
        }
      }
      throw error;
    } finally {
      starting = false;
      if (button.isConnected) button.disabled = false;
      if (restart.isConnected) restart.disabled = false;
    }
  }
  if (!isSeries) d.querySelector("#play").onclick = run(() => startPlayback(false));
  d.querySelector("#restart").onclick = run(() => startPlayback(true));
  d.querySelector("#versions")?.addEventListener("click", () => {
    const list = d.querySelector("#version-list");
    list.hidden = !list.hidden;
    d.querySelector("#versions").setAttribute(
      "aria-expanded",
      String(!list.hidden),
    );
  });
  d.querySelector("#version-list")?.addEventListener(
    "change",
    run(async (e) => {
      const wasPlaying = !!current;
      await stop();
      selected = e.target.value;
      const source = versions.find((v) => v.Id === selected);
      d.querySelector(".media-info").outerHTML = mediaInfo({
        MediaSources: [source],
      });
      if (wasPlaying) await startPlayback();
      toast("已切换版本：" + source.Name);
    }),
  );
  if (options.play && !isSeries) await startPlayback(false);
  if (generation !== detailGeneration || !d.open) return;
  try {
    const related = await api.query(
      `/emby/Items/${encodeURIComponent(id)}/Similar`,
      { Limit: 6 },
    );
    if (generation !== detailGeneration || !d.open) return;
    const shelf = d.querySelector(".related");
    shelf.replaceChildren(
      ...related.Items.map((x) =>
        UI.PosterCard(x, item => detail(item.Id)),
      ),
    );
    const host = shelf.parentElement;
    host.append(UI.bindMediaShelf(shelf));
    if (!related.Items.length)
      d.querySelector("#related-section").hidden = true;
  } catch (e) {
    if (generation === detailGeneration && d.open)
      d.querySelector(".related").textContent = "相关推荐暂不可用";
  }
}
async function stop(options = {}) {
  clearInterval(heartbeat);
  const video = $("#player video");
  const position = Math.floor(featurePlaybackPosition(video) * 1e7);
  if (video) {
    video.dataset.stopping='true';
    if (!options.keepPlayer) video.webPlayerDispose?.();
    if (document.pictureInPictureElement === video) await document.exitPictureInPicture().catch(() => {});
    video.pause();
    if (!options.keepPlayer) {
      video.removeAttribute("src");
      video.load();
    }
  }
  if (current) {
    const id = current;
    current = null;
    try {
      await api("/Sessions/Playing/Stopped", "POST", {
        ItemId: id,
        PositionTicks: position,
      });
    } catch {}
  }
}
async function closeModal() {
  ++detailGeneration;
  const d = $("#modal");
  await WebPlayer.exitFullscreen(d);
  d.close();
  await stop();
  if (!d.open) {
    d.replaceChildren();
    d.className = "";
  }
}
document.querySelector("#modal").addEventListener("cancel", () => {
  ++detailGeneration;
  stop();
  document.querySelector("#modal").className = "";
});
