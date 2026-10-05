const FilesPage = (() => {
  const state = { root: "media", roots: [], docker: false, directoryPath: "", path: "", entries: [], selected: new Set(), sort: "name", order: "asc", search: "", scope: "current", busy: false, request: 0, error: "" };
  const icon = (dir) => dir ? '<svg viewBox="0 0 24 24"><path d="M3 6.5h6l2 2h10v10.5H3z"/></svg>' : '<svg viewBox="0 0 24 24"><path d="M6 2h8l4 4v16H6zM14 2v5h5"/></svg>';
  const parentPath = (p) => !p ? "" : p.split("/").filter(Boolean).slice(0,-1).join("/");
  const join = (a,b) => [a,b].filter(Boolean).join("/");
  const size = (n) => { if (!n) return "—"; const units=["B","KB","MB","GB","TB"]; let i=0,v=n; while(v>=1024&&i<units.length-1){v/=1024;i++} return `${v.toFixed(i?1:0)} ${units[i]}`; };
  const authHeaders = () => ({"X-Emby-Token":token,"X-Emby-Authorization":`Emby Client="AI Emby Web", Device="Browser", DeviceId="${device}", Version="1.0"`});
  async function request(path, options={}, raw=false) { const requestToken=token; const res=await fetch(path,{...options,headers:{...authHeaders(),...(options.headers||{})}}); if(!res.ok){let b;try{b=await res.json()}catch{};if(res.status===401)handleExpiredSession(requestToken);const e=new Error(b?.Message||b?.message||b?.error||`HTTP ${res.status}`);e.status=res.status;throw e} if(raw)return res;const ct=res.headers.get("content-type")||""; return ct.includes("json")?res.json():res; }
  const currentRoot = () => state.roots.find(root => root.id === state.root);
  const readOnly = () => !!currentRoot()?.readOnly;
  const endpoint = (suffix = "", query = {}, root = state.root) => "/admin/features/local-files" + suffix + "?" + new URLSearchParams({...query, root});
  async function mutate(suffix, method, data, root) { return request(endpoint(suffix,{},root), {method, headers:{"Content-Type":"application/json"}, body:JSON.stringify(data)}); }
  async function loadRoots() { const result=await request('/admin/features/file-roots');state.roots=result.roots||[];state.docker=!!result.docker; }
  async function switchRoot(key) { state.root=key;state.search="";state.directoryPath="";await load("",true); }
  function rootBar() {
    const select=UI.el('select', {'aria-label':'选择本地路径',class:'files-root-select'});
    for (const root of state.roots) select.append(UI.el('option',{value:root.id},root.name+(root.readOnly?' · 只读':'')+(!root.available?' · 不可用':'')));
    select.value=state.root;select.onchange=run(()=>switchRoot(select.value));
    const root=currentRoot();
    const path=state.directoryPath||root?.hostPath||root?.path||'';
    return UI.el('div',{class:'files-root-bar'},[
      UI.el('div',{class:'files-root-location'},[select,UI.el('span',{class:'files-root-path',title:path},path)]),
      UI.el('div',{class:'files-root-buttons'},[
        UI.el('button',{class:'secondary',onclick:()=>editRoot()},'添加路径'),
        UI.el('button',{class:'secondary',onclick:run(manageRoots)},'管理路径')
      ])
    ]);
  }
  function editRoot(root) {
    const name=UI.el('input',{value:root?.name||'',placeholder:'例如 OpenList',maxlength:160});
    const path=UI.el('input',{value:root?.hostPath||root?.path||'',placeholder:state.docker?'E:\\123\\openlist 或 /movies':'/mnt/media',autocomplete:'off'});
    const readonly=UI.el('input',{type:'checkbox'});readonly.checked=!!root?.readOnly;
    const status=UI.el('p',{class:'files-dialog-status',role:'status'});
    const save=UI.el('button',{type:'submit'},'保存');
    const browse=UI.el('button',{type:'button',class:'secondary',onclick:()=>browseDirectory(path.value,value=>{path.value=value})},'选择目录');
    const cancel=UI.el('button',{type:'button',class:'secondary'},'取消');
    const form=UI.el('form',{class:'files-dialog-form'},[
      UI.el('label',{},['名称',name]),
      UI.el('label',{},['本地路径',UI.el('div',{class:'files-root-input'},[path,browse])]),
      UI.el('label',{class:'files-readonly'},[readonly,'只读']),
      ...(state.docker?[UI.el('p',{class:'files-dialog-hint'},'Docker 中的宿主目录需要先挂载，再添加路径。')]:[]),
      status,UI.el('div',{class:'files-dialog-buttons'},[cancel,save])
    ]);
    const dialog=UI.Modal(root?'编辑路径':'添加本地路径',form);dialog.classList.add('files-root-dialog');
    cancel.onclick=()=>dialog.close();
    form.onsubmit=async event=>{
      event.preventDefault();if(save.disabled)return;
      if(!path.value.trim()){status.textContent='请填写目录路径';path.focus();return;}
      save.disabled=true;browse.disabled=true;status.textContent='正在保存…';
      try {
        const result=await request('/admin/features/file-roots',{method:root?'PUT':'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:root?.id,name:name.value.trim(),path:path.value.trim(),readOnly:readonly.checked})});
        await loadRoots();dialog.close();toast('路径已保存');await switchRoot(result.id);
      } catch(error) {status.textContent=error.message;} finally {save.disabled=false;browse.disabled=false;}
    };
    path.focus();
  }
  function browseDirectory(start,onSelect) {
    const input=UI.el('input',{value:start||'/',autocomplete:'off','aria-label':'浏览目录路径'});
    const list=UI.el('div',{class:'files-directory-list'});
    const status=UI.el('p',{class:'files-dialog-status',role:'status'});
    const choose=UI.el('button',{type:'button',disabled:true},'选择此目录');
    const up=UI.el('button',{type:'button',class:'secondary',disabled:true},'上一级');
    const go=UI.el('button',{type:'submit',class:'secondary'},'打开');
    const cancel=UI.el('button',{type:'button',class:'secondary'},'取消');
    const form=UI.el('form',{class:'files-dialog-form'},[
      UI.el('div',{class:'files-root-input'},[input,go]),
      UI.el('div',{class:'files-directory-heading'},[up,status]),list,
      UI.el('div',{class:'files-dialog-buttons'},[cancel,choose])
    ]);
    const dialog=UI.Modal('选择本地目录',form);dialog.classList.add('files-root-dialog');
    let current='',display='',sequence=0;
    const read=async value=>{
      const requestID=++sequence;choose.disabled=true;up.disabled=true;list.replaceChildren();status.textContent='正在读取…';
      try {
        const result=await request('/admin/features/file-roots/browse?'+new URLSearchParams({path:value||'/'}));
        if(!dialog.open||requestID!==sequence)return;
        current=result.path;display=result.displayPath;input.value=display;choose.disabled=false;up.disabled=current==='/';
        status.textContent=(result.directories||[]).length?'':'没有子目录';
        for(const directory of result.directories||[])list.append(UI.el('button',{type:'button',class:'secondary',onclick:()=>read(directory.path)},directory.name));
      }catch(error){if(dialog.open&&requestID===sequence)status.textContent=error.message;}
    };
    form.onsubmit=event=>{event.preventDefault();read(input.value.trim());};
    up.onclick=()=>read(current.split('/').slice(0,-1).join('/')||'/');
    choose.onclick=()=>{onSelect(display);dialog.close();};cancel.onclick=()=>dialog.close();
    read(start||'/');
  }
  async function manageRoots() {
    await loadRoots();
    const list=UI.el('div',{class:'files-roots-list'});
    const dialog=UI.Modal('管理本地路径',list);dialog.classList.add('files-root-dialog');
    const draw=()=>{
      list.replaceChildren();
      for(const root of state.roots) {
        const buttons=UI.el('div',{class:'files-root-buttons'});
        if(!root.default)buttons.append(
          UI.el('button',{class:'secondary',onclick:()=>{dialog.close();editRoot(root);}},'编辑'),
          UI.el('button',{class:'danger',onclick:run(async()=>{
            if(!await confirmDialog('移除路径',`移除「${root.name}」的入口？目录和文件会保留。`,'移除'))return;
            await request('/admin/features/file-roots',{method:'DELETE',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:root.id})});
            await loadRoots();if(state.root===root.id)await switchRoot('media');else render();draw();toast('路径入口已移除');
          })},'移除')
        );
        list.append(UI.el('div',{class:'files-root-item'},[
          UI.el('div',{class:'files-root-info'},[UI.el('strong',{},root.name+(root.default?' · 默认':'')+(root.readOnly?' · 只读':'')+(!root.available?' · 不可用':'')),UI.el('span',{},root.hostPath||root.path)]),buttons
        ]));
      }
    };draw();
  }
  function setRoute(replace=false){const hash="#files"+(state.path?"/"+state.path.split("/").map(encodeURIComponent).join("/"):"")+(state.root!=="media"?"?"+new URLSearchParams({root:state.root}):"");history[replace?"replaceState":"pushState"]({page:"files",path:state.path,root:state.root},"",hash)}
  function routePath(){if(!location.hash.startsWith("#files"))return "";return location.hash.slice(6).split("?")[0].split("/").filter(Boolean).map(x=>decodeURIComponent(x)).join("/")}
  function routeRoot(){return new URLSearchParams(location.hash.split('?')[1]||'').get('root')||'media';}
  async function load(path=state.path,push=false){const sequence=++state.request;state.path=path.replace(/^\/+|\/+$/g,"");state.selected.clear();state.error="";state.entries=[];state.busy=true;if(push&&view==="files")setRoute();render();try{const q=new URLSearchParams({path:state.path,sort:state.sort,order:state.order,scope:state.scope});if(state.search)q.set("search",state.search);const b=await request(endpoint("",Object.fromEntries(q)));if(sequence!==state.request)return;state.entries=b.entries||[];state.directoryPath=b.directoryPath||"";}catch(error){if(sequence===state.request)state.error=error.message;throw error}finally{if(sequence===state.request){state.busy=false;render()}}}
  function toolbar(){
    const bar=UI.el("div",{class:"files-toolbar"});
    const trail=UI.el("nav",{class:"file-breadcrumbs","aria-label":"当前文件路径"});
    const home=UI.el("button",{class:"secondary",onclick:run(()=>load("",true))},currentRoot()?.name||"媒体目录");trail.append(home);
    const segments=state.path.split("/").filter(Boolean);segments.forEach((name,i)=>{trail.append(UI.el("span",{"aria-hidden":"true"},"/"),UI.el("button",{class:"secondary",onclick:run(()=>load(segments.slice(0,i+1).join("/"),true)),...(i===segments.length-1?{"aria-current":"location"}:{})},name))});
    const search=UI.el("input",{type:"search",class:"files-search",placeholder:"搜索当前目录","aria-label":"搜索文件",value:state.search});search.onchange=run(async()=>{state.search=search.value.trim();await load(state.path)});
    const sort=UI.el("select",{class:"files-sort","aria-label":"文件排序"},[UI.el("option",{value:"name"},"按名称"),UI.el("option",{value:"time"},"按时间"),UI.el("option",{value:"size"},"按大小")]);sort.value=state.sort;sort.onchange=run(async()=>{state.sort=sort.value;await load(state.path)});
    const order=UI.el("button",{class:"secondary",onclick:run(async()=>{state.order=state.order==="asc"?"desc":"asc";await load(state.path)})},state.order==="asc"?"升序 ↑":"降序 ↓");
    bar.append(trail,search,sort,order);return bar;
  }
  function row(item){const checked=state.selected.has(item.path);const checkbox=UI.el("input",{type:"checkbox","aria-label":`选择 ${item.name}`});checkbox.checked=checked;checkbox.onchange=()=>{checkbox.checked?state.selected.add(item.path):state.selected.delete(item.path);renderActions()};const name=UI.el("button",{class:"files-name",onclick:run(()=>item.isDir?load(item.path,true):download(item))});name.innerHTML=`<span class="file-icon">${icon(item.isDir)}</span><span><strong>${esc(item.name)}</strong><small>${item.isDir?"文件夹":size(item.size)}</small></span>`;const time=UI.el("time",{},new Date(item.modified).toLocaleString());const more=actions(item);return UI.el("div",{class:"files-row"+(checked?" selected":"")},[checkbox,name,time,more])}
  function renderActions() {
    const actions=document.querySelector("#files-actions");if(!actions)return;
    const count=state.selected.size;
    if(readOnly()){actions.replaceChildren(UI.el('span',{class:'files-readonly-badge'},'只读'));return;}
    actions.replaceChildren(
      UI.el("button",{class:"secondary",onclick:()=>pickUpload()},"上传"),
      UI.el("button",{class:"secondary",onclick:run(mkdir)},"新建文件夹"),
      ...(count?[
        UI.el("button",{class:"secondary",onclick:run(moveSelected)},`移动 (${count})`),
        UI.el("button",{class:"danger",onclick:run(deleteSelected)},`删除 (${count})`)
      ]:[])
    );
  }
  function render(){if(view!=="files")return;const app=$("#app");if(!app)return;const list=UI.el("section",{class:"files-list","aria-busy":state.busy});if(state.busy)list.append(UI.el("p",{class:"empty"},"正在读取…"));else if(state.error)list.append(UI.el("p",{class:"empty",role:"alert"},"目录读取失败："+state.error));else if(!state.entries.length)list.append(UI.el("p",{class:"empty"},state.search?"没有匹配的文件":"这个文件夹是空的"));else state.entries.forEach(x=>list.append(row(x)));app.replaceChildren(UI.el("section",{class:"files-page"},[UI.el("div",{class:"section-heading files-heading"},[UI.el("div",{},[UI.el("p",{class:"eyebrow"},"CONTENT / AI EMBY"),UI.el("h1",{},"文件管理")]),UI.el("div",{id:"files-actions",class:"files-actions"})]),rootBar(),toolbar(),list]));renderActions()}
  function promptDialog(title,label,value=""){return new Promise(resolve=>{const input=UI.el("input",{value,placeholder:label,autocomplete:"off"});const form=UI.el("form",{class:"files-dialog-form"},[input,UI.el("div",{class:"files-dialog-buttons"},[UI.el("button",{type:"button",class:"secondary"},"取消"),UI.el("button",{type:"submit"},"确定")])]);const d=UI.Modal(title,form);const buttons=form.querySelectorAll("button");buttons[0].onclick=()=>{d.close();resolve(null)};form.onsubmit=e=>{e.preventDefault();const v=input.value.trim();if(v){d.close();resolve(v)}};input.focus()})}
  async function mkdir(){const root=state.root,path=state.path;const name=await promptDialog("新建文件夹","文件夹名称");if(!name)return;await mutate("/mkdir","POST",{path,name},root);toast("文件夹已创建");if(state.root===root)await load()}
  async function rename(item){const root=item.root;const name=await promptDialog("重命名文件夹","新名称",item.name);if(!name||name===item.name)return;await mutate("/rename","POST",{OldPath:item.path,NewPath:"/"+join(parentPath(item.path),name)},root);toast("已重命名");if(state.root===root)await load()}
  async function remove(paths){const root=state.root;if(!await confirmDialog("删除文件",`确定删除 ${paths.length} 个项目？此操作不可撤销。`,"删除"))return;await mutate("","DELETE",{Paths:paths},root);toast("已删除");if(state.root===root)await load()}
  async function deleteSelected(){await remove([...state.selected])}
  async function move(paths){const root=state.root;const target=await promptDialog("移动到","目标目录，例如 Movies","/");if(target===null)return;await mutate("/move","POST",{paths,target:target.replace(/^\//,"")},root);toast("移动完成");if(state.root===root)await load()}
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
    if (item.isDir && !readOnly()) add("重命名", "rename", ()=>rename(item));
    add("复制目录路径", "copy", ()=>copyDirectory(item));
    if (item.scrape && !readOnly()) add("刮削", "scan", ()=>openFileScraper(item));
    if(!readOnly()){add("移动", "folder", ()=>move([item.path]));add("删除", "trash", ()=>remove([item.path]), true);}
    menu.append(summary,pop);
    menu.addEventListener('keydown', e=>{if(e.key==='Escape'){menu.open=false;summary.focus();}});
    return menu;
  }
  async function download(item){const res=await request(endpoint("/download",{path:item.path},item.root),{},true);const blob=await res.blob();const url=URL.createObjectURL(blob);const a=document.createElement("a");a.href=url;a.download=item.name;document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),1000)}
  function pickUpload(){const root=state.root,path=state.path;const input=document.createElement("input");input.type="file";input.multiple=true;input.onchange=run(async()=>{for(const f of input.files){toast(`正在上传 ${f.name}`);await request(endpoint("/upload",{path},root),{method:"POST",headers:{"X-File-Name":encodeURIComponent(f.name),"Content-Type":"application/octet-stream"},body:f})}toast("上传完成");if(state.root===root)await load()});input.click()}
  async function open(path){if(!user?.Policy?.IsAdministrator){toast("文件管理仅限管理员");return browseRoot()}const replace=location.hash.startsWith("#files");closeLogs();view="files";nav();document.title="文件管理 · "+serverName;document.body.dataset.panelSection="files";state.search="";state.directoryPath="";state.root=path!==undefined?"media":routeRoot();await loadRoots();if(!currentRoot()){state.root="media";path="";toast("路径入口已移除，已返回媒体目录");}await load(path??routePath(),false);if(view==="files")setRoute(replace)}
  return {open,load,routePath};
})();
function filesPage(path){return run(()=>FilesPage.open(path))()}
