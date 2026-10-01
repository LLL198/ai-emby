const $ = (s) => document.querySelector(s),
  esc = (s) => String(s ?? "").replace(/[&<>"']/g,(c)=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"})[c]);
const savedDeviceSession=DeviceSession.read();
let token=savedDeviceSession.token,user=savedDeviceSession.user,device=DeviceSession.deviceID(),rememberedLogin=savedDeviceSession.persistent,sessionEpoch=0,page=0,parent="",term="",view="browse",heartbeat,current;
function saveCurrentSession(){return DeviceSession.save(token,user,rememberedLogin)}
function clearCurrentSession(notifyTabs=true){
 sessionEpoch++;
 stop().catch(()=>{});
 closeLogs();
 document.querySelectorAll("dialog[open]").forEach(dialog=>dialog.close());
 token="";user=null;rememberedLogin=false;
 avatarForUser();
 if(notifyTabs)DeviceSession.clear();
}
function handleExpiredSession(requestToken){
 if(!requestToken||requestToken!==token)return;
 clearCurrentSession();
 login();
}
async function rememberCurrentDevice(){
 const requestToken=token,epoch=sessionEpoch;
 const result=await api("/web/session/persist","POST",{DeviceId:device},{signal:AbortSignal.timeout(20000)});
 if(requestToken!==token||epoch!==sessionEpoch)return;
 if(result.Persistent!==true||!result.User?.Id)throw new Error("未能保存长期登录，请稍后重新打开页面");
 user=result.User;rememberedLogin=true;
 if(!saveCurrentSession())throw new Error("当前浏览器无法保存长期登录，请允许此网站保存本地数据");
}
async function startRememberedSession(){
 if(!token||!user){login();return}
 const requestToken=token,epoch=sessionEpoch;
 saveCurrentSession();
 nav();
 try{await rememberCurrentDevice()}catch(error){if(token===requestToken&&epoch===sessionEpoch)toast(error.message,{type:"error"})}
 if(token===requestToken&&epoch===sessionEpoch){nav();routeFromLocation();loadServerName()}
}
window.addEventListener("storage",event=>{
 if(event.key!==DeviceSession.key&&event.key!==null)return;
 const next=event.key===null?{token:"",user:null,persistent:false}:DeviceSession.decode(event.newValue);
 if(next.token===token){
  if(token){user=next.user;rememberedLogin=next.persistent;nav()}
  return;
 }
 clearCurrentSession(false);
 token=next.token;user=next.user;rememberedLogin=next.persistent;
 startRememberedSession();
});
function toast(title, options){ return UI.Toast(title, options); }
function run(fn){return async(...args)=>{try{await fn(...args)}catch(e){toast(e.message,{type:"error"})}}}
const buildVersion = "__AI_EMBY_VERSION__";
function renderBuildFooter(){
 return `<div class="build-footer">${UI.githubRevisionLink(buildVersion)}</div>`;
}
function themeIcon(){return document.documentElement.dataset.theme==="light"?'<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.5 1.5M17.6 17.6l1.5 1.5M2 12h2M20 12h2M4.9 19.1l1.5-1.5M17.6 6.4l1.5-1.5"/></svg>':'<svg viewBox="0 0 24 24"><path d="M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9Z"/></svg>'}
const appearanceThemeKey="go-emby-azure-theme";
function toggleTheme(){const r=document.documentElement;r.dataset.theme=r.dataset.theme==="light"?"dark":"light";localStorage.setItem(appearanceThemeKey,r.dataset.theme);nav()}
function initTheme(){const r=document.documentElement;r.dataset.theme=localStorage.getItem(appearanceThemeKey)||"light"}
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
function navigateAdminSection(n, folderID = ''){
 return run(async()=>{
  if(!user?.Policy?.IsAdministrator)return;
  closeLogs();
  if(view==="admin"&&document.querySelectorAll(".admin-section").length){
   if(n===0)mediaPage(false,!folderID);else adminSection(n);
   if(n===0&&folderID)folderPage(folderID);
  }else await admin(n, folderID);
 })();
}
function drawerIcon(name) {
 const paths={dashboard:'M3 13a9 9 0 0 1 18 0M12 13l4-4M5 17h14M8 20h8',back:'M15 5 8 12l7 7M8 12h13',media:'M4 4h16v16H4zM8 8h8M8 12h8M8 16h5',files:'M3 7h7l2 2h9v11H3z',scraper:'M12 3l2 7 7 2-7 2-2 7-2-7-7-2 7-2z',users:'M8 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM2 21v-2a6 6 0 0 1 12 0v2M17 12a4 4 0 0 0-1-8M17 15a5 5 0 0 1 5 5',sort:'M5 5h14M5 12h10M5 19h6',info:'M12 11v7M12 6v1M3 12a9 9 0 1 0 18 0 9 9 0 0 0-18 0',settings:'M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1M9 12a3 3 0 1 0 6 0 3 3 0 0 0-6 0',sub:'M4 15h16M7 10h10M10 5h4',chapter:'M4 5h16M4 12h16M4 19h16M9 3v4M15 10v4M9 17v4',network:'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18ZM3 12h18M12 3c-4 4-4 14 0 18M12 3c4 4 4 14 0 18',api:'M8 8l-4 4 4 4M16 8l4 4-4 4M14 5l-4 14',refresh:'M20 11a8 8 0 1 0-2 6M20 4v7h-7',lock:'M5 11h14v10H5zM8 11V7a4 4 0 0 1 8 0v4',logout:'M10 4H4v16h6M14 7l5 5-5 5M9 12h10'};
 return `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="${paths[name]}"/></svg>`;
}
function renderAdminDrawer(){ return Panel.drawer(); }
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
  {label:'播放偏好',action:()=>Features.preferences(),icon:'chapter'},
  {label:'修改密码',action:password,icon:'lock'},
  {label:'退出登录',action:logout,icon:'logout',danger:true}
 ];
 let sheet;
 sheet=UI.ActionSheet('账户',entries.map(x=>({...x,icon:drawerIcon(x.icon),action:()=>{sheet.close();run(x.action)()}})),null,{
  variant:'account-menu',profile:{name:user?.Name||'用户',server:serverName||'AI Emby',version:buildVersion,avatar:avatarForUser(),loadAvatar,uploadAvatar:async file=>{await api(`/Users/${encodeURIComponent(profileUserId)}/Avatar`,'POST',file,{raw:true});if(user?.Id===profileUserId)setAvatar(file)}}
 });
}

function nav(){
 Panel.applyTheme();
 if(view!=="admin")stopConsolePolling();
 if(renderedView!==view){$("#app")?.replaceChildren();renderedView=view; if(view!=="browse" && typeof resumeMenuAbort!=="undefined") { resumeMenuAbort.abort(); resumeMenuAbort=new AbortController(); }}
 syncMediaHeader();
 if(view!=="login")stopLoginMark();
 document.body.classList.toggle("media-view",view==="browse");document.body.classList.toggle("admin-view",view==="admin");toggleDrawer(false);const n=$("#nav");if(!n)return;
 n.innerHTML=`${token&&user?.Policy?.IsAdministrator?`<div class="workspace-switch" aria-label="切换工作空间"><button type="button" aria-pressed="${view!=="admin"&&view!=="files"}" onclick="browseRoot()">影库</button><button type="button" aria-pressed="${view==="admin"||view==="files"}" onclick="navigateAdminSection(12)">管理面板</button></div>`:""}${token&&view==="admin"?'<button class="secondary icon-button" title="实时日志" aria-label="实时日志" onclick="showLogs()"><svg viewBox="0 0 24 24"><path d="M4 4h16v16H4zM8 8h8M8 12h8M8 16h5"/></svg></button>':""}${token?`${view!=="admin"&&view!=="files"?`<button class="secondary icon-button" aria-label="搜索" title="搜索" onclick="openSearch()">${UI.icons.search}</button>`:""}<details class="user-menu"><summary class="icon-button user-menu-trigger" onclick="openUserMenu(event)" title="${esc(user?.Name||"用户")}" aria-label="用户菜单"><svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 21v-2a8 8 0 0 1 16 0v2"/></svg></summary></details>`:""}<button class="secondary icon-button" title="切换明暗模式" aria-label="切换明暗模式" onclick="toggleTheme()">${themeIcon()}</button>`;
 paintTopAvatar(avatarForUser().objectURL);
 if(token&&view==='browse'){const links=UI.el('div',{class:'feature-actions',style:'margin:0'},[UI.el('button',{class:'secondary',onclick:run(()=>Features.discover())},'发现'),UI.el('button',{class:'secondary',onclick:run(()=>Features.collections())},'合集')]);n.prepend(links)}
 loadAvatar().catch(()=>{});
 $("#hamburger")?.remove();$("#drawer")?.remove();$("#drawer-backdrop")?.remove();
 if((view==="admin"||view==="files")&&user?.Policy?.IsAdministrator){document.querySelector("header").insertAdjacentHTML("afterbegin",'<button id="hamburger" aria-label="展开功能菜单" aria-expanded="false"><span></span><span></span><span></span></button>');const drawer=renderAdminDrawer(view);document.body.insertAdjacentHTML("beforeend",drawer);$("#drawer").addEventListener("click",e=>{if(e.target.closest("button")){toggleDrawer(false)}});$("#hamburger").onclick=()=>{toggleDrawer($("#hamburger").getAttribute("aria-expanded")!=="true")}}
 Panel.syncNavigation();
}
function serverDisplayName(value){const name=typeof value==="string"?value.trim():"";return !name||/^(?:go[ -]?emby|maca)$/i.test(name)?"AI Emby":name}
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
function login(){
 closeLogs();view="login";nav();
 $("#app").innerHTML=`<section class="login-page"><div class="login-card"><div class="login-mark" role="img" aria-label="${esc(serverName)}"></div><form id="login" class="login-form"><input name="username" autocomplete="username" placeholder="用户名" aria-label="用户名" required><input name="pw" type="password" autocomplete="current-password" placeholder="密码" aria-label="密码"><button class="login-submit" aria-label="登录">登录</button><p class="login-device-note">登录后自动记住当前设备</p></form></div></section>`;
 renderLoginMark();loadServerName();
 $("#login").onsubmit=run(async(e)=>{
  e.preventDefault();
  const form=e.target,button=form.querySelector("button"),epoch=sessionEpoch;
  if(button.disabled)return;
  button.disabled=true;
  try{
   const f=new FormData(form),b=await api.login(f.get("username"),f.get("pw"));
   if(epoch!==sessionEpoch)return;
   token=b.AccessToken;user=b.User;rememberedLogin=false;
   saveCurrentSession();
   try{await rememberCurrentDevice()}catch(error){if(token===b.AccessToken)toast(error.message,{type:"error"})}
   if(token===b.AccessToken&&epoch===sessionEpoch){nav();routeFromLocation()}
  }finally{button.disabled=false}
 });
}
async function logout(){
 const departingToken=token;
 clearCurrentSession();login();
 try{await api("/Sessions/Logout","POST",{},{authToken:departingToken,signal:AbortSignal.timeout(10000)})}catch{}
}
function password(){view="password";nav();$("#app").innerHTML=`<div class="panel login"><h2>修改密码</h2><form id="pw" style="display:grid;gap:15px"><input name="old" type="password" placeholder="当前密码"><input name="new" type="password" placeholder="新密码（普通用户可留空，管理员至少 12 字节）"><button>保存并重新登录</button></form></div>`;$("#pw").onsubmit=run(async(e)=>{e.preventDefault();const f=new FormData(e.target);await api(`/Users/${user.Id}/Password`,"POST",{CurrentPw:f.get("old"),NewPw:f.get("new")});await logout();toast("密码已修改")})}
function routeFromLocation(){
 if(!token||!user)return;
 Features.applyAppearance().catch(()=>{});
 if(location.hash.startsWith('#item/')){const id=decodeURIComponent(location.hash.slice(6));browseRoot().then(()=>detail(id)).catch(e=>toast(e.message,{type:'error'}));return}
 if(location.hash.startsWith("#discover")){Features.discover();return}
 if(location.hash.startsWith("#collections")){Features.collections(location.hash.split('/')[1]||'');return}
 if(location.hash.startsWith("#files")&&user.Policy?.IsAdministrator){filesPage();return}
 if(location.hash.startsWith("#admin")&&user.Policy?.IsAdministrator){navigateAdminSection(Panel.sectionFromHash(),Panel.folderFromHash());return}
 browseRoot();
}
window.addEventListener("hashchange",routeFromLocation);
document.addEventListener("DOMContentLoaded",()=>{window.addEventListener("unhandledrejection",(e)=>{toast(e.reason?.message||"请求失败",{type:"error"});e.preventDefault()});startRememberedSession()});

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
  const info=await api("/System/Updates","GET",undefined,{signal:AbortSignal.timeout(30000)});
  if(!dialog.isConnected)return;
  const current=`<p>当前版本：${esc(info.CurrentVersion)}</p><p>更新源：${esc(info.Repository)}</p>`;
  if(!info.VersionKnown){show("当前版本无法识别",current+`<p>最新版本：${esc(info.LatestVersion)}</p><p>是否立即更新？</p>`,true)}
  else if(!info.UpdateAvailable){show("已是最新版本",current);return}
  else {show("发现新版本",current+`<p>最新版本：${esc(info.LatestVersion)}</p><p>是否立即更新？</p>`,true)}
  if(!info.Enabled){dialog.querySelector("[data-update]").disabled=true;dialog.querySelector(".confirm-dialog-actions").before(UI.el("p",{},"宿主机更新服务未启动，请先安装更新服务"));return}
  dialog.querySelector("[data-update]").onclick=async()=>{
   if(updating)return;
   updating=true;
   dialog.querySelector("h2").textContent="正在更新…";
   dialog.querySelectorAll("button").forEach(b=>b.disabled=true);
   try{
    const task=await api("/System/Updates","POST",undefined,{signal:AbortSignal.timeout(30000)});
    await waitForReleaseUpdate(task.TargetVersion,status=>{if(dialog.isConnected)dialog.querySelector("h2").textContent=status.Message||"正在更新…"},task.RequestedAt);
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

async function waitForReleaseUpdate(target,onProgress,requestedAt){
 const deadline=Date.now()+900000;
 while(Date.now()<deadline){
  const remaining=deadline-Date.now();
  let status;
  try{
   const health=await fetch("/health",{cache:"no-store",signal:AbortSignal.timeout(Math.min(5000,remaining))});
   if(health.ok)status=await api("/System/Updates/Status","GET",undefined,{signal:AbortSignal.timeout(Math.max(1,Math.min(5000,deadline-Date.now())))});
  }catch(e){if(e.status===401||e.status===403)throw new Error("登录已失效，请重新登录确认更新结果")}
  const currentTask=status?.TargetVersion===target&&Date.parse(status.Updated)>=Date.parse(requestedAt);
  if(status?.State==="failed"&&currentTask)throw new Error(status.Message||"宿主机更新任务失败");
  if(currentTask)onProgress?.(status);
  // Health alone can still belong to the old container. Require the target version.
  if(status?.CurrentVersion===target)return;
  await new Promise(resolve=>setTimeout(resolve,Math.max(0,Math.min(2000,deadline-Date.now()))));
 }
 throw new Error("页面等待已超过 15 分钟，后台任务可能仍在执行，请重新打开系统页查看状态");
}
