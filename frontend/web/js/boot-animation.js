/* A short, local opening film; app initialization continues underneath it. */
(() => {
  'use strict';
  const key = 'ai-emby-opening-v4';
  // Guests enter the animated login scene directly, without a second curtain.
  try {
    const raw = localStorage.getItem('go-emby-device-session');
    const saved = raw === null ? {token:sessionStorage.getItem('token'),user:JSON.parse(sessionStorage.getItem('user')||'null')} : JSON.parse(raw);
    if (!saved?.token || !saved?.user?.Id) return;
  } catch (_) { return; }
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  try { if (sessionStorage.getItem(key)) return; } catch (_) { /* Storage is optional. */ }
  const root = document.documentElement;
  root.classList.add('boot-pending');
  // Also releases the initial curtain if another script prevents DOM readiness.
  let watchdog = setTimeout(() => root.classList.remove('boot-pending'), 12000);
  document.addEventListener('DOMContentLoaded', () => {
    const previousFocus = document.activeElement;
    const overlay = document.createElement('div');
    overlay.className = 'boot-overlay';
    overlay.setAttribute('role', 'dialog');
    overlay.setAttribute('aria-modal', 'true');
    overlay.setAttribute('aria-label', 'AI EMBY 开屏动画');
    const video = document.createElement('video');
    video.className = 'boot-video';
    video.muted = true;
    video.playsInline = true;
    video.preload = 'auto';
    video.setAttribute('aria-hidden', 'true');
    const skip = document.createElement('button');
    skip.type = 'button';
    skip.className = 'boot-skip';
    skip.textContent = '跳过动画 ↗';
    overlay.append(video, skip);
    const siblings = Array.from(document.body.children).map(el => [el, el.inert]);
    siblings.forEach(([el]) => { el.inert = true; });
    document.body.append(overlay);
    let closed = false, startWatchdog;
    function close() {
      if (closed) return;
      closed = true;
      clearTimeout(watchdog);
      clearTimeout(startWatchdog);
      document.removeEventListener('keydown', onKey);
      root.classList.remove('boot-pending');
      video.pause();
      video.removeAttribute('src');
      video.load();
      overlay.remove();
      siblings.forEach(([el, inert]) => { el.inert = inert; });
      try { sessionStorage.setItem(key, '1'); } catch (_) { /* No persistence required. */ }
      if (previousFocus?.isConnected && previousFocus !== document.body) previousFocus.focus({ preventScroll: true });
      else document.getElementById('app')?.focus({ preventScroll: true });
    }
    function onKey(event) { if (event.key === 'Escape') close(); }
    document.addEventListener('keydown', onKey);
    skip.addEventListener('click', close);
    video.addEventListener('ended', close);
    video.addEventListener('error', close);
    video.addEventListener('playing', () => clearTimeout(startWatchdog), { once: true });
    clearTimeout(watchdog);
    watchdog = setTimeout(close, 12000);
    startWatchdog = setTimeout(close, 4000);
    video.src = '/web/assets/ai-emby-boot-v5-4k.mp4';
    skip.focus({ preventScroll: true });
    video.play().catch(close);
  }, { once: true });
})();
