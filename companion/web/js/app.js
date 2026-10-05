const $ = (s) => document.querySelector(s),
  esc = (s) => String(s ?? "").replace(/[&<>"']/g,(c)=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"})[c]);
let token=sessionStorage.token||"",user=JSON.parse(sessionStorage.user||"null"),device=localStorage.device||(localStorage.device=globalThis.crypto?.randomUUID?crypto.randomUUID():String(Math.random()).slice(2)),page=0,parent="",term="",view="browse",heartbeat,current;
function toast(title, options){ return UI.Toast(title, options); }
function run(fn){return async(...args)=>{try{await fn(...args)}catch(e){toast(e.message,{type:"error"})}}}
const buildVersion = "2026.09.28-124350";
function renderBuildFooter(){
 return `<div class="build-footer">${UI.githubRevisionLink(buildVersion)}</div>`;
}
function themeIcon(){return document.documentElement.dataset.theme==="light"?'<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.5 1.5M17.6 17.6l1.5 1.5M2 12h2M20 12h2M4.9 19.1l1.5-1.5M17.6 6.4l1.5-1.5"/></svg>':'<svg viewBox="0 0 24 24"><path d="M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9Z"/></svg>'}
function toggleTheme(){const r=document.documentElement;r.dataset.theme=r.dataset.theme==="light"?"dark":"light";localStorage.setItem("go-emby-theme",r.dataset.theme);nav()}
function initTheme(){const r=document.documentElement;r.dataset.theme=localStorage.getItem("go-emby-theme")||"dark"}
initTheme();
let renderedView = null, mediaHeaderScroll = null;
function syncMediaHeader() {
 const header=document.querySelector("body > header");
 if(mediaHeaderScroll) { window.removeEventListener("scroll", mediaHeaderScroll.onScroll); cancelAnimationFrame(mediaHeaderScroll.frame); mediaHeaderScroll=null; }
 header?.classList.remove("media-header-hidden");
 if(view!=="browse" || !header) return;
 const state={last:window.scrollY, pending:window.scrollY, frame:0, onScroll:null};
 state.onScroll=()=>{
  state.pending=window.scrollY;
  if(state.frame) return;
  state.frame=requestAnimationFrame(()=>{
   state.frame=0;
   const y=state.pending, delta=y-state.last;
   if(y<20) { state.last=y; header.classList.remove("media-header-hidden"); return; }
   if(document.querySelector("dialog[open], .user-menu[open]")) { state.last=y; return; }
   if(Math.abs(delta)<=10) return;
   state.last=y;
   if(delta>10) header.classList.add("media-header-hidden");
   else if(delta < -10) header.classList.remove("media-header-hidden");
  });
 };
 mediaHeaderScroll=state;
 window.addEventListener("scroll", state.onScroll, {passive:true});
}
function navigateAdminSection(n){
 return run(async()=>{
  if(!user?.Policy?.IsAdministrator)return;
  const pending=admin(),generation=adminGeneration;
  await pending;
  if(view!=="admin"||generation!==adminGeneration)return;
  // admin() has already rendered media management after initialization.
  if(n!==0)adminSection(n);
 })();
}
function drawerIcon(name) {
 const paths={dashboard:'M3 13a9 9 0 0 1 18 0M12 13l4-4M5 17h14M8 20h8',back:'M15 5 8 12l7 7M8 12h13',media:'M4 4h16v16H4zM8 8h8M8 12h8M8 16h5',files:'M3 7h7l2 2h9v11H3z',scraper:'M12 3l2 7 7 2-7 2-2 7-2-7-7-2 7-2z',users:'M8 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM2 21v-2a6 6 0 0 1 12 0v2M17 12a4 4 0 0 0-1-8M17 15a5 5 0 0 1 5 5',sort:'M5 5h14M5 12h10M5 19h6',info:'M12 11v7M12 6v1M3 12a9 9 0 1 0 18 0 9 9 0 0 0-18 0',settings:'M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1M9 12a3 3 0 1 0 6 0 3 3 0 0 0-6 0',sub:'M4 15h16M7 10h10M10 5h4',chapter:'M4 5h16M4 12h16M4 19h16M9 3v4M15 10v4M9 17v4',network:'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18ZM3 12h18M12 3c-4 4-4 14 0 18M12 3c4 4 4 14 0 18',api:'M8 8l-4 4 4 4M16 8l4 4-4 4M14 5l-4 14',refresh:'M20 11a8 8 0 1 0-2 6M20 4v7h-7',lock:'M5 11h14v10H5zM8 11V7a4 4 0 0 1 8 0v4',logout:'M10 4H4v16h6M14 7l5 5-5 5M9 12h10'};
 return `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="${paths[name]}"/></svg>`;
}
function renderAdminDrawer(variant){
 const target=(n)=>variant==="files"?`navigateAdminSection(${n})`:n===0?"mediaPage()":`adminSection(${n})`;
 const link=(name,icon,action,current=false)=>`<button${current?' aria-current="page"':''} onclick="${action}">${drawerIcon(icon)}<span>${name}</span></button>`;
 return `<button id="drawer-backdrop" type="button" aria-label="关闭功能菜单" onclick="toggleDrawer(false)"></button><aside id="drawer">${link('返回影库','back','browseRoot()')}${link('控制台','dashboard',target(12))}${link('媒体管理','media',target(0))}${link('文件管理','files','filesPage()',variant==='files')}${link('刮削管理','scraper',target(8))}${link('用户管理','users',target(1))}${link('媒体排序','sort',target(2))}${link('媒体信息','info',target(3))}<details class="drawer-submenu"><summary>${drawerIcon('settings')}<span>增强功能<small>设置 · 字幕 · TMDB · Bot · 片头片尾 · 代理</small></span><svg class="drawer-chevron" viewBox="0 0 24 24" aria-hidden="true"><path d="m8 10 4 4 4-4"/></svg></summary><div class="drawer-submenu-items">${link('增强设置','settings',target(4))}${link('字幕增强','sub',target(9))}${link('TMDB','scraper',target(6))}${link('Telegram Bot','users',target(7))}${link('片头片尾','chapter',target(10))}${link('代理设置','network',target(11))}</div></details>${link('API','api',target(5))}${renderBuildFooter()}</aside>`;
}
let drawerScrollY = null, drawerScrollView = null;
function toggleDrawer(open) {
 const hamburger=$("#hamburger");
 const wasOpen = document.body.classList.contains('drawer-open');
 if (open && !wasOpen) { drawerScrollY = window.scrollY; drawerScrollView = view; }
 document.body.classList.toggle('drawer-open',open);
 if (!open && wasOpen && drawerScrollY !== null) {
  const y = drawerScrollY, restore = drawerScrollView === view;
  drawerScrollY = null; drawerScrollView = null;
  if (restore) requestAnimationFrame(() => window.scrollTo(0, y));
 }
 hamburger?.setAttribute('aria-expanded',String(open));
 hamburger?.setAttribute('aria-label',open?'收起功能菜单':'展开功能菜单');
}
const avatarState={userId:null,blob:null,objectURL:null,generation:0,loading:null,listeners:new Set()};
function clearAvatar(){
 avatarState.generation++;
 avatarState.loading=null;
 avatarState.blob=null;
 const old=avatarState.objectURL;
 avatarState.objectURL=null;
 avatarState.listeners.forEach(fn=>fn(null));
 if(old)URL.revokeObjectURL(old);
}
function avatarForUser(){
 const id=user?.Id||null;
 if(avatarState.userId!==id){clearAvatar();avatarState.userId=id}
 return avatarState;
}
function setAvatar(blob){
 const state=avatarForUser();
 state.generation++;
 const old=state.objectURL;
 state.blob=blob;
 state.objectURL=blob?URL.createObjectURL(blob):null;
 paintTopAvatar(state.objectURL);
 state.listeners.forEach(fn=>fn(state.objectURL));
 if(old)URL.revokeObjectURL(old);
}
function paintTopAvatar(url){
 const trigger=document.querySelector('.user-menu-trigger');
 if(!trigger)return;
 if(url){const img=document.createElement('img');img.className='user-menu-trigger-image';img.alt='';img.src=url;trigger.replaceChildren(img)}
}
async function loadAvatar(){
 const state=avatarForUser(),id=state.userId,generation=state.generation;
 if(!id||state.blob)return state.blob;
 if(state.loading)return state.loading;
 const request=(async()=>{
  const res=await fetch(`/Users/${encodeURIComponent(id)}/Avatar`,{headers:{'X-Emby-Token':token}});
  if(!res.ok)return null;
  const blob=await res.blob();
  if(avatarState.userId===id&&avatarState.generation===generation)setAvatar(blob);
  return blob;
 })();
 state.loading=request;
 try{return await request}finally{if(state.loading===request)state.loading=null}
}
function openUserMenu(event) {
 event.preventDefault();
 const menu=event.currentTarget.closest('.user-menu');
 menu.open=false;
 const profileUserId=user?.Id;
 const isAdmin=user?.Policy?.IsAdministrator === true;
 const entries=[
  ...(isAdmin?[{label:'后台管理',action:admin,icon:'settings'},
    {label:'文件管理',action:filesPage,icon:'files'},
    {label:'检测更新',action:checkUpdates,icon:'refresh'}]:[]),
  {label:'修改密码',action:password,icon:'lock'},
  {label:'退出登录',action:logout,icon:'logout',danger:true}
 ];
 let sheet;
 sheet=UI.ActionSheet('账户',entries.map(x=>({...x,icon:drawerIcon(x.icon),action:()=>{sheet.close();run(x.action)()}})),null,{
  variant:'account-menu',profile:{name:user?.Name||'用户',server:serverName||'AI Emby',version:buildVersion,avatar:avatarForUser(),loadAvatar,uploadAvatar:async file=>{await api(`/Users/${encodeURIComponent(profileUserId)}/Avatar`,'POST',file,{raw:true});if(user?.Id===profileUserId)setAvatar(file)}}
 });
}

function nav(){
 if(view!=="admin")stopConsolePolling();
 if(renderedView!==view){$("#app")?.replaceChildren();renderedView=view; if(view!=="browse" && typeof resumeMenuAbort!=="undefined") { resumeMenuAbort.abort(); resumeMenuAbort=new AbortController(); }}
 syncMediaHeader();
 if(view!=="login")stopLoginMark();
 document.body.classList.toggle("media-view",view==="browse");document.body.classList.toggle("admin-view",view==="admin");toggleDrawer(false);const n=$("#nav");if(!n)return;
 n.innerHTML=`${token&&view==="admin"?'<button class="secondary icon-button" title="实时日志" aria-label="实时日志" onclick="showLogs()"><svg viewBox="0 0 24 24"><path d="M4 4h16v16H4zM8 8h8M8 12h8M8 16h5"/></svg></button>':""}${token?`${view!=="admin"&&view!=="files"?`<button class="secondary icon-button" aria-label="搜索" title="搜索" onclick="openSearch()">${UI.icons.search}</button>`:""}<details class="user-menu"><summary class="icon-button user-menu-trigger" onclick="openUserMenu(event)" title="${esc(user?.Name||"用户")}" aria-label="用户菜单"><svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 21v-2a8 8 0 0 1 16 0v2"/></svg></summary></details>`:""}<button class="secondary icon-button" title="切换明暗模式" aria-label="切换明暗模式" onclick="toggleTheme()">${themeIcon()}</button>`;
 paintTopAvatar(avatarForUser().objectURL);
 loadAvatar().catch(()=>{});
 $("#hamburger")?.remove();$("#drawer")?.remove();$("#drawer-backdrop")?.remove();
 if((view==="admin"||view==="files")&&user?.Policy?.IsAdministrator){document.querySelector("header").insertAdjacentHTML("afterbegin",'<button id="hamburger" aria-label="展开功能菜单" aria-expanded="false"><span></span><span></span><span></span></button>');const drawer=renderAdminDrawer(view);document.body.insertAdjacentHTML("beforeend",drawer);$("#drawer").addEventListener("click",e=>{if(e.target.closest("button")){toggleDrawer(false)}});$("#hamburger").onclick=()=>{toggleDrawer($("#hamburger").getAttribute("aria-expanded")!=="true")}}
}
function serverDisplayName(value){const name=typeof value==="string"?value.trim():"";return name||"AI Emby"}
let serverName="AI Emby",loginMarkTimer=null;
function stopLoginMark(){clearTimeout(loginMarkTimer);loginMarkTimer=null}
function renderLoginMark(){
 stopLoginMark();const mark=$(".login-mark");if(view!=="login"||!mark)return;
 const name=serverName||"AI Emby",letters=Array.from(name),reduced=matchMedia("(prefers-reduced-motion: reduce)").matches;
 mark.setAttribute("aria-label",name);mark.textContent="";
 const text=document.createElement("span");text.setAttribute("aria-hidden","true");mark.append(text);
 mark.classList.toggle("login-mark-typing",!reduced);
 if(reduced){text.textContent=name;return}
 let index=0;
 function type(){loginMarkTimer=null;if(view!=="login"||!mark.isConnected)return;text.textContent=letters.slice(0,++index).join("");if(index<letters.length)loginMarkTimer=setTimeout(type,70)}
 type();
}
function login(){closeLogs();view="login";nav();$("#app").innerHTML=`<section class="login-page"><div class="login-card"><div class="login-mark" role="img" aria-label="${esc(serverName)}"></div><form id="login" class="login-form"><input name="username" autocomplete="username" placeholder="用户名" aria-label="用户名" required><input name="pw" type="password" autocomplete="current-password" placeholder="密码" aria-label="密码"><button class="login-submit" aria-label="登录">登录</button></form></div></section>`;renderLoginMark();loadServerName();$("#login").onsubmit=run(async(e)=>{e.preventDefault();const f=new FormData(e.target),b=await api.login(f.get("username"),f.get("pw"));token=b.AccessToken;user=b.User;sessionStorage.token=token;sessionStorage.user=JSON.stringify(user);nav();browseRoot()})}
async function logout(){await stop();try{await api.logout()}catch{}token="";user=null;avatarForUser();sessionStorage.removeItem("token");sessionStorage.removeItem("user");login()}
function password(){view="password";nav();$("#app").innerHTML=`<div class="panel login"><h2>修改密码</h2><form id="pw" style="display:grid;gap:15px"><input name="old" type="password" placeholder="当前密码"><input name="new" type="password" placeholder="新密码（普通用户可留空，管理员至少 12 字节）"><button>保存并重新登录</button></form></div>`;$("#pw").onsubmit=run(async(e)=>{e.preventDefault();const f=new FormData(e.target);await api(`/Users/${user.Id}/Password`,"POST",{CurrentPw:f.get("old"),NewPw:f.get("new")});await logout();toast("密码已修改")})}
function routeFromLocation(){if(!token||!user)return;if(location.hash.startsWith("#files")&&user.Policy?.IsAdministrator){filesPage();return}if(location.hash==="#admin"&&user.Policy?.IsAdministrator){admin();return}browseRoot()}
window.addEventListener("popstate",()=>routeFromLocation());window.addEventListener("hashchange",()=>{if(view!=="files"&&location.hash.startsWith("#files"))routeFromLocation()});
document.addEventListener("DOMContentLoaded",()=>{window.addEventListener("unhandledrejection",(e)=>{toast(e.reason?.message||"请求失败",{type:"error"});e.preventDefault()});if(token&&user){nav();setTimeout(routeFromLocation,0);loadServerName()}else login()});

async function checkUpdates(){
 if(checkUpdates.pending)return;
 checkUpdates.pending=true;
 document.querySelector(".user-menu")?.removeAttribute("open");
 const dialog=document.createElement("dialog");
 dialog.className="confirm-dialog";
 dialog.setAttribute("aria-label","检测更新");
 let updating=false;
 dialog.addEventListener("cancel",e=>{if(updating)e.preventDefault()});
 dialog.addEventListener("close",()=>{dialog.remove();checkUpdates.pending=false},{once:true});
 function show(title,body,confirm=false){
  dialog.innerHTML=`<h2>${esc(title)}</h2>${body}<div class="confirm-dialog-actions"><button type="button" class="secondary" data-cancel>${confirm?"取消":"确定"}</button>${confirm?'<button type="button" data-update>更新</button>':''}</div>`;
  dialog.querySelector("[data-cancel]").onclick=()=>dialog.close();
 }
 show("正在检测更新…","");
 document.body.append(dialog);dialog.showModal();
 try{
  const info=await api("/System/Updates","GET",undefined,{signal:AbortSignal.timeout(15000)});
  if(!dialog.isConnected)return;
  const current=`<p>当前版本：${esc(info.CurrentVersion)}</p>`;
  if(!info.VersionKnown){show("当前版本无法识别",current+`<p>最新版本：${esc(info.LatestVersion)}</p><p>是否立即更新？</p>`,true)}
  else if(!info.UpdateAvailable){show("已是最新版本",current);return}
  else {show("发现新版本",current+`<p>最新版本：${esc(info.LatestVersion)}</p><p>是否立即更新？</p>`,true)}
  dialog.querySelector("[data-update]").onclick=async()=>{
   if(updating)return;
   updating=true;
   dialog.querySelector("h2").textContent="正在更新…";
   dialog.querySelectorAll("button").forEach(b=>b.disabled=true);
   try{
    const task=await api("/System/Updates","POST",undefined,{signal:AbortSignal.timeout(20000)});
    await waitForReleaseUpdate(task.TargetVersion);
    updating=false;
    show("更新成功",`<p>当前版本：${esc(task.TargetVersion)}</p>`);
    dialog.querySelector("[data-cancel]").onclick=()=>location.reload();
   }catch(e){
    updating=false;
    const reason=e.status===403?"无权限，仅管理员可以更新":e.name==="TimeoutError"?"请求超时，请检查服务器状态":e.message;
    show("更新失败",`<p>${esc(reason)}</p>`);
   }
  };
 }catch(e){if(dialog.isConnected)show("检测更新失败",`<p>${esc(e.name==="TimeoutError"?"请求超时，请稍后重试":e.message)}</p>`)}
}

async function waitForReleaseUpdate(target){
 const deadline=Date.now()+180000;
 while(Date.now()<deadline){
  const remaining=deadline-Date.now();
  let status;
  try{
   const health=await fetch("/health",{cache:"no-store",signal:AbortSignal.timeout(Math.min(5000,remaining))});
   if(health.ok)status=await api("/System/Updates/Status","GET",undefined,{signal:AbortSignal.timeout(Math.max(1,Math.min(5000,deadline-Date.now())))});
  }catch(e){if(e.status===401||e.status===403)throw new Error("登录已失效，请重新登录确认更新结果")}
  if(status?.State==="failed")throw new Error(status.Message||"宿主机更新任务失败");
  // Health alone can still belong to the old container. Require the target version.
  if(status?.CurrentVersion===target)return;
  await new Promise(resolve=>setTimeout(resolve,Math.max(0,Math.min(2000,deadline-Date.now()))));
 }
 throw new Error("更新超时（3 分钟），请管理员检查服务状态");
}
