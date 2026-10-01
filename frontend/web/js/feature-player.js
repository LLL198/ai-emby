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
  if (!video.isConnected) return;
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
  const transcode = async () => {
    const start = featurePlaybackPosition(video),
      playing = !video.paused;
    video.pause();
    const info = await api("/features/playback", "POST", {
      ID: id,
      Audio: Number(audioSelect.value || -1),
      Start: start,
    });
    if (info.Error) throw Error(info.Error);
    if (!video.isConnected) return;
    video.dataset.offset = String(start);
    video.src = info.URL + "?" + new URLSearchParams({ api_key: token });
    video.load();
    if (playing) await video.play().catch(() => {});
    toast("已切换到转码播放");
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
  if (!video.isConnected) return;
  video.parentElement.after(controls);
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
  if (
    settings.Transcode &&
    (source.Container === "iso" ||
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
