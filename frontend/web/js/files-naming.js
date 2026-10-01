const FileNaming = (() => {
  const endpoint = "/admin/features/naming/";
  const labels = {ready:"可改名", unchanged:"已规范", review:"待确认", conflict:"有冲突"};
  const presets = {
    tv:"{title} - S{season:2}E{episode:2}",
    movie:"{title} ({year})",
    tmdb:"{title} ({year}) {tmdb-{tmdbid}}"
  };
  function field(label,input,wide=false) {
    return UI.el("label",{class:"naming-field"+(wide?" naming-wide":"")},[UI.el("span",{},label),input]);
  }
  function select(label,options) {
    return UI.el("select",{"aria-label":label},options.map(([value,text])=>UI.el("option",{value},text)));
  }
  function open(path,paths=[],refresh=()=>{}) {
    let plan = null, busy = false, requestNumber = 0, applying = false, tmdb = "";
    const chosen = new Set();
    const wrap = UI.el("div",{class:"naming-content"});
    const form = UI.el("form",{class:"naming-form"});
    const scope = UI.el("div",{class:"naming-scope"},[
      UI.el("span",{},"/media"+(path?"/"+path:"")),
      UI.el("small",{},paths.length?`已选择 ${paths.length} 项` : "当前目录 · 最多 500 项")
    ]);
    const mode = select("命名方式",[["auto","识别命名"],["regex","正则替换"],["sequence","顺序编号"]]);
    const kind = select("作品类型",[["auto","自动判断"],["tv","剧集 / 动漫 / 综艺"],["movie","电影"],["directory","仅文件夹"]]);
    const title = UI.el("input",{type:"text","aria-label":"作品名称",placeholder:"留空则从文件和目录识别",autocomplete:"off",maxlength:160});
    const year = UI.el("input",{type:"number","aria-label":"发行年份",placeholder:"可选",min:1800,max:2199});
    const season = UI.el("input",{type:"number","aria-label":"季号",placeholder:"从季目录识别；不确定时填写",min:0,max:999});
    const bare = UI.el("input",{type:"checkbox"});
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
    const searchButton = UI.el("button",{type:"button",class:"secondary"},"搜索 TMDB");
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
      UI.el("p",{},"{title} 标题 · {year} 年份 · {season:2} 季号 · {episode:2} 集号 · {quality} 清晰度 · {tmdbid} TMDB 编号。扩展名会自动保留。"),
      UI.el("code",{},"{title} ({year}) - S{season:2}E{episode:2} {tmdb-{tmdbid}}"),
      UI.el("p",{},"正则使用 RE2 语法，替换分组写 ${1} 或 $1，不支持环视。顺序编号按文件名自然排序：1、2、10。")
    ]);
    const option = UI.el("label",{class:"naming-option"},[bare,UI.el("span",{},"确认裸集号是本季集号（例如 01、02；非动漫绝对集数）")]);
    const regexArea = UI.el("div",{class:"naming-grid",hidden:true},[field("匹配正则",pattern,true),field("替换内容",replacement,true)]);
    const sequenceArea = UI.el("div",{class:"naming-grid",hidden:true},[field("起始集号",start),field("集号补位",width)]);
    const sequenceNote = UI.el("p",{class:"naming-note",hidden:true},"顺序编号会重排集号，请先确认作品、季号和文件顺序。只给选中的媒体文件编号。");
    const notice = UI.el("p",{class:"naming-note"},"仅修改本地名称，STRM 播放链接保留。关联字幕、NFO 和缩略图会随同改名。");
    const previewButton = UI.el("button",{type:"submit",class:"secondary"},"生成预览");
    const feedback = UI.el("p",{class:"naming-feedback",role:"status","aria-live":"polite"});
    const results = UI.el("section",{class:"naming-results","aria-label":"改名预览"},UI.el("p",{class:"empty"},"生成预览后，在这里确认改名内容。"));
    const applyButton = UI.el("button",{type:"button",disabled:true},"执行改名");
    const closeButton = UI.el("button",{type:"button",class:"secondary"},"关闭");
    const actions = UI.el("div",{class:"naming-actions"},[previewButton,closeButton,applyButton]);
    form.append(scope,basic,metadata,searchbar,candidates,regexArea,sequenceArea,sequenceNote,templateArea,presetArea,variableHelp,option,notice,feedback,results,actions);
    wrap.append(form);
    const dialog = UI.Modal("规范命名",wrap);
    dialog.classList.add("naming-sheet");
    const headingClose = dialog.querySelector('.section-heading button');
    function updateButtons() {
      previewButton.disabled=busy;
      applyButton.disabled=busy||!plan||!chosen.size;
      applyButton.textContent=applying?"正在改名…":`执行改名${chosen.size?` (${chosen.size})`:""}`;
      closeButton.disabled=applying; headingClose.disabled=applying;
      form.querySelectorAll("input,select").forEach(input=>input.disabled=applying);
      searchButton.disabled=busy||mode.value==="regex";
      presetArea.querySelectorAll("button").forEach(button=>button.disabled=applying);
      clearButton.disabled=busy;
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
      if(sequence)kind.value="tv";
      invalidate();
    }
    form.addEventListener("input",e=>{
      if(e.target===title||e.target===year) { tmdb=""; tmdbLabel.textContent=""; clearButton.hidden=true; candidates.replaceChildren(); }
      if(!e.target.closest('.naming-results'))invalidate();
    });
    mode.onchange=modeChanged;
    kind.onchange=()=>{tmdb="";tmdbLabel.textContent="";clearButton.hidden=true;candidates.replaceChildren();invalidate();};
    clearButton.onclick=()=>{tmdb="";tmdbLabel.textContent="";clearButton.hidden=true;invalidate();};
    closeButton.onclick=()=>dialog.close();
    dialog.addEventListener("cancel",e=>{if(applying)e.preventDefault();});
    dialog.addEventListener("close",()=>requestNumber++);
    searchButton.onclick=async()=>{
      if(busy)return;
      if(!["tv","movie"].includes(kind.value)) { feedback.textContent="请先选择电影或剧集类型。";return; }
      if(!title.value.trim()) { feedback.textContent="先在作品名称里输入标题或 TMDB ID。";title.focus();return; }
      busy=true;updateButtons();feedback.textContent="正在搜索 TMDB…";
      const sequence=++requestNumber;
      try {
        const data=await api("/admin/features/identify?"+new URLSearchParams({Type:kind.value,Query:title.value.trim()}));
        if(sequence!==requestNumber||!dialog.open)return;
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
      } catch(error) { if(sequence===requestNumber)feedback.textContent=error.message; }
      finally {busy=false;updateButtons();}
    };
    function renderRows() {
      const ready=plan.rows.filter(row=>row.status==="ready");
      const summary=UI.el("div",{class:"naming-summary"});
      const selectAll=UI.el("input",{type:"checkbox","aria-label":"选择全部可改名项目"});
      selectAll.checked=ready.length>0&&ready.every(row=>chosen.has(row.id));
      selectAll.disabled=ready.length===0;
      selectAll.onchange=()=>{chosen.clear();if(selectAll.checked)ready.forEach(row=>chosen.add(row.id));renderRows();updateButtons();};
      summary.append(UI.el("label",{},[selectAll,UI.el("span",{},`可改名 ${ready.length} / ${plan.rows.length}`)]),UI.el("small",{},`待确认或冲突 ${plan.rows.filter(row=>["review","conflict"].includes(row.status)).length}`));
      const table=UI.el("table",{class:"naming-table"});
      table.append(UI.el("thead",{},UI.el("tr",{},[UI.el("th",{},"选择"),UI.el("th",{},"原名称"),UI.el("th",{},"新名称"),UI.el("th",{},"识别依据")] )));
      const tbody=UI.el("tbody");
      for(const row of plan.rows) {
        const check=UI.el("input",{type:"checkbox","aria-label":"改名 "+row.old.split("/").at(-1)});
        check.checked=chosen.has(row.id);check.disabled=row.status!=="ready";
        check.onchange=()=>{check.checked?chosen.add(row.id):chosen.delete(row.id);selectAll.checked=ready.length>0&&ready.every(x=>chosen.has(x.id));updateButtons();};
        const detail=UI.el("td",{},[UI.el("span",{class:"naming-badge naming-badge--"+row.status},labels[row.status]),UI.el("small",{},row.reason)]);
        if(row.moves?.length>1) {
          const related=UI.el("details",{},[UI.el("summary",{},`关联文件 ${row.moves.length-1}`)]);
          for(const move of row.moves.slice(1))related.append(UI.el("p",{},move.old.split("/").at(-1)+" → "+move.new.split("/").at(-1)));
          detail.append(related);
        }
        tbody.append(UI.el("tr",{class:"naming-row--"+row.status},[
          UI.el("td",{},check),UI.el("td",{},row.old.split("/").at(-1)),UI.el("td",{},row.new?.split("/").at(-1)||"—"),detail
        ]));
      }
      table.append(tbody);
      results.replaceChildren(summary,UI.el("div",{class:"naming-table-scroll"},table));
      if(!plan.rows.length)results.append(UI.el("p",{class:"empty"},"当前范围没有可处理的媒体文件或文件夹。"));
    }
    form.onsubmit=async e=>{
      e.preventDefault();if(busy)return;
      if(mode.value==="sequence"&&(!season.value||!title.value.trim())) {feedback.textContent="顺序编号需要填写作品名称和季号（特别篇填写 0）。";return;}
      busy=true;plan=null;chosen.clear();updateButtons();feedback.textContent="正在识别名称并检查冲突…";
      const sequence=++requestNumber;
      try {
        const data=await api(endpoint+"preview","POST",{path,paths,mode:mode.value,kind:kind.value,title:title.value.trim(),year:year.value?Number(year.value):0,tmdb,season:season.value?Number(season.value):null,bare:bare.checked,template:template.value.trim(),pattern:pattern.value,replacement:replacement.value,start:Number(start.value),width:Number(width.value)});
        if(sequence!==requestNumber||!dialog.open)return;
        plan=data;data.rows.filter(row=>row.status==="ready").forEach(row=>chosen.add(row.id));
        renderRows();feedback.textContent="预览已生成，检查新名称后执行。";
      } catch(error) { if(sequence===requestNumber)feedback.textContent=error.message; }
      finally {busy=false;updateButtons();}
    };
    applyButton.onclick=async()=>{
      if(busy||!plan||!chosen.size)return;
      const selectedPlan=plan,selected=[...chosen];
      busy=true;applying=true;updateButtons();feedback.textContent="正在同步改名并迁移媒体记录…";
      try {
        const data=await api(endpoint+"apply","POST",{id:selectedPlan.id,rows:selected});
        plan=null;chosen.clear();
        feedback.textContent=`已改名 ${data.renamed} 项，包含关联文件共 ${data.files} 个。媒体库正在刷新。${data.journalWarning?"改名记录完成状态未能更新，准备记录仍可用于恢复。":""}`;
        results.replaceChildren(UI.el("p",{class:"empty"},"本批改名已完成。"));
        toast(`已改名 ${data.renamed} 项`);
        await refresh();
      } catch(error) { feedback.textContent=error.message;plan=null;chosen.clear(); }
      finally {busy=false;applying=false;updateButtons();}
    };
    modeChanged();
  }
  return {open};
})();
