(() => {
  const film = document.getElementById('opening-film');
  const replay = document.getElementById('replay');
  const eyeFocus = document.getElementById('eye-focus');
  const titleFocus = document.getElementById('title-focus');
  const status = document.getElementById('preview-status');
  async function playFrom(time) {
    film.currentTime = time;
    status.textContent = `7.2 秒 · ${film.videoWidth || 3840} × ${film.videoHeight || 2160} · 静音`;
    try { await film.play(); } catch (_) { status.textContent = '请点击画面中的播放按钮。'; }
  }
  replay.addEventListener('click', () => playFrom(0));
  eyeFocus.addEventListener('click', () => playFrom(2.5));
  titleFocus.addEventListener('click', () => playFrom(2.15));
  film.addEventListener('play', () => { replay.innerHTML = '重新播放 <span>↗</span>'; });
  film.addEventListener('ended', () => { status.textContent = '播放结束 · 可以重新播放或下载 MP4'; });
  film.addEventListener('error', () => { status.textContent = '视频加载失败，请刷新页面重试。'; });
})();
