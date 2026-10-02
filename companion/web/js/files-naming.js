const FileNaming = (() => {
  const endpoint = "/admin/features/naming/";
  const labels = {ready:"可改名", unchanged:"已规范", review:"待确认", conflict:"有冲突", renamed:"已改名"};
  const presets = {
    tv:"{title} - S{season:2}E{episode:2}",
    movie:"{title} ({year})"
  };
  function field(label,input,wide=false) {
    return UI.el("label",{class:"naming-field"+(wide?" naming-wide":"")},[UI.el("span",{},label),input]);
  }
  function select(label,options) {
    return UI.el("select",{"aria-label":label},options.map(([value,text])=>UI.el("option",{value},text)));
  }
  function mount(container, options={}) {
    const {path="",paths=[],refresh=()=>{},useScraperScopes=false,onBeforeRun=()=>{},onComplete=()=>{},onBusy=()=>{},onError=()=>{},label=()=>"自动命名",initialMode="auto"}=options;
    let externalBusy=false,reportedBusy=false,reportedApplying=false;
    let plan = null, busy = false, requestNumber = 0, applying = false, tmdb = "", scopePath=path, scopePaths=[...paths], visibleRows=200, previewAbort=null;
    const chosen = new Set();
    const wrap = UI.el("div",{class:"naming-content"});
    const form = UI.el("form",{class:"naming-form"});
    const scope = UI.el("div",{class:"naming-scope"},[
      UI.el("span",{},"/media"+(path?"/"+path:"")),
      UI.el("small",{},paths.length?`已选择 ${paths.length} 项` : "当前文件夹及子目录")
    ]);
    const folderButton=UI.el("button",{type:"button",class:"secondary"},"选择文件夹");
    scope.append(folderButton);
    const mode = select("命名方式",[["auto","自动标准命名"],["regex","正则替换"],["sequence","顺序编号"]]);
    mode.value=initialMode;
    const kind = select("作品类型",[["auto","自动判断"],["tv","剧集 / 动漫 / 综艺"],["movie","电影"],["directory","仅文件夹"]]);
    const title = UI.el("input",{type:"text","aria-label":"作品名称",placeholder:"留空则从文件和目录识别",autocomplete:"off",maxlength:160});
    const year = UI.el("input",{type:"number","aria-label":"发行年份",placeholder:"可选",min:1800,max:2199});
    const season = UI.el("input",{type:"number","aria-label":"季号",placeholder:"从季目录识别；不确定时填写",min:0,max:999});
    const bare = UI.el("input",{type:"checkbox"});
    const autoTMDB = UI.el("input",{type:"checkbox",checked:true});
    const recursive = UI.el("input",{type:"checkbox",checked:true});
    const folders = UI.el("input",{type:"checkbox",checked:true});
    const template = UI.el("input",{type:"text","aria-label":"命名模板",placeholder:"留空使用推荐格式，自动保留扩展名",maxlength:512,autocomplete:"off"});
    const pattern = UI.el("input",{type:"text","aria-label":"匹配正则",placeholder:"例如 EP(\\d+)",autocomplete:"off",maxlength:512});
    const replacement = UI.el("input",{type:"text","aria-label":"替换内容",placeholder:"例如 E${1}；留空表示删除匹配",autocomplete:"off",maxlength:512});
    const start = UI.el("input",{type:"number","aria-label":"起始集号",value:1,min:1,max:9999});
    const width = select("集号补位",[["2","2 位（01）"],["3","3 位（001）"],["4","4 位（0001）"],["1","不补位"]]);
    const basic = UI.el("div",{class:"naming-grid"},[field("命名方式",mode),field("作品类型",kind)]);
    const metadata = UI.el("div",{class:"naming-grid naming-metadata"},[
      field("作品名称",title,true),field("发行年份",year),field("季号",season)
    ]);
    const tmdbLabel = UI.el("span",{class:"naming-tmdb-label"});
    const searchButton = UI.el("button",{type:"button",class:"secondary"},"手动选作品");
    const clearButton = UI.el("button",{type:"button",class:"secondary",hidden:true},"取消作品绑定");
    const searchbar = UI.el("div",{class:"naming-searchbar"},[searchButton,clearButton,tmdbLabel]);
    const candidates = UI.el("div",{class:"naming-candidates"});
    const templateArea = UI.el("div",{class:"naming-grid"},[field("命名模板",template,true)]);
    const presetArea = UI.el("div",{class:"naming-presets"},[
      UI.el("button",{type:"button",class:"secondary",onclick:()=>{template.value="";invalidate();}},"推荐格式"),
      UI.el("button",{type:"button",class:"secondary",onclick:()=>{template.value=presets.tv;invalidate();}},"剧集格式"),
      UI.el("button",{type:"button",class:"secondary",onclick:()=>{template.value=presets.movie;invalidate();}},"电影格式")
    ]);
    const variableHelp = UI.el("details",{class:"naming-variable-help"},[
      UI.el("summary",{},"模板变量与示例"),
      UI.el("p",{},"{title} 标题 · {title_original} TMDB 原名 · {year} 年份 · {season:2} 季号 · {episode:2} 集号 · {episode_name} 原文件分集标题 · {quality} 清晰度 · {tmdbid} TMDB 编号。扩展名会自动保留。"),
      UI.el("code",{},"{title} ({year}) - S{season:2}E{episode:2} {tmdb-{tmdbid}}"),
      UI.el("p",{},"正则使用 RE2 语法，替换分组写 ${1} 或 $1，不支持环视。顺序编号按文件名自然排序：1、2、10。")
    ]);
    const option = UI.el("label",{class:"naming-option"},[bare,UI.el("span",{},"确认裸集号是本季集号（例如 01、02；非动漫绝对集数）")]);
    const tmdbOption = UI.el("label",{class:"naming-option"},[autoTMDB,UI.el("span",{},"自动搜索 TMDB，按作品名称、年份和类型匹配")]);
    const scopeOptions=UI.el("div",{class:"naming-scope-options"},[
      UI.el("label",{class:"naming-option"},[recursive,UI.el("span",{},"包含全部子文件夹")]),
      UI.el("label",{class:"naming-option"},[folders,UI.el("span",{},"同时规范作品和季目录")])
    ]);
    const regexArea = UI.el("div",{class:"naming-grid",hidden:true},[field("匹配正则",pattern,true),field("替换内容",replacement,true)]);
    const sequenceArea = UI.el("div",{class:"naming-grid",hidden:true},[field("起始集号",start),field("集号补位",width)]);
    const sequenceNote = UI.el("p",{class:"naming-note",hidden:true},"顺序编号会重排集号，请先确认作品、季号和文件顺序。只给选中的媒体文件编号。");
    const notice = UI.el("p",{class:"naming-note"},"自动命名会直接执行可靠匹配，不确定的项目跳过。关联文件随同改名，STRM 播放链接保留。");
    const previewButton = UI.el("button",{type:"submit",class:"secondary"},"生成预览");
    const autoButton = UI.el("button",{type:"submit"},"自动命名");
    const feedback = UI.el("p",{class:"naming-feedback",role:"status","aria-live":"polite"});
    const results = UI.el("section",{class:"naming-results","aria-label":"改名预览"},UI.el("p",{class:"empty"},"生成预览后，在这里确认改名内容。"));
    const applyButton = UI.el("button",{type:"button",disabled:true},"执行改名");
    const closeButton = UI.el("button",{type:"button",class:"secondary"},"关闭");
    const actions = UI.el("div",{class:"naming-actions"},[previewButton,closeButton,applyButton,autoButton]);
    if(useScraperScopes) {
      mode.querySelector('[value="sequence"]').remove();
      const singleSeason=UI.el("button",{type:"button",class:"secondary naming-single-season",onclick:()=>mount(null,{initialMode:"sequence",onBeforeRun,refresh})},"单季顺序编号");
      const advanced=UI.el("details",{class:"naming-advanced"},[UI.el("summary",{},"高级命名规则"),basic,metadata,searchbar,candidates,regexArea,templateArea,presetArea,variableHelp,tmdbOption,option,singleSeason]);
      recursive.checked=true;
      form.append(UI.el("label",{class:"naming-option"},[folders,UI.el("span",{},"同时规范作品和季目录")]),advanced,notice,feedback,results,actions);
      closeButton.remove();
    } else form.append(scope,scopeOptions,basic,metadata,searchbar,candidates,regexArea,sequenceArea,sequenceNote,templateArea,presetArea,variableHelp,tmdbOption,option,notice,feedback,results,actions);
    wrap.append(form);
    const dialog = container?null:UI.Modal("规范命名",wrap);
    if(container){container.replaceChildren(wrap);wrap.classList.add('naming-inline');}
    else dialog.classList.add("naming-sheet");
    const headingClose = dialog?.querySelector('.section-heading button');
    const alive=()=>dialog?dialog.open:wrap.isConnected&&(options.alive?.()??true);
    function updateButtons() {
      const locked=busy||externalBusy;
      previewButton.disabled=locked;
      autoButton.disabled=locked;
      autoButton.textContent=busy&&mode.value==="auto"?"正在处理…":label();
      applyButton.disabled=locked||!plan||!chosen.size;
      applyButton.hidden=!plan;
      applyButton.textContent=applying?"正在改名…":`${options.applyLabel?.()||"执行改名"}${chosen.size?` (${chosen.size})`:""}`;
      closeButton.disabled=applying; if(headingClose)headingClose.disabled=applying;
      form.querySelectorAll("input,select").forEach(input=>input.disabled=locked||(input.closest('.naming-results')&&(!plan||input.dataset.namingReady!=="true")));
      searchButton.disabled=locked||mode.value==="regex";
      presetArea.querySelectorAll("button").forEach(button=>button.disabled=locked);
      form.querySelectorAll('.naming-single-season').forEach(button=>button.disabled=locked);
      clearButton.disabled=locked;
      folderButton.disabled=locked;
      recursive.disabled=locked||mode.value==="sequence";
      folders.disabled=locked||!recursive.checked||mode.value==="sequence";
      results.querySelectorAll('.naming-more').forEach(button=>button.disabled=applying);
      if(reportedBusy!==busy||reportedApplying!==applying){reportedBusy=busy;reportedApplying=applying;onBusy(busy);}
    }
    function updateScope() {
      scope.querySelector("span").textContent="/media"+(scopePath?"/"+scopePath:"");
      scope.querySelector("small").textContent=scopePaths.length?`已选择 ${scopePaths.length} 项${recursive.checked?" · 包含子目录":""}`:recursive.checked?"当前文件夹及全部子目录":"仅当前目录";
      updateButtons();
    }
    folderButton.onclick=()=>{
      if(busy)return;
      let current=scopePath, loading=0;
      const breadcrumb=UI.el("div",{class:"naming-folder-path"});
      const list=UI.el("div",{class:"naming-folder-list"});
      const choose=UI.el("button",{type:"button"},"选择这个文件夹");
      const cancel=UI.el("button",{type:"button",class:"secondary"},"取消");
      const picker=UI.Modal("选择命名文件夹",UI.el("div",{class:"naming-folder-picker"},[breadcrumb,list,UI.el("div",{class:"naming-picker-actions"},[cancel,choose])]));
      picker.classList.add("naming-folder-dialog");
      cancel.onclick=()=>picker.close();
      choose.onclick=()=>{scopePath=current;scopePaths=[];updateScope();invalidate();picker.close();};
      async function loadFolder(next) {
        const request=++loading;choose.disabled=true;
        list.replaceChildren(UI.el("p",{class:"empty"},"正在读取文件夹…"));
        breadcrumb.replaceChildren(UI.el("button",{type:"button",class:"secondary",onclick:()=>loadFolder("")},"媒体目录"));
        const parts=next.split("/").filter(Boolean);
        parts.forEach((name,i)=>breadcrumb.append(UI.el("span",{},"/"),UI.el("button",{type:"button",class:"secondary",onclick:()=>loadFolder(parts.slice(0,i+1).join("/"))},name)));
        try {
          const data=await api("/api/files?"+new URLSearchParams({path:next,sort:"name",order:"asc"}));
          if(request!==loading||!picker.open)return;
          current=next;list.replaceChildren();
          for(const item of data.entries||[])if(item.isDir)list.append(UI.el("button",{type:"button",class:"secondary naming-folder-item",onclick:()=>loadFolder(item.path.replace(/^\/+/,""))},[UI.el("span",{},item.name),UI.el("span",{"aria-hidden":"true"},"›")]));
          if(!list.children.length)list.append(UI.el("p",{class:"empty"},"没有子文件夹，可以选择当前文件夹。"));
          choose.disabled=false;
        } catch(error) {if(request===loading)list.replaceChildren(UI.el("p",{role:"alert"},error.message));}
      }
      loadFolder(current);
    };
    function progressMonitor(key, request) {
      let timer=null, stopped=false;
      async function poll() {
        try {
          const value=await api(endpoint+"progress?"+new URLSearchParams({ID:key}));
          if(stopped||request!==requestNumber||!alive())return;
          if(busy&&!['完成','预览完成','已停止'].includes(value.phase)) {
            const count=value.total?`${value.done}/${value.total}`:`已发现 ${value.done||0} 项`;
            feedback.textContent=`${value.phase} · ${count}${value.current?" · "+value.current:""}`;
          }
        } catch {}
        if(!stopped&&request===requestNumber&&alive())timer=setTimeout(poll,2000);
      }
      poll();
      return ()=>{stopped=true;clearTimeout(timer);};
    }
    function invalidate() {
      requestNumber++; plan=null;chosen.clear();feedback.textContent="";
      results.replaceChildren(UI.el("p",{class:"empty"},"参数已调整，请重新生成预览。"));
      updateButtons();
    }
    function modeChanged() {
      const regex=mode.value==="regex",sequence=mode.value==="sequence";
      metadata.hidden=regex;searchbar.hidden=regex;candidates.hidden=regex;
      regexArea.hidden=!regex;sequenceArea.hidden=!sequence;sequenceNote.hidden=!sequence;
      templateArea.hidden=regex;presetArea.hidden=regex;option.hidden=regex||sequence;
      tmdbOption.hidden=regex;autoButton.hidden=regex||sequence;
      scopeOptions.hidden=sequence;
      notice.textContent=regex||sequence?"关联字幕、NFO 和缩略图随同改名，STRM 播放链接保留。":"自动命名会直接执行可靠匹配，不确定的项目跳过。关联文件随同改名，STRM 播放链接保留。";
      if(sequence)kind.value="tv";
      updateScope();
      invalidate();
    }
    form.addEventListener("input",e=>{
      if(e.target===title||e.target===year) { tmdb=""; tmdbLabel.textContent=""; clearButton.hidden=true; candidates.replaceChildren(); }
      if(!e.target.closest('.naming-results')){if(e.target===recursive)updateScope();invalidate();}
    });
    mode.onchange=modeChanged;
    kind.onchange=()=>{tmdb="";tmdbLabel.textContent="";clearButton.hidden=true;candidates.replaceChildren();invalidate();};
    clearButton.onclick=()=>{tmdb="";tmdbLabel.textContent="";clearButton.hidden=true;invalidate();};
    closeButton.onclick=()=>dialog?.close();
    dialog?.addEventListener("cancel",e=>{if(applying)e.preventDefault();});
    dialog?.addEventListener("close",()=>{requestNumber++;previewAbort?.abort();});
    searchButton.onclick=async()=>{
      if(busy)return;
      if(!["tv","movie"].includes(kind.value)) { feedback.textContent="请先选择电影或剧集类型。";return; }
      if(!title.value.trim()) { feedback.textContent="先在作品名称里输入标题或 TMDB ID。";title.focus();return; }
      busy=true;updateButtons();feedback.textContent="正在搜索 TMDB…";
      const sequence=++requestNumber;
      try {
        const data=await api("/admin/features/identify?"+new URLSearchParams({Type:kind.value,Query:title.value.trim()}));
        if(sequence!==requestNumber||!alive())return;
        candidates.replaceChildren();
        for(const item of (data.Items||[]).slice(0,12)) {
          const button=UI.el("button",{type:"button",class:"naming-candidate"});
          if(item.Poster)button.append(UI.el("img",{src:item.Poster,alt:"",loading:"lazy",referrerpolicy:"no-referrer"}));
          button.append(UI.el("span",{},[UI.el("strong",{},item.Name),UI.el("small",{},`${item.Date?.slice(0,4)||"年份未知"} · TMDB ${item.ID}`)]));
          button.onclick=()=>{
            title.value=item.Name;year.value=item.Date?.slice(0,4)||"";tmdb=String(item.ID);
            tmdbLabel.textContent="TMDB "+tmdb;clearButton.hidden=false;candidates.replaceChildren();invalidate();
          };
          candidates.append(button);
        }
        feedback.textContent=data.Items?.length?"请选择作品，再生成改名预览。":"没有找到结果，可以修改标题或输入 TMDB ID 再试。";
      } catch(error) { if(sequence===requestNumber){feedback.textContent=error.name==='AbortError'?'已停止准备工作':error.message;onError(error);} }
      finally {busy=false;updateButtons();}
    };
    function renderRows(displayPlan=plan) {
      const ready=displayPlan.rows.filter(row=>row.status==="ready");
      const renamed=displayPlan.rows.filter(row=>row.status==="renamed").length;
      const summary=UI.el("div",{class:"naming-summary"});
      const selectAll=UI.el("input",{type:"checkbox","aria-label":"选择全部可改名项目"});
      selectAll.checked=ready.length>0&&ready.every(row=>chosen.has(row.id));
      selectAll.dataset.namingReady=String(ready.length>0);
      selectAll.disabled=busy||!plan||ready.length===0;
      selectAll.onchange=()=>{chosen.clear();if(selectAll.checked)ready.forEach(row=>chosen.add(row.id));renderRows();updateButtons();};
      summary.append(UI.el("label",{},[selectAll,UI.el("span",{},renamed?`已改名 ${renamed} / ${displayPlan.rows.length}`:`可改名 ${ready.length} / ${displayPlan.rows.length}`)]),UI.el("small",{},`扫描 ${displayPlan.directories||1} 个目录 · 待确认或冲突 ${displayPlan.rows.filter(row=>["review","conflict"].includes(row.status)).length}`));
      const table=UI.el("table",{class:"naming-table"});
      table.append(UI.el("thead",{},UI.el("tr",{},[UI.el("th",{},"选择"),UI.el("th",{},"原名称"),UI.el("th",{},"新名称"),UI.el("th",{},"识别依据")] )));
      const tbody=UI.el("tbody");
      const root=displayPlan.root&&displayPlan.root!=="."?displayPlan.root+"/":"";
      const displayPath=value=>value?.startsWith(root)?value.slice(root.length):value||"—";
      for(const row of displayPlan.rows.slice(0,visibleRows)) {
        const check=UI.el("input",{type:"checkbox","aria-label":"改名 "+row.old.split("/").at(-1)});
        check.dataset.namingReady=String(row.status==="ready");
        check.checked=chosen.has(row.id);check.disabled=busy||!plan||row.status!=="ready";
        check.onchange=()=>{check.checked?chosen.add(row.id):chosen.delete(row.id);selectAll.checked=ready.length>0&&ready.every(x=>chosen.has(x.id));updateButtons();};
        const detail=UI.el("td",{},[UI.el("span",{class:"naming-badge naming-badge--"+(row.status==="renamed"?"unchanged":row.status)},labels[row.status]),UI.el("small",{},row.reason)]);
        if(row.moves?.length>1) {
          const related=UI.el("details",{},[UI.el("summary",{},`关联文件 ${row.moves.length-1}`)]);
          for(const move of row.moves.slice(1))related.append(UI.el("p",{},move.old.split("/").at(-1)+" → "+move.new.split("/").at(-1)));
          detail.append(related);
        }
        tbody.append(UI.el("tr",{class:"naming-row--"+row.status},[
          UI.el("td",{},check),UI.el("td",{},displayPath(row.old)),UI.el("td",{},displayPath(row.new)),detail
        ]));
      }
      table.append(tbody);
      results.replaceChildren(summary,UI.el("div",{class:"naming-table-scroll"},table));
      if(displayPlan.rows.length>visibleRows)results.append(UI.el("button",{type:"button",class:"secondary naming-more",onclick:()=>{visibleRows+=200;renderRows(displayPlan);}},`显示更多（${Math.min(visibleRows,displayPlan.rows.length)}/${displayPlan.rows.length}）`));
      if(!displayPlan.rows.length)results.append(UI.el("p",{class:"empty"},"当前范围没有可处理的媒体文件或文件夹。"));
    }
    form.onsubmit=async e=>{
      e.preventDefault();if(busy||externalBusy)return;
      const automatic=e.submitter===autoButton||!e.submitter&&mode.value==="auto";
      if(mode.value==="sequence"&&(!season.value||!title.value.trim())) {feedback.textContent="顺序编号需要填写作品名称和季号（特别篇填写 0）。";return;}
      busy=true;plan=null;chosen.clear();visibleRows=200;updateButtons();feedback.textContent=recursive.checked&&mode.value!=="sequence"?"正在扫描文件夹及全部子目录…":autoTMDB.checked?"正在自动匹配 TMDB 并检查名称…":"正在识别名称并检查冲突…";
      const sequence=++requestNumber;
      const progress=crypto.randomUUID?.()||"naming-"+Date.now().toString(36)+"-"+Math.random().toString(36).slice(2);
      previewAbort=new AbortController();
      const stopProgress=progressMonitor(progress,sequence);
      try {
        await onBeforeRun();
        const data=await api(endpoint+"preview","POST",{path:scopePath,paths:scopePaths,useScraperScopes,mode:mode.value,kind:kind.value,title:title.value.trim(),year:year.value?Number(year.value):0,tmdb,autoTMDB:autoTMDB.checked,recursive:recursive.checked&&mode.value!=="sequence",folders:folders.checked,progress,season:season.value?Number(season.value):null,bare:bare.checked,template:template.value.trim(),pattern:pattern.value,replacement:replacement.value,start:Number(start.value),width:Number(width.value)},{signal:previewAbort.signal});
        if(sequence!==requestNumber||!alive())return;
        plan=data;data.rows.filter(row=>row.status==="ready").forEach(row=>chosen.add(row.id));
        renderRows();
        if(automatic&&chosen.size){stopProgress();await applyPlan(true);}
        else {
          feedback.textContent=automatic?"处理完成：已规范的项目保持原名，其余跳过原因见下方。":"预览已生成，检查新名称后执行。";
          if(automatic){plan=null;await onComplete({renamed:0,scopeVersion:data.scopeVersion});}
        }
      } catch(error) { if(sequence===requestNumber){feedback.textContent=error.name==='AbortError'?'已停止准备工作':error.message;onError(error);} }
      finally {stopProgress();previewAbort=null;busy=false;updateButtons();}
    };
    async function applyPlan(automatic=false) {
      if(!plan||!chosen.size)return;
      const selectedPlan=plan,selected=[...chosen];
      const selectedIDs=new Set(selected);
      busy=true;applying=true;updateButtons();feedback.textContent="正在同步改名并迁移媒体记录…";
      const stopProgress=progressMonitor(selectedPlan.progress,requestNumber);
      try {
        await onBeforeRun();
        const data=await api(endpoint+"apply","POST",{id:selectedPlan.id,rows:selected});
        plan=null;chosen.clear();
        feedback.textContent=`已改名 ${data.renamed} 项，包含关联文件共 ${data.files} 个。媒体库正在刷新。${data.journalWarning?"改名记录完成状态未能更新，准备记录仍可用于恢复。":""}`;
        if(automatic) {
          selectedPlan.rows.forEach(row=>{if(selectedIDs.has(row.id))row.status="renamed";if(data.destinations?.[row.id])row.new=data.destinations[row.id];});
          renderRows(selectedPlan);
          feedback.textContent+=` 已规范 ${selectedPlan.rows.filter(row=>row.status==="unchanged").length} 项，跳过 ${selectedPlan.rows.filter(row=>["review","conflict"].includes(row.status)).length} 项。`;
        } else results.replaceChildren(UI.el("p",{class:"empty"},"本批改名已完成。"));
        toast(`已改名 ${data.renamed} 项`);
        await refresh();
        applying=false;updateButtons();
        await onComplete(data);
      } catch(error) { feedback.textContent=error.message;plan=null;chosen.clear();onError(error); }
      finally {stopProgress();busy=false;applying=false;updateButtons();}
    }
    applyButton.onclick=()=>{if(!busy)applyPlan();};
    modeChanged();
    return {invalidate, setBusy(value){externalBusy=value;updateButtons();}, cancel(){if(applying)return false;previewAbort?.abort();return true;}, get applying(){return applying;}};
  }
  return {open:(path,paths=[],refresh=()=>{})=>mount(null,{path,paths,refresh}),mount};
})();
