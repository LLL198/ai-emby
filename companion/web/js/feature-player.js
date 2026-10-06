function featurePlaybackPosition(video) {
  if (video && video.readyState === 0 && !Number(video.dataset.offset || 0))
    return Math.max(0, Number(video.dataset.resume || 0));
  return video
    ? Math.max(
        0,
        (Number.isFinite(video.currentTime) ? video.currentTime : 0) +
          Number(video.dataset.offset || 0),
      )
    : 0;
}
const featureLanguageCode = (value) =>
  ({ zh: "zho", chi: "zho", cmn: "zho", en: "eng", ja: "jpn", jp: "jpn" })[
    (value || "").toLowerCase()
  ] || (value || "").toLowerCase();
async function featurePlayer(id, video, source) {
  const settings = await api("/features/playback-settings");
  if (!video.isConnected || video.dataset.stopping === "true") return;
  const c = settings.Preference,
    streams = source.MediaStreams || [],
    audio = streams.filter((x) => x.Type === "Audio"),
    subs = streams.filter((x) => x.Type === "Subtitle" && !x.IsExternal);
  const choose = (list, language) =>
    list.find(
      (x) => featureLanguageCode(x.Language) === featureLanguageCode(language),
    ) ||
    list.find((x) => x.IsDefault) ||
    list[0];
  const preferred = choose(audio, c.AudioLanguage),
    subtitle = choose(subs, c.SubtitleLanguage);
  let transcodeStarted = false;
  const controls = UI.el("div", { class: "feature-play-controls" }),
    audioSelect = UI.el(
      "select",
      { "aria-label": "音轨" },
      audio.map((x) =>
        UI.el(
          "option",
          { value: x.Index },
          x.DisplayTitle || `${x.Language || "未知语言"} · ${x.Codec}`,
        ),
      ),
    ),
    subtitleSelect = UI.el("select", { "aria-label": "字幕" }, [
      UI.el("option", { value: -1 }, "关闭字幕"),
      ...subs.map((x) =>
        UI.el(
          "option",
          { value: x.Index },
          x.DisplayTitle || `${x.Language || "未知语言"} · ${x.Codec}`,
        ),
      ),
    ]);
  if (preferred) audioSelect.value = String(preferred.Index);
  subtitleSelect.value =
    c.Subtitles && subtitle ? String(subtitle.Index) : "-1";
  let taskID = "", taskDone = false, taskPaused = false, preparing = false,
    taskHeartbeat, checkingTask = false, taskCommands = Promise.resolve();
  const taskPause = UI.el("button", {class:"secondary", type:"button", hidden:""}, "暂停后台任务"),
    taskStop = UI.el("button", {class:"secondary", type:"button", hidden:""}, "停止后台任务"),
    taskDelete = UI.el("button", {class:"secondary", type:"button", hidden:""}, "删除任务和缓存");
  const showProgress = text => {
    const feedback = video.closest(".watch-player")?.querySelector(".watch-feedback");
    if (feedback) { feedback.textContent = text; feedback.hidden = false; }
  };
  const updateTaskControls = () => {
    taskPause.hidden = taskStop.hidden = !taskID || taskDone;
    taskDelete.hidden = !taskID;
    taskPause.textContent = taskPaused ? "继续后台任务" : "暂停后台任务";
  };
  const clearTask = () => {
    taskID = "";
    clearInterval(taskHeartbeat);
    updateTaskControls();
  };
  // Serialize play/pause commands so a slow response cannot reverse the last action.
  const commandTask = (action, target = taskID) => {
    if (!target) return Promise.resolve();
    const command = taskCommands.catch(() => {}).then(() =>
      api("/features/playback-control", "POST", {ID:target, Action:action}, {keepalive:true}),
    ).then(result => {
      if (taskID === target && (action === "pause" || action === "resume")) {
        taskDone = result.Done;
        taskPaused = result.Paused;
        updateTaskControls();
      }
      return result;
    });
    taskCommands = command;
    return command;
  };
  video.webTranscodeDelete = () => {
    const target = taskID;
    clearTask();
    if (!target) return Promise.resolve();
    // Exit notifications must start immediately, including during page unload.
    return api("/features/playback-control", "POST", {ID:target, Action:"delete"}, {keepalive:true})
      .catch(e => { if (e.status !== 404) throw e; });
  };
  window.addEventListener("pagehide", () => video.webTranscodeDelete().catch(() => {}), {signal:video.webPlayerSignal});
  const updateTaskStatus = status => {
    taskDone = status.Done;
    taskPaused = status.Paused;
    updateTaskControls();
    if (status.Paused) showProgress("后台任务已暂停，点击继续可接着处理");
    else if (preparing) {
      if (status.State === "caching") {
        const percent = status.Total > 0 ? ` ${Math.min(100, Math.floor(status.Downloaded / status.Total * 100))}%` : "";
        showProgress("当前镜像需完整缓存后播放…" + percent + (status.FallbackReason ? ` · ${status.FallbackReason}` : ""));
      } else if (["range", "local", "range-bluray", "local-bluray"].includes(status.SourceMode) || status.State === "reading-disc")
        showProgress("正在按需读取光盘正片，无需下载整张镜像…");
      else showProgress(status.State === "remuxing" ? "正在封装网页播放视频…" : "正在生成网页播放视频…");
    }
  };
  const monitorTask = () => {
    clearInterval(taskHeartbeat);
    taskHeartbeat = setInterval(async () => {
      if (!taskID || checkingTask || video.dataset.stopping === "true") return;
      checkingTask = true;
      const target = taskID;
      try {
        const status = await api.query("/features/playback-status", {ID:target});
        if (taskID !== target) return;
        updateTaskStatus(status);
        if (status.Error && !preparing) {
          clearTask();
          video.pause();
          video.webPlayerError?.(status.Error);
        }
      } catch (e) {
        if (taskID === target && e.status === 404) {
          clearTask();
          video.pause();
          video.webPlayerError?.("播放任务已删除，可重试播放");
        }
      } finally { checkingTask = false; }
    }, 15000);
  };
  taskPause.onclick = run(async () => {
    const resume = taskPaused;
    await commandTask(resume ? "resume" : "pause");
    if (resume) {
      showProgress("后台任务已继续");
      if (!preparing && video.getAttribute("src")) await video.play().catch(() => {});
    } else {
      video.pause();
      showProgress("后台任务已暂停，点击继续可接着处理");
    }
  });
  const endTask = async action => {
    const target = taskID;
    if (!target) return;
    await commandTask(action, target);
    if (taskID !== target) return;
    clearTask();
    video.pause();
    video.removeAttribute("src");
    video.dataset.taskStopped = "true";
    video.load();
    video.webPlayerError?.(action === "delete" ? "任务已停止，正在删除缓存；可重试播放" : "后台任务已停止；可重试播放");
    toast(action === "delete" ? "正在删除任务和缓存" : "后台任务已停止");
  };
  taskStop.onclick = run(() => endTask("stop"));
  taskDelete.onclick = run(() => endTask("delete"));
  video.addEventListener("pause", () => {
    if (taskID && !taskDone && !preparing && !video.ended && video.dataset.stopping !== "true")
      commandTask("pause").catch(e => toast(e.message, {type:"error"}));
  });
  video.addEventListener("play", () => {
    if (taskID && !taskDone && !preparing && video.dataset.stopping !== "true")
      commandTask("resume").catch(e => toast(e.message, {type:"error"}));
  });
  const transcode = async (autoplay = false) => {
    if (video.dataset.stopping === "true" || !video.isConnected) return;
    if (preparing) return;
    const initialDisc = source.IsDisc && !transcodeStarted;
    transcodeStarted = true;
    const start = featurePlaybackPosition(video),
      playing = !video.paused;
    preparing = true;
    try {
      await video.webTranscodeDelete();
      delete video.dataset.taskStopped;
      video.pause();
      showProgress(source.IsDisc ? "正在准备光盘正片…" : "正在转换为网页可播放的视频…");
      const info = await api("/features/playback", "POST", {
        ID: id,
        Audio: initialDisc ? -1 : Number(audioSelect.value || -1),
        Start: start,
        Session: Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join(""),
      });
      if (info.Error) throw Error(info.Error);
      if (!video.isConnected || video.dataset.stopping === "true") {
        await commandTask("delete", info.ID);
        return;
      }
      taskID = info.ID;
      taskDone = info.Done;
      taskPaused = false;
      updateTaskControls();
      monitorTask();
      if (source.IsDisc && !info.Done) {
        while (video.isConnected && video.dataset.stopping !== "true") {
          const status = await api("/features/playback-status?" + new URLSearchParams({ID:info.ID}), "GET", undefined, {signal:video.webPlayerSignal});
          if (taskID !== info.ID) return;
          if (status.Error) throw Error(status.Error);
          updateTaskStatus(status);
          if (status.Ready && !status.Paused) break;
          if (status.Done) throw Error("没有生成可播放的正片视频");
          await new Promise(resolve => setTimeout(resolve, 1000));
        }
        if (!video.isConnected || video.dataset.stopping === "true") return;
      }
      video.dataset.offset = String(start);
      video.src = info.URL + "?" + new URLSearchParams({ api_key: token });
      video.load();
      preparing = false;
      if (playing || source.IsDisc || autoplay === true) await video.play().catch(() => {});
      else await commandTask("pause");
      toast("已切换到转码播放");
    } finally { preparing = false; }
  };
  if (audio.length) {
    controls.append(UI.el("label", {}, ["音轨 ", audioSelect]));
    audioSelect.onchange = run(async () => {
      if (video.audioTracks?.length) {
        for (let i = 0; i < video.audioTracks.length; i++)
          video.audioTracks[i].enabled =
            i === audio.findIndex((x) => String(x.Index) === audioSelect.value);
      } else {
        if (!settings.Transcode) throw Error("切换嵌入音轨需要启用浏览器转码");
        await transcode();
      }
    });
  }
  if (subs.length) {
    controls.append(UI.el("label", {}, ["字幕 ", subtitleSelect]));
    const setSubtitle = () => {
      video
        .querySelectorAll("track[data-feature-subtitle]")
        .forEach((t) => t.remove());
      if (subtitleSelect.value === "-1") return;
      const selected = subs.find(
        (x) => String(x.Index) === subtitleSelect.value,
      );
      const track = UI.el("track", {
        kind: "subtitles",
        label: selected.DisplayTitle || selected.Language || "字幕",
        src:
          "/features/subtitle?" +
          new URLSearchParams({
            ID: id,
            Index: subtitleSelect.value,
            api_key: token,
          }),
        "data-feature-subtitle": "",
        default: "",
      });
      track.onload = () => {
        track.track.mode = "showing";
      };
      track.onerror = () =>
        toast("字幕转换失败，图像字幕请使用兼容播放器", { type: "error" });
      video.append(track);
    };
    subtitleSelect.onchange = setSubtitle;
    setSubtitle();
  }
  if (settings.Transcode)
    controls.append(
      UI.el(
        "button",
        { class: "secondary", type: "button", onclick: run(transcode) },
        "转码播放",
      ),
    );
  controls.append(taskPause, taskStop, taskDelete);
  if (user?.Policy?.IsAdministrator)
    controls.append(
      UI.el(
        "button",
        {
          class: "secondary",
          type: "button",
          onclick: run(async () => {
            await api(
              "/admin/features/artwork?" +
                new URLSearchParams({ ID: id, Kind: "Primary" }),
              "POST",
              { Capture: true, Seconds: featurePlaybackPosition(video) },
            );
            toast("当前画面已保存为缩略图");
          }),
        },
        "截取封面",
      ),
    );
  if (!video.isConnected || video.dataset.stopping === "true") return;
  const tools = video.closest(".watch-player")?.querySelector("[data-player-tools]");
  if (tools) tools.replaceChildren(controls);
  else video.parentElement.after(controls);
  let comments = [],
    enabled = c.Danmaku,
    lastTime = -1,
    commentsPromise,
    cursor = 0;
  const wrapper = UI.el("div", { class: "feature-player-wrap" }),
    layer = UI.el("div", { class: "feature-danmaku", "aria-hidden": "true" });
  video.replaceWith(wrapper);
  wrapper.append(video, layer);
  const fetchComments = () =>
    commentsPromise ||
    (commentsPromise = api("/features/danmaku?ID=" + encodeURIComponent(id))
      .then((b) => {
        comments = (b.Items || []).sort((a, b) => a.Time - b.Time);
      })
      .catch((e) => {
        commentsPromise = null;
        toast(e.message, { type: "error" });
      }));
  const toggle = UI.el(
    "button",
    {
      class: "secondary",
      type: "button",
      "aria-pressed": String(enabled),
      onclick: run(async () => {
        enabled = !enabled;
        toggle.setAttribute("aria-pressed", String(enabled));
        toggle.textContent = enabled ? "弹幕：开" : "弹幕：关";
        if (enabled) await fetchComments();
        else layer.replaceChildren();
      }),
    },
    enabled ? "弹幕：开" : "弹幕：关",
  );
  controls.append(toggle);
  if (enabled) fetchComments();
  video.addEventListener("timeupdate", () => {
    const t = featurePlaybackPosition(video);
    if (!enabled) {
      lastTime = t;
      return;
    }
    if (t < lastTime || t - lastTime > 2) {
      layer.replaceChildren();
      cursor = 0;
      while (cursor < comments.length && comments[cursor].Time < t) cursor++;
      lastTime = t;
      return;
    }
    let emitted = 0;
    while (cursor < comments.length && comments[cursor].Time <= t) {
      const comment = comments[cursor++];
      if (comment.Time > lastTime && emitted++ < 15) {
        const span = UI.el(
          "span",
          {
            style: `color:${/^#[0-9a-f]{6}$/i.test(comment.Color) ? comment.Color : "#fff"};top:${(emitted % 8) * 9 + 6}%`,
          },
          comment.Text,
        );
        layer.append(span);
        span.addEventListener("animationend", () => span.remove(), {
          once: true,
        });
      }
    }
    lastTime = t;
  });
  const signal = (event) => {
    if (!current || current !== id || video.dataset.stopping === "true") return;
    api("/features/playback-event", "POST", {
      ItemId: id,
      PositionTicks: Math.floor(featurePlaybackPosition(video) * 1e7),
      IsPaused: video.paused,
      PlaybackRate: video.playbackRate,
      Event: event,
      Client: "AI Emby Web",
    }).catch(() => {});
  };
  video.addEventListener("pause", () => signal("pause"));
  video.addEventListener("play", () => signal("play"));
  video.addEventListener("seeked", () => signal("seek"));
  if (source.IsISO && !source.IsDisc && settings.Transcode) {
    const fallback = () => {
      if (transcodeStarted || !video.isConnected || video.dataset.stopping === "true") return;
      transcode(true).catch(e => video.webPlayerError?.(e.message));
    };
    video.addEventListener("error", fallback, {signal:video.webPlayerSignal});
    // Some containers play their audio while silently dropping an unsupported video codec.
    video.addEventListener("loadeddata", () => {
      if (!video.videoWidth) fallback();
    }, {signal:video.webPlayerSignal});
    if (video.error || (video.readyState >= 2 && !video.videoWidth)) await transcode(true);
  }
  if (source.IsDisc && !settings.Transcode) throw Error("这是光盘镜像，请管理员在播放管理中开启浏览器转码");
  if (
    settings.Transcode && !transcodeStarted &&
    (source.IsDisc ||
      (preferred &&
        audio.length > 1 &&
        preferred.Index !== (audio.find((x) => x.IsDefault) || audio[0]).Index))
  ) {
    await transcode();
    if (video.isConnected) await video.play().catch(() => {});
  }
  api("/features/chapters?ID=" + encodeURIComponent(id))
    .then((result) => {
      if (!controls.isConnected || !result.Items?.length) return;
      controls.append(
        UI.el(
          "button",
          {
            type: "button",
            class: "secondary",
            onclick: () => {
              let sheet;
              sheet = UI.ActionSheet(
                "章节",
                result.Items.map((chapter, index) => ({
                  label: chapter.Name || `章节 ${index + 1}`,
                  action: () => {
                    const offset = Number(video.dataset.offset || 0);
                    if (chapter.Start < offset) {
                      toast("该章节位于当前转码起点之前，请从头重新播放");
                      return;
                    }
                    video.currentTime = chapter.Start - offset;
                    sheet.close();
                  },
                })),
              );
            },
          },
          "章节",
        ),
      );
      const intro = result.Items.find((x) => x.Kind === "intro");
      if (intro) {
        const button = UI.el(
          "button",
          {
            type: "button",
            class: "secondary",
            hidden: "",
            onclick: () => {
              video.currentTime = Math.max(
                0,
                intro.End - Number(video.dataset.offset || 0),
              );
            },
          },
          "跳过片头",
        );
        controls.append(button);
        video.addEventListener("timeupdate", () => {
          const position = featurePlaybackPosition(video);
          button.hidden = position < intro.Start || position >= intro.End;
        });
      }
    })
    .catch(() => {});
}
