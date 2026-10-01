const FilesPage = (() => {
  const state = { path: "", entries: [], selected: new Set(), sort: "name", order: "asc", search: "", scope: "current", busy: false, request: 0, error: "" };
  const icon = (dir) => dir ? '<svg viewBox="0 0 24 24"><path d="M3 6.5h6l2 2h10v10.5H3z"/></svg>' : '<svg viewBox="0 0 24 24"><path d="M6 2h8l4 4v16H6zM14 2v5h5"/></svg>';
  const parentPath = (p) => !p ? "" : p.split("/").filter(Boolean).slice(0,-1).join("/");
  const join = (a,b) => [a,b].filter(Boolean).join("/");
  const size = (n) => { if (!n) return "—"; const units=["B","KB","MB","GB","TB"]; let i=0,v=n; while(v>=1024&&i<units.length-1){v/=1024;i++} return `${v.toFixed(i?1:0)} ${units[i]}`; };
  const authHeaders = () => ({"X-Emby-Token":token,"X-Emby-Authorization":`Emby Client="AI Emby Web", Device="Browser", DeviceId="${device}", Version="1.0"`});
  async function request(path, options={}) { const requestToken=token; const res=await fetch(path,{...options,headers:{...authHeaders(),...(options.headers||{})}}); if(!res.ok){let b;try{b=await res.json()}catch{};if(res.status===401&&b?.error!=="license_required")handleExpiredSession(requestToken);const e=new Error(b?.Message||b?.message||b?.error||`HTTP ${res.status}`);e.status=res.status;throw e} const ct=res.headers.get("content-type")||""; return ct.includes("json")?res.json():res; }
  function setRoute(replace=false){const hash="#files"+(state.path?"/"+state.path.split("/").map(encodeURIComponent).join("/"):"");history[replace?"replaceState":"pushState"]({page:"files",path:state.path},"",hash)}
  function routePath(){if(!location.hash.startsWith("#files"))return "";return location.hash.slice(6).split("/").filter(Boolean).map(x=>decodeURIComponent(x)).join("/")}
  async function load(path=state.path,push=false){const sequence=++state.request;state.path=path.replace(/^\/+|\/+$/g,"");state.selected.clear();state.error="";state.entries=[];state.busy=true;render();try{const q=new URLSearchParams({path:state.path,sort:state.sort,order:state.order,scope:state.scope});if(state.search)q.set("search",state.search);const b=await request("/api/files?"+q);if(sequence!==state.request)return;state.entries=b.entries||[];if(push&&view==="files")setRoute()}catch(error){if(sequence===state.request)state.error=error.message;throw error}finally{if(sequence===state.request){state.busy=false;render()}}}
  function toolbar(){
    const bar=UI.el("div",{class:"files-toolbar"});
    const trail=UI.el("nav",{class:"file-breadcrumbs","aria-label":"当前文件路径"});
    const home=UI.el("button",{class:"secondary",onclick:run(()=>load("",true))},"媒体文件");trail.append(home);
    const segments=state.path.split("/").filter(Boolean);segments.forEach((name,i)=>{trail.append(UI.el("span",{"aria-hidden":"true"},"/"),UI.el("button",{class:"secondary",onclick:run(()=>load(segments.slice(0,i+1).join("/"),true)),...(i===segments.length-1?{"aria-current":"location"}:{})},name))});
    const search=UI.el("input",{type:"search",class:"files-search",placeholder:"搜索当前目录","aria-label":"搜索文件",value:state.search});search.onchange=run(async()=>{state.search=search.value.trim();await load(state.path)});
    const sort=UI.el("select",{class:"files-sort","aria-label":"文件排序"},[UI.el("option",{value:"name"},"按名称"),UI.el("option",{value:"time"},"按时间"),UI.el("option",{value:"size"},"按大小")]);sort.value=state.sort;sort.onchange=run(async()=>{state.sort=sort.value;await load(state.path)});
    const order=UI.el("button",{class:"secondary",onclick:run(async()=>{state.order=state.order==="asc"?"desc":"asc";await load(state.path)})},state.order==="asc"?"升序 ↑":"降序 ↓");
    bar.append(trail,search,sort,order);return bar;
  }
  function row(item){const checked=state.selected.has(item.path);const checkbox=UI.el("input",{type:"checkbox","aria-label":`选择 ${item.name}`});checkbox.checked=checked;checkbox.onchange=()=>{checkbox.checked?state.selected.add(item.path):state.selected.delete(item.path);renderActions()};const name=UI.el("button",{class:"files-name",onclick:run(()=>item.isDir?load(item.path,true):download(item))});name.innerHTML=`<span class="file-icon">${icon(item.isDir)}</span><span><strong>${esc(item.name)}</strong><small>${item.isDir?"文件夹":size(item.size)}</small></span>`;const time=UI.el("time",{},new Date(item.modified).toLocaleString());const more=actions(item);return UI.el("div",{class:"files-row"+(checked?" selected":"")},[checkbox,name,time,more])}
  function renderActions(){const n=document.querySelector("#files-actions");if(!n)return;const count=state.selected.size;n.replaceChildren(UI.el("button",{class:"secondary",onclick:()=>pickUpload()},"上传"),UI.el("button",{class:"secondary",onclick:run(mkdir)},"新建文件夹"),...(count?[UI.el("button",{class:"secondary",onclick:run(moveSelected)},`移动 (${count})`),UI.el("button",{class:"danger",onclick:run(deleteSelected)},`删除 (${count})`)]:[]))}
  function render(){if(view!=="files")return;const app=$("#app");if(!app)return;const list=UI.el("section",{class:"files-list","aria-busy":state.busy});if(state.busy)list.append(UI.el("p",{class:"empty"},"正在读取…"));else if(state.error)list.append(UI.el("p",{class:"empty",role:"alert"},"目录读取失败："+state.error));else if(!state.entries.length)list.append(UI.el("p",{class:"empty"},state.search?"没有匹配的文件":"这个文件夹是空的"));else state.entries.forEach(x=>list.append(row(x)));app.replaceChildren(UI.el("section",{class:"files-page"},[UI.el("div",{class:"section-heading files-heading"},[UI.el("div",{},[UI.el("p",{class:"eyebrow"},"CONTENT / AI EMBY"),UI.el("h1",{},"文件管理")]),UI.el("div",{id:"files-actions",class:"files-actions"})]),toolbar(),list]));renderActions()}
  function promptDialog(title,label,value=""){return new Promise(resolve=>{const input=UI.el("input",{value,placeholder:label,autocomplete:"off"});const form=UI.el("form",{class:"files-dialog-form"},[input,UI.el("div",{class:"files-dialog-buttons"},[UI.el("button",{type:"button",class:"secondary"},"取消"),UI.el("button",{type:"submit"},"确定")])]);const d=UI.Modal(title,form);const buttons=form.querySelectorAll("button");buttons[0].onclick=()=>{d.close();resolve(null)};form.onsubmit=e=>{e.preventDefault();const v=input.value.trim();if(v){d.close();resolve(v)}};input.focus()})}
  async function mkdir(){const name=await promptDialog("新建文件夹","文件夹名称");if(!name)return;await api("/api/files/mkdir","POST",{path:state.path,name});toast("文件夹已创建");await load()}
  async function rename(item){const name=await promptDialog("重命名文件夹","新名称",item.name);if(!name||name===item.name)return;await api("/api/files/rename","POST",{OldPath:item.path,NewPath:"/"+join(parentPath(item.path),name)});toast("已重命名");await load()}
  async function remove(paths){if(!await confirmDialog("删除文件",`确定删除 ${paths.length} 个项目？此操作不可撤销。`,"删除"))return;await api("/api/files","DELETE",{Paths:paths});toast("已删除");await load()}
  async function deleteSelected(){await remove([...state.selected])}
  async function move(paths){const target=await promptDialog("移动到","目标目录，例如 Movies","/");if(target===null)return;await api("/api/files/move","POST",{paths,target:target.replace(/^\//,"")});toast("移动完成");await load()}
  async function moveSelected(){await move([...state.selected])}
  async function copyDirectory(item) {
    const path = item.directoryPath;
    if (!path) throw new Error("目录路径不可用，请刷新");
    try { await navigator.clipboard.writeText(path); toast("目录路径已复制"); return; } catch {}
    const input = UI.el("textarea", {class:"files-copy-fallback", "aria-label":"目录路径"});
    input.value = path; document.body.append(input); input.select();
    let copied = false;
    try { copied = document.execCommand('copy'); } catch {} finally { input.remove(); }
    if (copied) toast("目录路径已复制");
    else { window.prompt("自动复制失败，请手动复制目录路径", path); toast("请手动复制目录路径"); }
  }
  function actions(item) {
    const menu = UI.el("details", {class:"fm-menu fm-menu--files"});
    const summary = UI.el("summary", {class:"icon-button", "aria-label":`更多操作 ${item.name}`, title:"更多操作"});
    summary.innerHTML = fmIcon("more");
    const pop = UI.el("div", {class:"fm-pop"});
    const add = (label, icon, action, danger=false) => {
      const button = UI.el("button", {type:"button", class:danger?"danger":"", onclick:run(async()=>{menu.open=false;await action();})});
      button.innerHTML = fmIcon(icon) + esc(label); pop.append(button);
    };
    if (!item.isDir) add("下载", "download", ()=>download(item));
    if (item.isDir) add("重命名", "rename", ()=>rename(item));
    add("复制目录路径", "copy", ()=>copyDirectory(item));
    if (item.scrape) add("刮削", "scan", ()=>openFileScraper(item));
    add("移动", "folder", ()=>move([item.path]));
    add("删除", "trash", ()=>remove([item.path]), true);
    menu.append(summary,pop);
    menu.addEventListener('keydown', e=>{if(e.key==='Escape'){menu.open=false;summary.focus();}});
    return menu;
  }
  async function download(item){const res=await request("/api/files/download?"+new URLSearchParams({path:item.path}));const blob=await res.blob();const url=URL.createObjectURL(blob);const a=document.createElement("a");a.href=url;a.download=item.name;document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),1000)}
  function pickUpload(){const input=document.createElement("input");input.type="file";input.multiple=true;input.onchange=run(async()=>{for(const f of input.files){toast(`正在上传 ${f.name}`);await request("/api/files/upload?"+new URLSearchParams({path:state.path}),{method:"POST",headers:{"X-File-Name":f.name,"Content-Type":"application/octet-stream"},body:f})}toast("上传完成");await load()});input.click()}
  async function open(path){if(!user?.Policy?.IsAdministrator){toast("文件管理仅限管理员");return browseRoot()}const replace=location.hash.startsWith("#files");closeLogs();view="files";nav();document.title="文件管理 · "+serverName;document.body.dataset.panelSection="files";state.search="";await load(path??routePath(),false);if(view==="files")setRoute(replace)}
  return {open,load,routePath};
})();
function filesPage(path){return run(()=>FilesPage.open(path))()}
