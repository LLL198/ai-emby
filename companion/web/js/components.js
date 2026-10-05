// Small DOM factories. No virtual DOM or component lifecycle.
const UI = (() => {
  const icons = {
    search:
      '<svg viewBox="0 0 24 24"><circle cx="10" cy="10" r="7"/><path d="m15 15 6 6"/></svg>',
    close: '<svg viewBox="0 0 24 24"><path d="m6 6 12 12M18 6 6 18"/></svg>',
    back: '<svg viewBox="0 0 24 24"><path d="m15 5-7 7 7 7"/></svg>',
  };
  const githubIcon = '<svg class="github-brand-icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82A7.65 7.65 0 0 1 8 3.86c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"/></svg>';
  function githubRevisionLink(version, menu = false) {
    return `<a class="github-revision-link${menu ? ' github-revision-link--menu' : ''}" href="https://github.com/LLL198/ai-emby" target="_blank" rel="noopener noreferrer" aria-label="AI Emby GitHub，版本 ${esc(version)}">${githubIcon}${menu ? `<span class="github-revision-text"><strong>AI Emby</strong><small>${esc(version)}</small></span>` : `<small>${esc(version)}</small>`}</a>`;
  }
  function el(tag, attrs = {}, children = []) {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (k === "class") n.className = v;
      else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
      else if (v !== undefined) n.setAttribute(k, String(v));
    }
    for (const c of [].concat(children))
      if (c != null)
        n.append(c instanceof Node ? c : document.createTextNode(String(c)));
    return n;
  }
  const toastTimers = new WeakMap();
  function dismissToast(item) {
    if (!item) return;
    clearTimeout(toastTimers.get(item));
    item.remove();
  }
  function Toast(title, options = {}) {
    if (typeof options === "string") options = {type: options};
    title = String(title || "操作完成");
    const type = options.type || (/失败|错误|无效|不支持|不能|无法|超时|拒绝/.test(title) ? "error" : "success");
    const message = String(options.message || "");
    let stack = document.querySelector(".toast-stack");
    const watch = document.querySelector(".watch-dialog .watch-player");
    if (!stack) {
      stack = el("div", {class:"toast-stack", "aria-live":"polite", "aria-relevant":"additions removals"});
      (watch || document.body).append(stack);
    }
    if (watch && !watch.contains(stack)) watch.append(stack);
    const duplicate = [...stack.children].find(item => item.dataset.key === `${type}:${title}:${message}`);
    if (duplicate) return duplicate;
    while (stack.childElementCount >= 3) dismissToast(stack.firstElementChild);
    const icon = {success:"✓", info:"i", warning:"!", error:"×"}[type] || "i";
    const item = el("div", {class:`toast-item toast-item--${type}`, role:type === "error" ? "alert" : "status"});
    item.dataset.key = `${type}:${title}:${message}`;
    const close = el("button", {type:"button", class:"toast-close", "aria-label":"关闭提示"}, "×");
    close.addEventListener("click", () => dismissToast(item));
    item.append(el("span", {class:"toast-icon", "aria-hidden":"true"}, icon),
      el("div", {class:"toast-content"}, [el("strong", {class:"toast-title"}, title), el("small", {class:"toast-message"}, message)]),
      close, el("span", {class:"toast-progress", "aria-hidden":"true"}));
    stack.append(item);
    toastTimers.set(item, setTimeout(() => dismissToast(item), 3600));
    return item;
  }
  function IconButton(label, icon, action) {
    const b = el("button", {
      class: "secondary icon-button",
      type: "button",
      "aria-label": label,
      title: label,
      onclick: action,
    });
    b.innerHTML = icons[icon] || icons.close;
    return b;
  }
  const posterObserver = typeof IntersectionObserver === "undefined" ? null : new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      const img = entry.target;
      img.setAttribute("fetchpriority", "auto");
      img.src = img.dataset.posterSrc;
      posterObserver.unobserve(img);
    }
  }, { rootMargin: "0px" });
  const removalObserver = new MutationObserver(records => {
    for (const record of records) for (const node of record.removedNodes) {
      if (!(node instanceof Element) || node.isConnected) continue;
      if (node.matches("img[data-poster-src]")) posterObserver?.unobserve(node);
      node.querySelectorAll("img[data-poster-src]").forEach(img => posterObserver?.unobserve(img));
    }
  });
  document.addEventListener("DOMContentLoaded", () => removalObserver.observe(document.body, {childList:true, subtree:true}), {once:true});
  function ShelfArrow(direction, action) {
    const button = IconButton(direction < 0 ? "向左滚动" : "向右滚动", "back", action);
    button.classList.add("shelf-arrow", direction < 0 ? "shelf-prev" : "shelf-next");
    return button;
  }
  function bindMediaShelf(shelf) {
    if (shelf.parentElement?.classList.contains("shelf-track")) return shelf.parentElement;
    const track = el("div", {class:"shelf-track"}, shelf);
    shelf.tabIndex = 0;
    shelf.addEventListener("dragstart", e => e.preventDefault());
    const scroll = direction => shelf.scrollBy({left: direction * shelf.clientWidth * .85,
      behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth"});
    const prev = ShelfArrow(-1, () => scroll(-1));
    const next = ShelfArrow(1, () => scroll(1));
    track.append(prev, next);
    const update = () => {
      prev.disabled = shelf.scrollLeft <= 2;
      next.disabled = shelf.scrollLeft + shelf.clientWidth >= shelf.scrollWidth - 2;
      track.classList.toggle("can-scroll-left", !prev.disabled);
      track.classList.toggle("can-scroll-right", !next.disabled);
    };
    shelf.addEventListener("scroll", update, {passive:true});
    track.addEventListener("pointerenter", update);
    track.addEventListener("focusin", update);
    requestAnimationFrame(update);
    shelf.addEventListener("wheel", e => {
      // Preserve horizontal and fine-grained trackpad gestures; map coarse wheel ticks.
      if (e.ctrlKey || e.deltaX || !e.deltaY || (e.deltaMode === 0 && Math.abs(e.deltaY) < 40)) return;
      const delta = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? shelf.clientWidth : 1);
      if ((delta < 0 && shelf.scrollLeft <= 0) || (delta > 0 && shelf.scrollLeft + shelf.clientWidth >= shelf.scrollWidth - 1)) return;
      e.preventDefault();
      shelf.scrollLeft += delta;
    }, {passive:false});
    let drag = null, suppressUntil = 0;
    shelf.addEventListener("pointerdown", e => {
      if (e.pointerType !== "mouse" || e.button !== 0) return;
      suppressUntil = 0;
      drag = {id:e.pointerId, x:e.clientX, left:shelf.scrollLeft, moved:false};
    });
    shelf.addEventListener("pointermove", e => {
      if (!drag || e.pointerId !== drag.id) return;
      const delta = e.clientX - drag.x;
      if (!drag.moved && Math.abs(delta) <= 7) return;
      if (!drag.moved) {
        drag.moved = true;
        shelf.setPointerCapture(e.pointerId);
        shelf.classList.add("is-dragging");
      }
      e.preventDefault();
      shelf.scrollLeft = drag.left - delta;
    });
    const finish = () => {
      if (drag?.moved) suppressUntil = performance.now() + 400;
      drag = null;
      shelf.classList.remove("is-dragging");
    };
    for (const event of ["pointerup", "pointercancel", "lostpointercapture"]) shelf.addEventListener(event, finish);
    shelf.addEventListener("pointerleave", () => { if (!drag?.moved) drag = null; });
    shelf.addEventListener("click", e => {
      if (e.detail && performance.now() < suppressUntil) {
        e.preventDefault(); e.stopImmediatePropagation();
      }
    }, true);
    shelf.addEventListener("keydown", e => {
      if (e.target !== shelf || !["ArrowLeft", "ArrowRight"].includes(e.key)) return;
      e.preventDefault(); scroll(e.key === "ArrowLeft" ? -1 : 1);
    });
    return track;
  }
  function FeaturedHero(item) {
    // Reuse the exact list poster URL: no extra image variant or detail request.
    const img = el("img", {class:"featured-backdrop media-poster", src:api.image(item.Id), alt:"", decoding:"async"});
    img.draggable = false;
    img.onerror = () => { img.removeAttribute("src"); img.classList.add("image-missing"); };
    return el("section", {class:"featured-hero media-interactive", "aria-label":"精选推荐"}, [img,
      el("div", {class:"featured-content"}, [el("span", {class:"featured-label"}, "精选推荐"),
        el("h1", {}, item.Name),
        el("div", {class:"detail-meta"}, [item.ProductionYear, item.CommunityRating ? "★ " + item.CommunityRating : ""].filter(Boolean).join(" · ")),
        el("div", {class:"detail-actions"}, [
          el("button", {class:"media-primary", onclick:run(() => detail(item.Id, {play:true}))}, "▶ 播放"),
          el("button", {class:"secondary", onclick:run(() => detail(item.Id))}, "更多信息")])])]);
  }
  function FeaturedCarousel(items) {
    const slides = items.slice(0, 5);
    const host = el("div", {class:"featured-carousel", "aria-label":"精选推荐"});
    host.addEventListener("dragstart", e => e.preventDefault());
    host.addEventListener("dblclick", e => e.preventDefault());
    host.addEventListener("contextmenu", e => e.preventDefault());
    let index = 0;
    const render = () => {
      const hero = FeaturedHero(slides[index]);
      if (slides.length > 1) {
        const controls = el("div", {class:"featured-controls", "aria-label":"切换推荐"});
        const move = step => { index = (index + step + slides.length) % slides.length; render(); };
        controls.append(el("button", {class:"secondary", onclick:() => move(-1), "aria-label":"上一张推荐"}, "‹"));
        slides.forEach((item, n) => controls.append(el("button", {class:"secondary", "aria-label":item.Name, "aria-current":String(n === index), onclick:() => { index = n; render(); }}, String(n + 1))));
        controls.append(el("button", {class:"secondary", onclick:() => move(1), "aria-label":"下一张推荐"}, "›"));
        hero.querySelector(".featured-content").append(controls);
      }
      host.replaceChildren(hero);
    };
    render();
    return host;
  }
  function DetailHero(item) {
    const image = el("img", {class:"detail-backdrop", src:api.image(item.Id, "Backdrop", 1200), decoding:"async", alt:""});
    image.onerror = () => { image.onerror = null; image.src = api.image(item.Id); };
    return el("section", {class:"detail-hero"}, [image,
      el("div", {class:"detail-shell"}, el("h2", {class:"detail-title"}, item.Name))]);
  }
  function PosterCard(item, open) {
    const img = el("img", {
      class: "media-poster",
      ...(posterObserver ? { "data-poster-src": api.image(item.Id) } : { src: api.image(item.Id) }),
      alt: "",
      loading: "lazy",
      decoding: "async",
    });
    img.onload = () => img.classList.add("image-loaded");
    if (posterObserver) posterObserver.observe(img);
    img.onerror = () => {
      img.removeAttribute("src");
      img.classList.add("image-missing");
    };
    const card = el(
      "article",
      {
        class: "card media-interactive",
        role: "button",
        tabindex: 0,
        "data-id": item.Id,
        "data-type": item.Type,
        ...(typeof item.UserData?.IsFavorite === "boolean" ? {"data-favorite": String(item.UserData.IsFavorite)} : {}),
        "data-folder": !!item.IsFolder,
        "aria-label": item.Name,
      },
      [
        img,
        ...(["Series", "Season"].includes(item.Type) && Number(item.RecursiveItemCount) > 0
          ? [el("span", {class:"episode-count", "aria-label":`${item.RecursiveItemCount} 集`}, `${item.RecursiveItemCount} 集`)] : []),
        el("div", {class: "poster-caption"}, [el("strong", {}, item.Name),
        el(
          "span",
          { class: "muted" },
          item.Type === "Season"
            ? `第 ${item.IndexNumber ?? ""} 季`
            : item.Type === "Episode"
              ? `第 ${item.IndexNumber ?? ""} 集`
              : [item.ProductionYear, item.CommunityRating ? "★ " + item.CommunityRating : ""].filter(Boolean).join(" · "),
        ),
        ]),
      ],
    );
    card.onclick = run(() => open(item));
    img.draggable = false;
    card.addEventListener("dragstart", e => e.preventDefault());
    card.addEventListener("dblclick", e => e.preventDefault());
    card.addEventListener("contextmenu", e => e.preventDefault());
    card.onkeydown = (e) => {
      if (["Enter", " "].includes(e.key)) {
        e.preventDefault();
        card.click();
      }
    };
    const pct = item.UserData?.PlayedPercentage;
    if (pct > 0)
      card.append(
        el("progress", { max: 100, value: pct, "aria-label": "观看进度" }),
      );
    return card;
  }
  function LibraryCard(item, open) {
    const c = PosterCard(item, open);
    c.classList.add("library-card");
    const img = c.querySelector("img");
    if (posterObserver) img.dataset.posterSrc = api.image(item.Id, "Primary", 640);
    else img.src = api.image(item.Id, "Primary", 640);
    return c;
  }
  function MediaShelf(title, items, open, more, library = false) {
    const head = el("div", { class: "section-heading" }, el("h2", {}, title));
    if (more)
      head.append(
        el("button", { class: "text-button", onclick: run(more) }, "查看全部"),
      );
    const shelf = el(
      "div",
      {
        class: "media-shelf" + (library ? " library-shelf" : ""),
        "aria-label": title,
      },
      items.map((x) => (library ? LibraryCard : PosterCard)(x, open)),
    );
    return el("section", { class: "shelf-section" }, [
      head,
      items.length ? bindMediaShelf(shelf) : el("p", { class: "empty" }, "暂无媒体"),
    ]);
  }
  function Modal(title, content) {
    const d = el("dialog", { "aria-label": title }, [
      el("div", { class: "section-heading" }, [
        el("h2", {}, title),
        IconButton("关闭", "close", () => d.close()),
      ]),
      content,
    ]);
    d.addEventListener("close", () => d.remove(), { once: true });
    document.body.append(d);
    d.showModal();
    return d;
  }
  function BottomSheet(title, content, options = {}) {
    const variant = options.variant || "compact-list";
    const d = el("dialog", { class: "bottom-sheet bottom-sheet--" + variant, "aria-label": title });
    const heading = el("div", { class: "sheet-heading" }, [el("h2", {}, title)]);
    if (variant === "account-menu") d.append(content);
    else d.append(el("span", { class: "sheet-drag-indicator", "aria-hidden": "true" }), heading, content);
    const nativeClose = d.close.bind(d);
    let closing = false, fallback;
    d.close = () => {
      if (closing || !d.open) return;
      closing = true;
      if (matchMedia("(prefers-reduced-motion: reduce)").matches) { nativeClose(); return; }
      d.classList.add("is-closing");
      fallback = setTimeout(nativeClose, 320);
    };
    d.addEventListener("animationend", e => {
      if (closing && e.target === d && (e.animationName === "sheet-exit" || e.animationName === "account-menu-exit")) {
        clearTimeout(fallback);
        nativeClose();
      }
    });
    d.addEventListener("cancel", e => { e.preventDefault(); d.close(); });
    d.addEventListener("click", e => {
      const bounds = d.getBoundingClientRect();
      if (e.target === d && (e.clientY < bounds.top || e.clientY > bounds.bottom || e.clientX < bounds.left || e.clientX > bounds.right)) d.close();
    });
    d.addEventListener("close", () => { clearTimeout(fallback); d.remove(); }, { once: true });
    if (variant === "user-menu" || variant === "account-menu") d.tabIndex = -1;
    document.body.append(d);
    d.showModal();
    if (variant === "user-menu" || variant === "account-menu") d.focus({ preventScroll: true });
    return d;
  }
  function ImageCropper(file, options) {
    const avatar = options.variant === "avatar";
    const width = avatar ? 320 : 320, height = avatar ? 320 : 180;
    const crop = avatar ? {x:32,y:32,w:256,h:256} : {x:16,y:9,w:288,h:162};
    const d = el("dialog", {class:"image-cropper image-cropper--sheet", "aria-label":avatar?"上传头像":"上传收藏封面"});
    const title = el("h2", {}, avatar ? "上传头像" : "上传收藏封面");
    const canvas = el("canvas", {width, height, class:"image-cropper-canvas image-cropper-mask", "aria-label":"拖动图片调整裁剪位置"});
    const stage = el("div", {class:"image-cropper-stage"}, canvas);
    const slider = el("input", {type:"range", min:"1", max:"3", step:"0.01", value:"1", "aria-label":"缩放图片"});
    const decrease = el("button", {type:"button", class:"secondary", "aria-label":"缩小图片"}, "−");
    const increase = el("button", {type:"button", class:"secondary", "aria-label":"放大图片"}, "+");
    const zoom = el("div", {class:"image-cropper-zoom"}, [decrease, slider, increase]);
    const rotate = el("button", {type:"button", class:"secondary"}, "旋转");
    const reset = el("button", {type:"button", class:"secondary"}, "重置");
    const tools = el("div", {class:"image-cropper-toolbar"}, avatar ? [reset] : [rotate,reset]);
    const cancel = el("button", {type:"button", class:"secondary"}, "取消");
    const remove = el("button", {type:"button", class:"image-cropper-remove"}, "移除封面");
    remove.disabled = !options.hasCover;
    const upload = el("button", {type:"button"}, avatar ? "上传头像" : "上传封面");
    const actions = el("div", {class:"image-cropper-actions"}, avatar ? [cancel,upload] : [upload,remove]);
    d.append(title,stage,zoom,tools,actions);
    let image = null, sourceURL = null, angle = 0, scale = 1, dx = 0, dy = 0, drag = null;
    const baseSize = () => {
      if (!image) return 1;
      const iw = angle % 180 ? image.naturalHeight : image.naturalWidth;
      const ih = angle % 180 ? image.naturalWidth : image.naturalHeight;
      return Math.max(crop.w / iw, crop.h / ih);
    };
    const drawImage = ctx => {
      const size = baseSize() * scale;
      ctx.save(); ctx.translate(width/2+dx,height/2+dy); ctx.rotate(angle*Math.PI/180);
      ctx.drawImage(image,-image.naturalWidth*size/2,-image.naturalHeight*size/2,image.naturalWidth*size,image.naturalHeight*size);
      ctx.restore();
    };
    const clamp = () => {
      if (!image) return;
      const size = baseSize()*scale;
      const iw = (angle%180 ? image.naturalHeight : image.naturalWidth)*size;
      const ih = (angle%180 ? image.naturalWidth : image.naturalHeight)*size;
      dx = Math.max(-(iw-crop.w)/2,Math.min((iw-crop.w)/2,dx));
      dy = Math.max(-(ih-crop.h)/2,Math.min((ih-crop.h)/2,dy));
    };
    const render = () => {
      const ctx = canvas.getContext("2d"); ctx.clearRect(0,0,width,height);
      if (!image) {
        ctx.fillStyle = getComputedStyle(d).getPropertyValue("--surface-2");ctx.fillRect(0,0,width,height);
        ctx.fillStyle = getComputedStyle(d).getPropertyValue("--text-secondary");ctx.font = "14px sans-serif";
        ctx.textAlign="center";ctx.fillText("点击上传封面选择图片",width/2,height/2);return;
      }
      clamp(); drawImage(ctx);
      ctx.fillStyle = "rgba(0,0,0,.56)";
      ctx.beginPath();ctx.rect(0,0,width,height);
      if (avatar) ctx.arc(width/2,height/2,crop.w/2,0,Math.PI*2,true);
      else ctx.rect(crop.x,crop.y,crop.w,crop.h);
      ctx.fill("evenodd");
      ctx.beginPath();
      if (avatar) ctx.arc(width/2,height/2,crop.w/2,0,Math.PI*2);
      else ctx.rect(crop.x,crop.y,crop.w,crop.h);
      ctx.strokeStyle="rgba(255,255,255,.92)";ctx.lineWidth=2;ctx.stroke();
    };
    const setFile = async selected => {
      if (!selected || !["image/jpeg","image/png","image/webp"].includes(selected.type) || selected.size > 5*1024*1024) throw Error("仅支持 5MB 内的 JPEG、PNG、WebP 图片");
      const url = URL.createObjectURL(selected), img = new Image(); img.src = url;
      try { await img.decode(); } catch { URL.revokeObjectURL(url); throw Error("图片无法读取"); }
      if (sourceURL) URL.revokeObjectURL(sourceURL);
      sourceURL=url; image=img; angle=0; scale=1; dx=dy=0; slider.value="1"; render();
    };
    const picker = el("input", {type:"file", accept:"image/jpeg,image/png,image/webp", hidden:""});
    picker.addEventListener("change", async () => { try { await setFile(picker.files?.[0]); } catch(e) { Toast(e.message, {type:"warning"}); } picker.value=""; });
    d.append(picker);
    stage.addEventListener("click",()=>{if(!image)picker.click();});
    const close = () => d.close();
    d.addEventListener("cancel", e => { e.preventDefault(); close(); });
    d.addEventListener("click", e => { if (e.target === d) close(); });
    d.addEventListener("close", () => { if (sourceURL) URL.revokeObjectURL(sourceURL); d.remove(); }, {once:true});
    cancel.onclick=close;
    decrease.onclick=()=>{slider.value=String(Math.max(1,Number(slider.value)-.1));scale=Number(slider.value);render();};
    increase.onclick=()=>{slider.value=String(Math.min(3,Number(slider.value)+.1));scale=Number(slider.value);render();};
    slider.oninput=()=>{scale=Number(slider.value);render();};
    rotate.onclick=()=>{angle=(angle+90)%360;dx=dy=0;render();};
    reset.onclick=()=>{angle=0;scale=1;dx=dy=0;slider.value="1";render();};
    canvas.addEventListener("pointerdown", e=>{if (!image) return;drag={id:e.pointerId,x:e.clientX,y:e.clientY,dx,dy};canvas.setPointerCapture(e.pointerId);});
    canvas.addEventListener("pointermove", e=>{if (!drag || drag.id!==e.pointerId)return;const factor=width/canvas.getBoundingClientRect().width;dx=drag.dx+(e.clientX-drag.x)*factor;dy=drag.dy+(e.clientY-drag.y)*factor;render();});
    for (const event of ["pointerup","pointercancel","lostpointercapture"]) canvas.addEventListener(event,()=>{drag=null;});
    upload.onclick=async()=>{
      if (!image) { picker.click(); return; }
      const output=el("canvas", {width:options.outputWidth||512,height:options.outputHeight||512});
      const ctx=output.getContext("2d"); ctx.fillStyle="#fff";ctx.fillRect(0,0,output.width,output.height);
      ctx.scale(output.width/crop.w,output.height/crop.h);ctx.translate(-crop.x,-crop.y);drawImage(ctx);
      const blob=await new Promise(resolve=>output.toBlob(resolve,"image/webp",.9));
      if (!blob) { Toast("图片处理失败", {type:"error"});return; }
      upload.disabled=true;
      try { await options.onUpload(new File([blob],"image.webp",{type:"image/webp"})); close(); }
      catch(e) { Toast(e.message, {type:"error"}); }
      finally { upload.disabled=false; }
    };
    remove.onclick=async()=>{remove.disabled=true;try{await options.onRemove();close();}catch(e){Toast(e.message,{type:"error"});remove.disabled=false;}};
    document.body.append(d);d.showModal();render();
    if (file) setFile(file).catch(e=>{Toast(e.message,{type:"warning"});close();});
    return d;
  }
  function ActionSheet(title, actions, content = null, options = {}) {
    if (options.variant === "account-menu") {
      const profile = options.profile || {};
      const avatar = el("button", { class: "account-menu-avatar", type: "button", "aria-label": "上传头像", title: "上传头像" },
        String(profile.name || "用").trim().slice(0, 1).toUpperCase());
      const input = el("input", { type: "file", accept: "image/jpeg,image/png,image/webp", hidden: "" });
      const showAvatar = url => {
        if (!url) { avatar.textContent=String(profile.name || "用").trim().slice(0, 1).toUpperCase(); return; }
        const img = el("img", { class: "account-menu-avatar-image", alt: "" });
        img.src = url;
        avatar.replaceChildren(img);
      };
      showAvatar(profile.avatar?.objectURL);
      profile.avatar?.listeners.add(showAvatar);
      avatar.addEventListener("click", () => input.click());
      input.addEventListener("change", async () => {
        const file = input.files?.[0];
        if (!file) return;
        try {
          if (!['image/jpeg','image/png','image/webp'].includes(file.type) || file.size > 5 * 1024 * 1024) throw new Error("仅支持 5MB 内的 JPEG、PNG、WebP 图片");
          ImageCropper(file, {variant:"avatar", onUpload:async cropped=>{
            await profile.uploadAvatar(cropped);
            Toast("头像上传成功", {type:"success", message:"已保存到 images-tx"});
          }});
        } catch (error) { Toast(error.message, {type:"warning"}); }
        input.value = "";
      });
      Promise.resolve(profile.loadAvatar?.()).catch(() => {});
      const identity = el("div", { class: "account-menu-identity" }, [
        el("strong", {}, profile.name || "用户"),
        el("small", {}, profile.server || "AI Emby"),
      ]);
      const header = el("div", { class: "account-menu-header" }, [avatar, identity, input]);
      const rows = actions.map(a => {
        const button = el("button", {
          type: "button",
          class: "account-menu-item" + (a.danger ? " account-menu-item--danger" : ""),
          onclick: run(a.action),
        });
        const icon = el("span", { class: "account-menu-icon", "aria-hidden": "true" });
        icon.innerHTML = a.icon;
        button.append(el("span", { class: "account-menu-label" }, a.label), icon);
        return button;
      });
      const version = el("div", { class: "account-menu-version" });
      version.innerHTML = githubRevisionLink(profile.version || "", true);
      const sheet = BottomSheet(title, el("div", { class: "account-menu-content" }, [
        header, el("div", { class: "account-menu-list" }, rows), version,
      ]), options);
      sheet.addEventListener("close", () => { profile.avatar?.listeners.delete(showAvatar); }, { once: true });
      return sheet;
    }
    if (options.variant === "user-menu") {
      const rows = actions.map(a => {
        const button = el("button", {
          type: "button",
          class: "user-menu-item" + (a.danger ? " user-menu-item--danger" : ""),
          onclick: run(a.action),
        });
        const icon = el("span", { class: "user-menu-icon", "aria-hidden": "true" });
        icon.innerHTML = a.icon;
        button.append(icon, el("span", { class: "user-menu-label" }, a.label));
        if (a.chevron) {
          const chevron = el("span", { class: "user-menu-chevron", "aria-hidden": "true" });
          chevron.innerHTML = icons.back;
          button.append(chevron);
        }
        return button;
      });
      return BottomSheet(title, el("div", { class: "user-menu-list" }, rows), options);
    }
    const ordered = [
      ...actions.filter(a => !a.danger && a.label !== "取消"),
      ...actions.filter(a => !a.danger && a.label === "取消"),
      ...actions.filter(a => a.danger),
    ];
    return BottomSheet(title, el("div", { class: "action-sheet-content" }, [
      content,
      el("div", { class: "action-sheet-list" }, ordered.map(a => el("button", {
        type: "button",
        class: "action-sheet-item" + (a.danger ? " action-sheet-item--danger" : "") + (a.label === "取消" ? " action-sheet-item--cancel" : ""),
        onclick: run(a.action),
      }, a.label))),
    ].filter(Boolean)), { ...options, variant: "compact-list" });
  }
  function SegmentedControl(options, value, change) {
    return el(
      "div",
      { class: "segmented", role: "group" },
      options.map((o) =>
        el(
          "button",
          { "aria-pressed": o.value === value, onclick: () => change(o.value) },
          o.label,
        ),
      ),
    );
  }
  function SettingsRow(label, control, description) {
    return el("label", { class: "settings-row" }, [
      el("span", {}, [
        label,
        description ? el("small", {}, description) : null,
      ]),
      control,
    ]);
  }
  function SettingsGroup(title, rows) {
    return el("section", { class: "settings-group" }, [
      el("h3", {}, title),
      ...rows,
    ]);
  }
  function Switch(checked, change) {
    const n = el("input", {
      type: "checkbox",
      role: "switch",
      class: "switch",
    });
    n.checked = checked;
    n.onchange = () => change(n.checked);
    return n;
  }
  function SearchOverlay(submit) {
    const input = el("input", {
      type: "search",
      placeholder: "搜索片名或首字母",
      "aria-label": "搜索媒体",
      name: "query",
      autocomplete: "off",
    });
    const form = el("form", { class: "search-form" }, [
      input,
      el("button", { type: "submit" }, "搜索"),
    ]);
    const d = Modal("搜索", form);
    form.onsubmit = (e) => {
      e.preventDefault();
      const value = input.value.trim();
      if (value) {
        d.close();
        run(() => submit(value))();
      }
    };
    input.focus();
    return d;
  }
  function LoadingSkeleton() {
    return el(
      "div",
      { class: "page-loading", "aria-label": "正在加载", role: "status" },
      "正在加载…",
    );
  }
  return {
    el,
    icons,
    githubRevisionLink,
    PosterCard,
    bindMediaShelf,
    FeaturedHero,
    FeaturedCarousel,
    DetailHero,
    ShelfArrow,
    MediaShelf,
    LibraryCard,
    Modal,
    BottomSheet,
    ActionSheet,
    SegmentedControl,
    SettingsGroup,
    SettingsRow,
    Switch,
    Toast,
    ImageCropper,
    IconButton,
    SearchOverlay,
    LoadingSkeleton,
  };
})();
