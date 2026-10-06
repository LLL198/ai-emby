// HLS.js owns buffering, fragment retries and the full movie timeline.
// Serve the pinned library locally so playback does not require a public CDN.
function featureHLSAvailable(video) {
  return (typeof Hls !== "undefined" && Hls.isSupported()) || !!video.canPlayType("application/vnd.apple.mpegurl");
}
function featureHLSCodecs() {
  const codecs = [];
  if (typeof Hls === "undefined" || !Hls.isSupported()) return codecs;
  const mse = window.MediaSource || window.ManagedMediaSource;
  for (const [name, codec] of [["hevc", "hvc1.1.6.L150.B0"], ["hevc10", "hvc1.2.4.L153.B0"]]) {
    if (mse?.isTypeSupported(`video/mp4; codecs="${codec},mp4a.40.2"`)) codecs.push(name);
  }
  return codecs;
}
function featureHLSPlayer(video, address, start, token, onError, duration) {
  const url = new URL(address, location.href);
  if (url.origin !== location.origin || !url.pathname.startsWith("/features/hls/")) throw Error("分段播放地址无效");
  let hls, seekTimer, disposed = false, recoveries = 0;
  if (typeof Hls !== "undefined" && Hls.isSupported()) {
    hls = new Hls({
      enableWorker: false,
      startPosition: start,
      maxBufferLength: 12,
      maxMaxBufferLength: 20,
      maxBufferSize: 16 * 1024 * 1024,
      backBufferLength: 20,
      xhrSetup(xhr, target) {
        const segment = new URL(target, location.href);
        if (segment.origin !== url.origin || !segment.pathname.startsWith("/features/hls/")) throw Error("播放片段地址无效");
        xhr.setRequestHeader("X-Emby-Token", token);
      },
    });
    if (duration > 0) hls.on(Hls.Events.BUFFER_CREATED, (_, data) => {
      // Clip encoder delay/preroll at the known movie end. Otherwise independent
      // final fragments can extend MediaSource.duration beyond the VOD timeline.
      for (const track of Object.values(data.tracks)) if (track.buffer) track.buffer.appendWindowEnd = duration;
    });
    hls.on(Hls.Events.ERROR, (_, data) => {
      if (disposed || !data.fatal) return;
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR && recoveries++ === 0) { hls.recoverMediaError(); return; }
      onError(data.type === Hls.ErrorTypes.MEDIA_ERROR ? "当前设备无法解码该播放流，可重试兼容播放" : "播放片段加载失败，请重试播放", data.type === Hls.ErrorTypes.MEDIA_ERROR);
    });
    hls.loadSource(url.href);
    hls.attachMedia(video);
  } else {
    url.searchParams.set("api_key", token);
    video.src = url.href;
  }
  return {
    seek(position) {
      if (disposed) return;
      clearTimeout(seekTimer);
      // Abort both init and media loads before selecting another discontinuity.
      // Their rejected promises must settle while the loaders are stopped;
      // otherwise an old init failure can abort the newly selected fragment.
      hls?.stopLoad();
      video.dataset.resume = String(position);
      video.currentTime = position;
      if (hls) seekTimer = setTimeout(() => {
        if (!disposed) hls.startLoad(position);
      }, 0);
    },
    destroy() { disposed = true; clearTimeout(seekTimer); hls?.destroy(); },
  };
}
