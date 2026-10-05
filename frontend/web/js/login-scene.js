/* The background belongs to the login view; release media on every departure. */
const LoginScene = (() => {
  let cleanup = null;
  function destroy() { cleanup?.(); cleanup = null; }
  function mount(scene) {
    destroy();
    const events = new AbortController(), options = {signal:events.signal};
    const film = scene.querySelector('.login-film');
    const title = scene.querySelector('.login-title-canvas'), titleContext = title.getContext('2d');
    const card = scene.querySelector('.login-card');
    const preference = matchMedia('(prefers-reduced-motion: reduce)');
    let failed = false, autoplayBlocked = false, formRevealed = false, completed = false;
    let titleFrame = 0;
    const motionAllowed = () => !preference.matches && !navigator.connection?.saveData;
    // Same glyph-by-glyph outline and metallic fill as the original opening film.
    // A separate canvas keeps the title aligned with the form at every aspect ratio.
    const smooth = (a,b,t) => {const x=Math.max(0,Math.min(1,(t-a)/(b-a)));return x*x*(3-2*x);};
    function drawTitle() {
      cancelAnimationFrame(titleFrame);titleFrame=0;
      const still = !motionAllowed() || failed || autoplayBlocked || (completed && !film.hasAttribute('src'));
      const t = still ? 5.5 : film.currentTime;
      scene.classList.toggle('scene-still',still);
      scene.style.setProperty('--title-caption',smooth(3.45,4.2,t));
      const reveal=still||formRevealed ? 1 : smooth(3.45,4.2,t);
      scene.style.setProperty('--form-reveal',reveal);
      card.inert=reveal<.1;
      // Keep the form visible after its first entrance, including while typing.
      if(reveal>=1)formRevealed=true;
      const box = title.getBoundingClientRect(), ratio = Math.min(devicePixelRatio || 1, 3);
      const width = Math.round(box.width * ratio), height = Math.round(box.height * ratio);
      if (title.width !== width || title.height !== height) {title.width=width;title.height=height;}
      const ctx=titleContext;
      ctx.setTransform(title.width/720,0,0,title.height/170,0,0);
      ctx.clearRect(0,0,720,170);ctx.save();ctx.translate(8,145);
      ctx.font='italic 900 140px "Segoe UI", Arial, sans-serif';ctx.lineJoin='round';
      ctx.strokeStyle='rgba(165,212,239,.65)';ctx.lineWidth=1.5;
      const metallic=ctx.createLinearGradient(0,-135,0,20);
      for(const [offset,color] of [[0,'#fbfdff'],[.31,'#dbe9f5'],[.5,'#8da3bc'],[.53,'#bed6ed'],[.74,'#f5fbff'],[1,'#66829b']])metallic.addColorStop(offset,color);
      ctx.fillStyle=metallic;
      const lettering='AI EMBY';
      // Center the form under the visible lettering, excluding the canvas gutter.
      const metrics=ctx.measureText(lettering);
      const titleCenter=`${(8+(metrics.actualBoundingBoxRight-metrics.actualBoundingBoxLeft)/2)/720*100}%`;
      if(scene.style.getPropertyValue('--title-center')!==titleCenter)scene.style.setProperty('--title-center',titleCenter);
      let index=0;
      for(let i=0;i<lettering.length;i++){
        const glyph=lettering[i];if(glyph===' ')continue;
        const start=2.35+index*.18,trace=smooth(start,start+.16,t);
        const fill=smooth(3.25+index*.12,3.55+index*.12,t);index++;
        if(!trace)continue;
        const x=ctx.measureText(lettering.slice(0,i)).width+8*(1-trace),y=6*(1-trace);
        ctx.shadowBlur=0;ctx.globalAlpha=trace;ctx.strokeText(glyph,x,y);
        if(fill){ctx.globalAlpha=trace*fill;ctx.shadowColor=`rgba(101,188,255,${.5*fill})`;ctx.shadowBlur=16;ctx.fillText(glyph,x,y);}
      }
      ctx.restore();
      if(!film.paused&&!document.hidden&&motionAllowed()&&!failed)titleFrame=requestAnimationFrame(drawTitle);
    }
    const titleSize = new ResizeObserver(drawTitle);titleSize.observe(title);
    film.addEventListener('timeupdate',drawTitle,options);
    film.addEventListener('seeked',drawTitle,options);
    function play() {
      if (!motionAllowed() || document.hidden || failed || completed) return;
      if (!film.hasAttribute('src')) film.src = '/web/assets/ai-emby-login-background-v5-4k.mp4';
      film.play().catch(() => {if(events.signal.aborted)return;autoplayBlocked=true;drawTitle();});
    }
    function sync() {
      if (!motionAllowed()) {
        film.pause(); film.removeAttribute('src'); film.load(); scene.classList.remove('film-ready');
      } else if (document.hidden) film.pause();
      else play();
      drawTitle();
    }
    film.muted = true;
    film.addEventListener('playing', () => {autoplayBlocked=false;scene.classList.add('film-ready');drawTitle();}, options);
    film.addEventListener('pause', drawTitle, options);
    film.addEventListener('ended', () => {completed=true;formRevealed=true;drawTitle();}, options);
    film.addEventListener('error', () => {failed = true; film.pause(); scene.classList.remove('film-ready');drawTitle();}, options);
    document.addEventListener('visibilitychange', sync, options);
    preference.addEventListener('change', sync, options);
    navigator.connection?.addEventListener('change', sync, options);
    const password = scene.querySelector('#login-password'), toggle = scene.querySelector('.login-password-toggle');
    toggle.addEventListener('click', () => {
      const reveal = password.type === 'password';
      password.type = reveal ? 'text' : 'password';
      toggle.setAttribute('aria-pressed', String(reveal));
      toggle.setAttribute('aria-label', reveal ? '隐藏密码' : '显示密码');
      toggle.title = reveal ? '隐藏密码' : '显示密码';
    }, options);
    cleanup = () => {events.abort();cancelAnimationFrame(titleFrame);titleSize.disconnect();film.pause();film.removeAttribute('src');film.load();};
    sync();
  }
  return {mount, destroy};
})();
