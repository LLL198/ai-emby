const Tracking = (() => {
  const base = '/admin/features/tracking';
  const stateNames = {idle:'等待搜索',running:'搜索中',complete:'搜索完成',error:'搜索失败',interrupted:'已中断'};
  const importStates={error:'入库失败',queued:'等待入库',transfer:'转存中',strm:'生成 STRM',scan:'扫描入库',rename:'标准命名',scrape:'刮削中',complete:'入库完成','waiting-resource':'等待合适资源',interrupted:'已中断'};
  const resourceStates = {new:'未读',seen:'已读',ignored:'已忽略'};
  let host, still, data, selected='', status='', cloud='', page=1, results, timer, generation=0, requestSerial=0, summarySerial=0;
  let section='subscriptions', subscriptionQuery='', subscriptionState='', subscriptionSaving=false;
  const checked = new Set();
  const date = value => value ? new Date(value * 1000).toLocaleString() : '尚未搜索';
  const field = (name,label,value='',type='text',extra='') => `<label>${esc(label)}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
  const words = value => String(value || '').split(/[,，\n]/).map(x=>x.trim()).filter(Boolean);
  const current = () => still?.() && host?.isConnected;
  const selectedSub = () => data.Subscriptions.find(s=>s.ID===selected);
  function stopPoll() {clearTimeout(timer);}
  function schedule() {
    stopPoll();
    if (!current()) return;
    timer=setTimeout(()=>{if(current())refresh(false).catch(error=>{if(current()){host.querySelector('[data-run-state]').textContent=error.message;schedule();}});},data.Busy?2500:30000);
  }
  async function load(target,isCurrent) {
    stopPoll();const serial=++generation;++summarySerial;host=target;still=isCurrent;results=null;
    const b=await api(base);if(serial!==generation||!isCurrent())return;data=b;
    if(selected&&!selectedSub())selected='';
    checked.clear();render();await resourceList();schedule();
  }
  function render() {
    host.classList.add('tracking-section');
    host.innerHTML=`<div class="tracking-toolbar"><div><h2>资源追新</h2><span data-run-state role="status">${data.Busy?'正在执行追新任务…':data.Settings.Enabled?'定时追新已开启':'定时追新未开启'}</span></div><div class="tracking-actions"><button type="button" class="secondary" data-refresh>刷新</button><button type="button" class="secondary" data-settings>盘搜设置</button><button type="button" data-add>＋ 添加订阅</button></div></div>
      ${!data.Settings.URL?'<div class="tracking-notice">连接 PanSou 后即可搜索网盘资源。<button type="button" class="secondary" data-config>配置服务</button></div>':''}
      <nav class="tracking-view-tabs section-tabs" role="tablist" aria-label="追新页面"><button type="button" role="tab" id="tracking-subscriptions-tab" data-tracking-view="subscriptions" aria-controls="tracking-subscriptions-pane">我的订阅 <span data-subscription-count>${data.Subscriptions.length}</span></button><button type="button" role="tab" id="tracking-resources-tab" data-tracking-view="resources" aria-controls="tracking-resources-pane">资源索引</button></nav>
      <section id="tracking-subscriptions-pane" role="tabpanel" aria-labelledby="tracking-subscriptions-tab" data-tracking-pane="subscriptions"><div data-subscription-summary class="tracking-subscription-summary"></div><div class="tracking-management-toolbar"><input type="search" data-subscription-search aria-label="搜索我的订阅" placeholder="搜索作品名称或关键词" value="${esc(subscriptionQuery)}"><select data-subscription-state aria-label="筛选订阅状态"><option value="">全部订阅</option><option value="enabled" ${subscriptionState==='enabled'?'selected':''}>正在追新</option><option value="paused" ${subscriptionState==='paused'?'selected':''}>已暂停</option><option value="import" ${subscriptionState==='import'?'selected':''}>自动入库</option></select><button type="button" class="secondary" data-manage-run-all ${data.Busy||!data.Settings.URL||!data.Subscriptions.some(s=>s.Enabled)?'disabled':''}>搜索全部订阅</button></div><div data-subscription-management class="tracking-subscription-management"></div></section>
      <section id="tracking-resources-pane" role="tabpanel" aria-labelledby="tracking-resources-tab" data-tracking-pane="resources"><div class="tracking-workspace"><aside class="tracking-subscriptions" aria-label="作品订阅"><div class="tracking-subscriptions-head"><h3>作品筛选 <span>${data.Subscriptions.length}</span></h3><button type="button" class="secondary" data-manage>管理订阅</button></div><div data-subscriptions></div></aside>
      <section class="tracking-results"><div data-selected></div><div class="tracking-filters"><nav aria-label="资源状态">${[['','全部'],['new','未读'],['seen','已读'],['ignored','忽略']].map(([key,label])=>`<button type="button" data-filter="${key}" aria-current="${status===key?'page':'false'}">${label}</button>`).join('')}</nav><label>网盘<select data-cloud aria-label="筛选网盘"><option value="">全部网盘</option>${Object.entries(data.CloudTypes).map(([key,label])=>`<option value="${key}" ${key===cloud?'selected':''}>${esc(label)}</option>`).join('')}</select></label></div>
      <div class="tracking-batch"><label><input type="checkbox" data-select-all aria-label="全选本页">全选本页</label><span data-selection>未选择</span><button type="button" class="secondary" data-batch="seen" disabled>标为已读</button><button type="button" class="secondary" data-batch="ignored" disabled>忽略</button></div><div data-resources aria-live="polite"></div><div data-pagination></div></section></div></section>`;
    renderManagement();renderSubscriptions();renderSelected();showSection(section);
    host.querySelector('[data-add]').onclick=()=>edit();host.querySelector('[data-settings]').onclick=settings;
    host.querySelector('[data-config]')?.addEventListener('click',settings);
    host.querySelector('[data-refresh]').onclick=run(()=>refresh(true));host.querySelector('[data-manage-run-all]').onclick=run(()=>search(''));
    host.querySelector('[data-manage]').onclick=()=>showSection('subscriptions');
    const viewButtons=[...host.querySelectorAll('[data-tracking-view]')];
    viewButtons.forEach((button,index)=>{
      button.onclick=()=>showSection(button.dataset.trackingView);
      button.onkeydown=event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const next=event.key==='Home'?0:event.key==='End'?viewButtons.length-1:(index+(event.key==='ArrowRight'?1:-1)+viewButtons.length)%viewButtons.length;showSection(viewButtons[next].dataset.trackingView);viewButtons[next].focus();};
    });
    host.querySelector('[data-subscription-search]').oninput=event=>{subscriptionQuery=event.target.value;renderManagement();};
    host.querySelector('[data-subscription-state]').onchange=event=>{subscriptionState=event.target.value;renderManagement();};
    host.querySelectorAll('[data-filter]').forEach(b=>b.onclick=run(async()=>{status=b.dataset.filter;page=1;checked.clear();host.querySelectorAll('[data-filter]').forEach(x=>x.setAttribute('aria-current',String(x===b?'page':'false')));await resourceList();}));
    host.querySelector('[data-cloud]').onchange=run(async e=>{cloud=e.target.value;page=1;checked.clear();await resourceList();});
    host.querySelector('[data-select-all]').onchange=e=>{checked.clear();if(e.target.checked)results.Items.forEach(x=>checked.add(x.ID));host.querySelectorAll('[data-resource-check]').forEach(x=>x.checked=e.target.checked);updateSelection();};
    host.querySelectorAll('[data-batch]').forEach(b=>b.onclick=run(()=>mark([...checked],b.dataset.batch)));
  }
  function showSection(next) {
    section=next;
    host.querySelectorAll('[data-tracking-pane]').forEach(pane=>{pane.hidden=pane.dataset.trackingPane!==section;});
    host.querySelectorAll('[data-tracking-view]').forEach(button=>{const active=button.dataset.trackingView===section;button.setAttribute('aria-selected',String(active));button.tabIndex=active?0:-1;});
  }
  async function viewSubscription(id) {
    selected=id;page=1;checked.clear();renderSubscriptions();renderSelected();showSection('resources');await resourceList();
  }
  async function changeSubscription(s,action) {
    if(subscriptionSaving||data.Busy)return;
    if(action==='delete'&&!(await confirmDialog('删除订阅',`删除「${s.Title}」及其资源索引？已转存的影视文件和 STRM 会保留。`,'删除订阅')))return;
    if(subscriptionSaving||!current())return;
    subscriptionSaving=true;renderManagement();renderSelected();
    try {
      if(action==='delete'){
        await api(base+'/subscription?ID='+encodeURIComponent(s.ID),'DELETE');
        if(selected===s.ID){selected='';page=1;checked.clear();}
        toast('订阅已删除');
      }else{
        await api(base+'/subscription','POST',{...s,Enabled:!s.Enabled});
        toast(s.Enabled?'订阅已暂停':'订阅已恢复');
      }
      if(current())await refresh(true);
    }finally{subscriptionSaving=false;if(current()){renderManagement();renderSelected();}}
  }
  function renderManagement() {
    const subscriptions=data.Subscriptions,locked=data.Busy||subscriptionSaving;
    host.querySelector('[data-subscription-count]').textContent=subscriptions.length;
    host.querySelector('[data-subscription-summary]').innerHTML=[['全部订阅',subscriptions.length],['正在追新',subscriptions.filter(s=>s.Enabled).length],['已暂停',subscriptions.filter(s=>!s.Enabled).length],['自动入库',subscriptions.filter(s=>s.AutoImport?.Enabled).length]].map(([label,count])=>`<div><span>${label}</span><strong>${count}</strong></div>`).join('');
    host.querySelector('[data-manage-run-all]').disabled=locked||!data.Settings.URL||!subscriptions.some(s=>s.Enabled);
    host.querySelector('[data-add]').disabled=locked;
    const query=subscriptionQuery.trim().toLocaleLowerCase();
    const filtered=subscriptions.filter(s=>(!query||`${s.Title} ${s.Year||''} ${s.Query}`.toLocaleLowerCase().includes(query))&&(!subscriptionState||subscriptionState==='enabled'&&s.Enabled||subscriptionState==='paused'&&!s.Enabled||subscriptionState==='import'&&s.AutoImport?.Enabled));
    const list=host.querySelector('[data-subscription-management]');
    list.innerHTML=filtered.length?filtered.map(s=>`<article class="tracking-subscription-card"><div class="tracking-subscription-card-head"><div><h3><button type="button" class="text-button" data-subscription-action="view" data-subscription-id="${esc(s.ID)}">${esc(s.Title)}${s.Year?` <span>${s.Year}</span>`:''}</button></h3><span class="tracking-subscription-state ${s.Enabled?'is-enabled':''}">${s.Enabled?'正在追新':'已暂停'}</span></div><div class="tracking-subscription-actions"><button type="button" class="secondary" data-subscription-action="view" data-subscription-id="${esc(s.ID)}">查看资源</button><button type="button" class="secondary" data-subscription-action="edit" data-subscription-id="${esc(s.ID)}" ${locked?'disabled':''}>编辑</button><button type="button" class="secondary" data-subscription-action="pause" data-subscription-id="${esc(s.ID)}" ${locked?'disabled':''}>${s.Enabled?'暂停':'恢复'}</button><button type="button" class="secondary tracking-delete-subscription" data-subscription-action="delete" data-subscription-id="${esc(s.ID)}" ${locked?'disabled':''}>删除</button></div></div><div class="tracking-subscription-info"><div><span>搜索设置</span><strong>${esc(s.Query||s.Title)}</strong><small>每 ${s.Minutes>=60&&s.Minutes%60===0?s.Minutes/60+' 小时':s.Minutes+' 分钟'} · ${({all:'全部来源',plugin:'搜索插件',tg:'Telegram 频道'})[s.Source]||'全部来源'}</small></div><div><span>搜索网盘</span><strong>${esc(s.CloudTypes?.length?s.CloudTypes.map(key=>data.CloudTypes[key]||key).join('、'):'全部网盘')}</strong><small>${s.AutoImport?.Enabled?'自动入库'+(s.ImportMount?' · '+esc(s.ImportMount):'')+(processingLabel(s.AutoImport)?' · '+processingLabel(s.AutoImport):''):'仅列出资源'}</small></div><div><span>已发现资源</span><strong>${s.ResourceCount} 条${s.NewCount?` <span class="tracking-unread">${s.NewCount} 条未读</span>`:''}</strong><small>${esc(stateNames[s.State]||s.State||'等待搜索')}</small></div></div><div class="tracking-subscription-card-footer"><span>上次搜索：${date(s.LastSearch)}</span>${data.Settings.Enabled&&s.Enabled?`<span>下次搜索：${date(s.NextSearch)}</span>`:''}${s.Error?`<details class="tracking-subscription-error"><summary>搜索失败详情</summary><p>${esc(s.Error)}</p></details>`:''}</div></article>`).join(''):`<div class="tracking-empty"><h3>${subscriptions.length?'没有符合条件的订阅':'还没有订阅作品'}</h3><p>${subscriptions.length?'尝试更换关键词或筛选条件。':'添加电影或剧集后，可在这里查看和管理。'}</p>${subscriptions.length?'':'<button type="button" data-management-add>添加订阅</button>'}</div>`;
    list.querySelector('[data-management-add]')?.addEventListener('click',()=>edit());
    list.querySelectorAll('[data-subscription-action]').forEach(button=>button.onclick=run(async()=>{
      const s=data.Subscriptions.find(sub=>sub.ID===button.dataset.subscriptionId);if(!s)return;
      const action=button.dataset.subscriptionAction;
      if(action==='view')await viewSubscription(s.ID);
      else if(action==='edit')await edit(s);
      else await changeSubscription(s,action);
    }));
  }
  function renderSubscriptions() {
    const unread=data.Subscriptions.reduce((n,s)=>n+s.NewCount,0);
    host.querySelector('[data-subscriptions]').innerHTML=`<button type="button" class="tracking-sub ${!selected?'is-active':''}" data-sub=""><span><strong>全部资源</strong><small>${data.Subscriptions.reduce((n,s)=>n+s.ResourceCount,0)} 条资源</small></span>${unread?`<span class="tracking-unread">${unread}</span>`:''}</button>${data.Subscriptions.map(s=>`<button type="button" class="tracking-sub ${s.ID===selected?'is-active':''}" data-sub="${s.ID}"><span><strong>${esc(s.Title)}${s.Year?` <small>${s.Year}</small>`:''}</strong><small class="${s.State==='error'?'tracking-error':''}">${s.Enabled?esc(stateNames[s.State]||s.State):'已暂停'}</small></span>${s.NewCount?`<span class="tracking-unread">${s.NewCount}</span>`:''}</button>`).join('')}${!data.Subscriptions.length?'<p class="tracking-empty-small">订阅电影或剧集，资源会集中显示在这里。</p>':''}`;
    host.querySelectorAll('[data-sub]').forEach(b=>b.onclick=run(()=>viewSubscription(b.dataset.sub)));
  }
  function renderSelected() {
    const s=selectedSub();
    host.querySelector('[data-selected]').innerHTML=s?`<div class="tracking-selected-head"><div><h3>${esc(s.Title)}${s.Year?` <span>${s.Year}</span>`:''}</h3><p>关键词：${esc(s.Query)} · 每 ${s.Minutes>=60?(s.Minutes/60)+' 小时':s.Minutes+' 分钟'}</p></div><div class="tracking-actions"><button type="button" class="secondary" data-edit ${data.Busy?'disabled':''}>编辑</button><button type="button" data-run ${data.Busy||!data.Settings.URL?'disabled':''}>立即搜索</button></div></div>${s.Owned?`<p class="tracking-owned">已入库：${esc(s.Owned)}</p>`:''}${s.AutoImport?.Enabled||s.Import?.ManualConfig?`<div class="tracking-import-status"><div><strong>${s.Import?.ManualConfig?'所选资源入库':'自动入库'}${s.ImportMount?' · '+esc(s.ImportMount):''}</strong><span>${esc(importStates[s.Import?.Stage]||'等待执行')}${s.Import?.Saved?' · 已转存 '+s.Import.Saved+' 个视频':''}${s.Import?.RenamePending?' · '+s.Import.RenamePending+' 项命名待处理':''}</span></div>${s.Import?.Error?`<p class="tracking-error" role="alert">${esc(s.Import.Error)}</p>`:''}${s.Import?.PendingTask?'<p>网盘任务待确认，下次执行会继续核对。</p>':''}<div class="tracking-actions"><button type="button" class="secondary" data-retry-import ${data.Busy?'disabled':''}>${s.Import?.ManualConfig?(s.Import?.Error?'重试入库':'再次入库'):s.Import?.Error?'重试入库':'检查更新并入库'}</button>${s.AutoImport.Enabled&&!s.Import?.ManualConfig&&(s.AutoImport.ResourceID||s.Import?.ResourceID)?`<button type="button" class="text-button" data-unpin ${data.Busy?'disabled':''}>重新自动选择分享</button>`:''}</div></div>`:''}<div class="tracking-search-meta"><span>上次：${date(s.LastSearch)}</span>${data.Settings.Enabled&&s.Enabled?`<span>下次：${date(s.NextSearch)}</span>`:''}<span>上次找到 ${s.LastCount} 条</span></div>${s.Error?`<p class="tracking-error" role="alert">${esc(s.Error)}</p>`:''}<div class="tracking-selected-actions"><button type="button" class="text-button" data-read-all ${!s.NewCount?'disabled':''}>本订阅全部标为已读</button><button type="button" class="text-button" data-pause ${data.Busy?'disabled':''}>${s.Enabled?'暂停订阅':'恢复订阅'}</button><button type="button" class="text-button" data-delete ${data.Busy?'disabled':''}>删除订阅</button></div>`:`<div class="tracking-selected-head"><div><h3>资源索引</h3><p>按作品和网盘筛选，选择合适的分享资源。</p></div></div>`;
    if(!s)return;
    host.querySelector('[data-edit]').onclick=()=>edit(s);host.querySelector('[data-retry-import]')?.addEventListener('click',run(()=>s.Import?.ManualConfig?importResource({ID:s.Import.ResourceID,Subscription:s.ID,Cloud:s.ImportCloud,Title:s.Title}):search(s.ID)));host.querySelector('[data-unpin]')?.addEventListener('click',run(async()=>{await api(base+'/import/resource','POST',{ID:s.ID,ResourceID:''});await refresh(true);}));host.querySelector('[data-run]').onclick=run(()=>search(s.ID));
    host.querySelector('[data-read-all]').onclick=run(async()=>{await api(base+'/resources','PUT',{Subscription:s.ID,Status:'seen',IDs:[]});await refresh(true);});
    host.querySelector('[data-pause]').onclick=run(()=>changeSubscription(s,'pause'));
    host.querySelector('[data-delete]').onclick=run(()=>changeSubscription(s,'delete'));
  }
  async function refresh(reset) {
    const viewGeneration=generation,serial=++summarySerial,b=await api(base);if(serial!==summarySerial||viewGeneration!==generation||!current())return;
    const changed=JSON.stringify(data.Subscriptions.map(s=>[s.ID,s.NewCount,s.ResourceCount,s.LastSearch,s.State,s.Import]))!==JSON.stringify(b.Subscriptions.map(s=>[s.ID,s.NewCount,s.ResourceCount,s.LastSearch,s.State,s.Import]));
    data=b;if(selected&&!selectedSub())selected='';if(reset)checked.clear();
    host.querySelector('[data-run-state]').textContent=data.Busy?'正在执行追新任务…':data.Settings.Enabled?'定时追新已开启':'定时追新未开启';
    renderManagement();renderSubscriptions();renderSelected();if(reset||changed||data.Busy||results==null)await resourceList();schedule();
  }
  async function resourceList() {
    const serial=++requestSerial,viewGeneration=generation;
    const response=await api.query(base+'/resources',{Subscription:selected,Status:status,Cloud:cloud,Page:page,Limit:30});
    if(serial!==requestSerial||viewGeneration!==generation||!current())return;results=response;
    if(page>1&&!results.Items.length){page--;await resourceList();return;}
    const ids=new Set(results.Items.map(x=>x.ID));for(const id of checked)if(!ids.has(id))checked.delete(id);
    host.querySelector('[data-resources]').innerHTML=results.Items.length?results.Items.map((r,i)=>`<article class="tracking-resource ${r.Status==='new'?'is-new':''}"><label class="tracking-resource-select"><input type="checkbox" data-resource-check="${r.ID}" aria-label="选择 ${esc(r.Title)}" ${checked.has(r.ID)?'checked':''}></label><div class="tracking-resource-content"><div class="tracking-resource-tags"><span class="tracking-cloud">${esc(data.CloudTypes[r.Cloud]||r.Cloud)}</span>${r.Status==='new'?'<span class="tracking-new-label">新资源 / 更新</span>':`<span>${resourceStates[r.Status]||esc(r.Status)}</span>`}${!selected?`<span>${esc(data.Subscriptions.find(s=>s.ID===r.Subscription)?.Title||'')}</span>`:''}</div><h4><a href="${esc(r.URL)}" target="_blank" rel="noopener noreferrer" data-open="${i}">${esc(r.Title)}</a></h4><div class="tracking-resource-meta"><span>${esc(r.Source||'PanSou')}</span><span>发现：${date(r.Updated)}</span>${r.Password?`<span>提取码：<strong>${esc(r.Password)}</strong></span>`:''}</div><div class="tracking-resource-actions"><button type="button" class="secondary" data-copy="${i}">复制链接</button>${r.Status!=='ignored'?(['mobile','115','quark','guangya'].includes(r.Cloud)?`<button type="button" data-import="${i}" ${data.Busy?'disabled':''}>${data.Subscriptions.find(s=>s.ID===r.Subscription)?.Import?.ResourceID===r.ID?(data.Subscriptions.find(s=>s.ID===r.Subscription)?.Import?.Stage==='complete'?'再次入库':'继续入库'):'入库'}</button>`:'<span class="tracking-import-unavailable">暂不支持此网盘转存</span>'):''}${r.Status!=='seen'?`<button type="button" class="text-button" data-mark="seen" data-id="${r.ID}">已读</button>`:''}<button type="button" class="text-button" data-mark="${r.Status==='ignored'?'new':'ignored'}" data-id="${r.ID}">${r.Status==='ignored'?'恢复':'忽略'}</button></div></div></article>`).join(''):`<div class="tracking-empty"><h3>${data.Subscriptions.length?'暂无符合条件的资源':'开始追你喜欢的作品'}</h3><p>${data.Subscriptions.length?'尝试搜索订阅或切换筛选条件。':'添加订阅，按周期寻找新的网盘分享。'}</p>${!data.Subscriptions.length?'<button type="button" data-empty-add>添加订阅</button>':''}</div>`;
    host.querySelector('[data-empty-add]')?.addEventListener('click',()=>edit());
    host.querySelectorAll('[data-resource-check]').forEach(input=>input.onchange=()=>{if(input.checked)checked.add(input.dataset.resourceCheck);else checked.delete(input.dataset.resourceCheck);updateSelection();});
    host.querySelectorAll('[data-copy]').forEach(button=>button.onclick=run(async()=>{const r=results.Items[Number(button.dataset.copy)],text=r.URL+(r.Password?'\n提取码：'+r.Password:'');try {await navigator.clipboard.writeText(text);toast('链接已复制');}catch{fmDialog('分享链接',`<label>链接<textarea rows="4" readonly>${esc(text)}</textarea></label>`,null);}if(r.Status==='new')await mark([r.ID],'seen');}));
    host.querySelectorAll('[data-open]').forEach(link=>link.addEventListener('click',()=>{const r=results.Items[Number(link.dataset.open)];if(r.Status==='new')mark([r.ID],'seen').catch(error=>toast(error.message,'error'));}));
    host.querySelectorAll('[data-import]').forEach(button=>button.onclick=run(()=>importResource(results.Items[Number(button.dataset.import)])));
    host.querySelectorAll('[data-mark]').forEach(button=>button.onclick=run(()=>mark([button.dataset.id],button.dataset.mark)));
    const pages=Math.max(1,Math.ceil(results.Total/results.Limit));
    host.querySelector('[data-pagination]').innerHTML=`<div class="tracking-pagination"><span>共 ${results.Total} 条 · 第 ${page} / ${pages} 页</span><div><button type="button" class="secondary" data-prev ${page<=1?'disabled':''}>上一页</button><button type="button" class="secondary" data-next ${page>=pages?'disabled':''}>下一页</button></div></div>`;
    host.querySelector('[data-prev]').onclick=run(async()=>{page--;checked.clear();await resourceList();});host.querySelector('[data-next]').onclick=run(async()=>{page++;checked.clear();await resourceList();});updateSelection();
  }
  function updateSelection() {
    const n=checked.size;host.querySelector('[data-selection]').textContent=n?`已选择 ${n} 条`:'未选择';
    host.querySelectorAll('[data-batch]').forEach(b=>b.disabled=!n);
    const input=host.querySelector('[data-select-all]');input.disabled=!results?.Items.length;input.checked=!!results?.Items.length&&n===results.Items.length;input.indeterminate=n>0&&n<results.Items.length;
  }
  async function mark(ids,state) {await api(base+'/resources','PUT',{IDs:ids,Status:state});if(current())await refresh(true);}
  async function search(id) {
    const b=await api(base+'/run','POST',{ID:id});toast(`已加入 ${b.Queued} 个订阅`);if(current()){data.Busy=true;await refresh(true);}
  }
  function processingFields(config={}) {
    const scrape=config.AutoScrape??data.Settings.AutoScrape??true,rename=config.AutoRename??data.Settings.AutoRename??false;
    return `<div class="tracking-processing"><label><input name="AutoScrape" type="checkbox" ${scrape?'checked':''}><span><strong>自动刮削</strong><small>下载 NFO、海报和单集资料</small></span></label><label><input name="AutoRename" type="checkbox" ${rename?'checked':''}><span><strong>自动重命名</strong><small>按 TMDB 名称和季集号规范本地文件</small></span></label></div>`;
  }
  function processingLabel(config={}) {
    return [(config.AutoRename??data.Settings.AutoRename??false)?'标准命名':'',(config.AutoScrape??data.Settings.AutoScrape??true)?'自动刮削':''].filter(Boolean).join(' · ');
  }
  function bindProcessing(dialog,options) {
    const update=()=>{
      const scrape=dialog.querySelector('[name="AutoScrape"]').checked,rename=dialog.querySelector('[name="AutoRename"]').checked;
      const warning=dialog.querySelector('[data-processing-warning]');
      if(warning){warning.textContent=[scrape&&!options.ScraperEnabled?'自动刮削需要先开启刮削总开关。':'',rename&&!options.TMDBConfigured?'自动重命名需要先配置 TMDB 凭据。':''].filter(Boolean).join(' ');warning.hidden=!warning.textContent;}
      const steps=dialog.querySelector('[data-import-steps]');
      if(steps)steps.innerHTML=['转存网盘','生成 STRM'+(rename?' · 标准命名':''),'扫描入库',scrape?'元数据刮削':'完成入库'].map(step=>`<li>${step}</li>`).join('');
    };
    dialog.querySelector('[name="AutoScrape"]').onchange=update;
    dialog.querySelector('[name="AutoRename"]').onchange=update;
    update();
  }
  function validateProcessing(f,options) {
    if(f.has('AutoScrape')&&!options.ScraperEnabled)throw new Error('请先开启刮削总开关，或取消自动刮削。');
    if(f.has('AutoRename')&&!options.TMDBConfigured)throw new Error('请先配置 TMDB 凭据，或取消自动重命名。');
  }
  function settings() {
    const c=data.Settings;
    fmDialog('盘搜设置',`<section class="tracking-sheet-block"><div class="feature-grid">${field('URL','PanSou 服务地址',c.URL,'url','required placeholder="http://pansou:8888"')}<label class="feature-check"><input name="Enabled" type="checkbox" ${c.Enabled?'checked':''}>启用定时追新</label></div><p class="tracking-help">填写运行 PanSou 的服务地址。Windows Docker 访问宿主机服务可用 http://host.docker.internal:8888。</p></section><section class="tracking-sheet-block"><h3>认证（未开启认证时留空）</h3><div class="feature-grid">${field('Username','账号',c.Username,'text','autocomplete="off"')}${field('Password','密码','','password',`autocomplete="new-password" placeholder="${c.HasPassword?'已保存，留空保留':'可选'}"`)}${field('Token','或填写 Bearer Token','','password',`autocomplete="new-password" placeholder="${c.HasToken?'已保存，留空保留':'可选'}"`)}</div><label class="feature-check"><input name="ClearCredentials" type="checkbox">清除已保存认证</label></section><section class="tracking-sheet-block"><h3>入库默认处理</h3>${processingFields(c)}<p class="tracking-help">单次入库和订阅可以分别调整。重命名只修改本地 STRM 和关联资料。</p></section><div class="tracking-connect-check"><button type="button" class="secondary" data-connect>检查已保存的连接</button><span data-connect-status role="status"></span></div>`,async f=>{
      await api(base+'/settings','PUT',{URL:f.get('URL'),Username:f.get('Username'),Password:f.get('Password'),Token:f.get('Token'),Enabled:f.has('Enabled'),AutoScrape:f.has('AutoScrape'),AutoRename:f.has('AutoRename'),ClearCredentials:f.has('ClearCredentials')});
      if(current()){await load(host,still);}toast('盘搜配置已保存');
    });
    const dialog=$('#modal');dialog.classList.add('tracking-sheet');
    dialog.querySelector('[data-connect]').onclick=run(async e=>{const b=e.currentTarget,display=dialog.querySelector('[data-connect-status]');b.disabled=true;display.classList.remove('tracking-error');display.textContent='正在连接…';try {const r=await api(base+'/connect','POST',{});display.textContent=`已连接 · ${r.Plugins} 个插件 · ${r.Channels} 个频道`;}catch(error){display.textContent=error.message;display.classList.add('tracking-error');}finally{b.disabled=false;}});
  }
  async function importResource(resource) {
    const subscription=data.Subscriptions.find(s=>s.ID===resource.Subscription);
    if(!subscription)return;
    const options=await api(base+'/import/options');if(!current())return;
    const mounts=options.Mounts.filter(m=>m.Enabled&&m.Supported&&m.Cloud===resource.Cloud),libraries=options.Libraries;
    const previous=subscription.Import?.ManualConfig||subscription.AutoImport||{};
    const mount=mounts.find(m=>m.ID===previous.MountID)||mounts[0],sameMount=mount?.ID===previous.MountID;
    const library=libraries.find(l=>l.Id===previous.Library)||libraries[0];
    const pending=!!subscription.Import?.PendingTask;
    const blocked=pending&&subscription.Import.ResourceID!==resource.ID;
    const ready=mounts.length&&libraries.length&&!blocked;
    const pickerMount={ID:mount?.ID||'',Name:mount?.Name||'网盘'};
    const notices=[!mounts.length?`请先添加并启用「${data.CloudTypes[resource.Cloud]||resource.Cloud}」挂载，分享只能转存到同类网盘。`:'',!libraries.length?'请先创建媒体库。':'',blocked?'另一条分享有待确认的转存任务，请先继续该资源的入库。':''].filter(Boolean);
    fmDialog('资源入库',`<section class="tracking-import-source"><span>${esc(data.CloudTypes[resource.Cloud]||resource.Cloud)}</span><h3>${esc(resource.Title)}</h3><div>${esc(subscription.Title)}${subscription.Year?' · '+subscription.Year:''}${resource.Password?' · 提取码：'+esc(resource.Password):''}</div></section><ol class="tracking-import-steps" data-import-steps><li>转存网盘</li><li>生成 STRM</li><li>扫描入库</li><li>元数据刮削</li></ol>${notices.map(n=>`<p class="tracking-error" role="alert">${esc(n)}</p>`).join('')}<section class="tracking-sheet-block tracking-auto-config"><div class="feature-grid"><label>目标网盘账号<select name="MountID" required><option value="">选择网盘账号</option>${mounts.map(m=>`<option value="${esc(m.ID)}" ${m.ID===mount?.ID?'selected':''}>${esc(m.Name)}</option>`).join('')}</select></label><label>入库媒体库<select name="Library" required><option value="">选择媒体库</option>${libraries.map(l=>`<option value="${esc(l.Id)}" ${l.Id===library?.Id?'selected':''}>${esc(l.Name)}</option>`).join('')}</select></label></div><div class="cloud-directory-grid">${CloudMounts.directoryField('Source','网盘转存父目录',sameMount?previous.RemotePath||'/':'/')}${CloudMounts.directoryField('Output','本地 STRM 父目录',previous.Output||library?.Locations?.[0]||options.FileRoot)}</div><section id="cloud-generate-picker" data-directory-picker role="region" hidden></section><div class="feature-grid">${field('PublicURL','播放器可访问的 AI Emby 地址',previous.PublicURL||location.origin,'url','required')}${field('ImportLimit','本次最多转存视频数',previous.Limit||200,'number','min="1" max="5000" required')}</div><section class="tracking-processing-section"><h3>入库处理</h3>${processingFields(previous)}<p class="tracking-error" data-processing-warning role="alert" hidden></p></section><p class="tracking-help">父目录下会建立「${esc(subscription.Title)}${subscription.Year?' ('+subscription.Year+')':''}」文件夹。本次仅入库所选分享，不改变订阅的自动入库设置。</p>${pending?'<p class="tracking-help">已有网盘任务待确认，请保留原目标目录后继续入库。</p>':''}</section>`,ready?async f=>{
      validateProcessing(f,options);
      await api(base+'/import/start','POST',{ID:resource.Subscription,ResourceID:resource.ID,Config:{MountID:f.get('MountID'),Library:f.get('Library'),RemotePath:f.get('Source'),Output:f.get('Output'),PublicURL:f.get('PublicURL'),Limit:Number(f.get('ImportLimit')),AutoScrape:f.has('AutoScrape'),AutoRename:f.has('AutoRename')}});
      selected=resource.Subscription;toast('已加入入库任务');if(current())await refresh(true);
    }:null,'开始入库');
    const dialog=$('#modal');dialog.classList.add('tracking-sheet','tracking-import-sheet','cloud-generate-sheet');
    bindProcessing(dialog,options);
    CloudMounts.bindDirectoryPicker(dialog,pickerMount,options.FileRoot);
    dialog.querySelector('[name="MountID"]').onchange=e=>{dialog.querySelector('[data-picker-close]')?.click();const m=mounts.find(m=>m.ID===e.target.value);pickerMount.ID=m?.ID||'';pickerMount.Name=m?.Name||'网盘';dialog.querySelector('[name="Source"]').value='/';};
    dialog.querySelector('[name="Library"]').onchange=e=>{dialog.querySelector('[data-picker-close]')?.click();const l=libraries.find(l=>l.Id===e.target.value);if(l?.Locations?.[0])dialog.querySelector('[name="Output"]').value=l.Locations[0];};
  }
  async function edit(s) {
    let options;try {options=await api(base+'/import/options');}catch(error){toast(error.message,'error');return;}if(!current())return;
    const c=s?.AutoImport||{},mounts=options.Mounts,libraries=options.Libraries;
    const pickerMount={ID:c.MountID||'',Name:mounts.find(m=>m.ID===c.MountID)?.Name||'网盘'};
    let resourceID=c.ResourceID||'';
    const initialLibrary=c.Library||libraries[0]?.Id||'',initialOutput=c.Output||libraries.find(l=>l.Id===initialLibrary)?.Locations?.[0]||options.FileRoot;
    const clouds=s?.CloudTypes||['mobile','115','quark','guangya'];
    fmDialog(s?'编辑订阅':'添加订阅',`<div class="tracking-editor-content"><section class="tracking-editor-card tracking-editor-basics"><div class="tracking-editor-section-head"><h3>基本设置</h3><label class="feature-check"><input name="Enabled" type="checkbox" ${!s||s.Enabled?'checked':''}>启用订阅</label></div><div class="feature-grid">${field('Title','作品名称',s?.Title||'','text','required maxlength="512" placeholder="电影或剧集名称"')}${field('Year','年份（可选）',s?.Year||'','number','min="0" max="9999"')}${field('Query','搜索关键词（可选）',s?.Query||'','text','maxlength="1024" placeholder="留空使用作品名称和年份"')}${field('Minutes','搜索间隔（分钟）',s?.Minutes||360,'number','min="15" max="10080" required')}<label>搜索来源<select name="SearchSource">${[['all','全部来源'],['plugin','搜索插件'],['tg','Telegram 频道']].map(([key,label])=>`<option value="${key}" ${key===(s?.Source||'all')?'selected':''}>${label}</option>`).join('')}</select></label></div></section>
      <section class="tracking-editor-card"><div class="tracking-editor-section-head"><h3>搜索网盘</h3><span>未选择时搜索全部</span></div><div class="tracking-cloud-choices">${Object.entries(data.CloudTypes).map(([key,label])=>`<label><input type="checkbox" name="CloudTypes" value="${key}" ${clouds.includes(key)?'checked':''}><span>${esc(label)}</span></label>`).join('')}</div></section>
      <section class="tracking-editor-card"><h3>关键词筛选</h3><div class="feature-grid">${field('Include','包含关键词（逗号分隔）',(s?.Include||[]).join('，'),'text','placeholder="如：1080P，4K"')}${field('Exclude','排除关键词（逗号分隔）',(s?.Exclude||[]).join('，'),'text','placeholder="如：预告，花絮"')}</div></section>
      <section class="tracking-editor-card tracking-auto-config"><div class="tracking-editor-section-head"><label class="feature-check tracking-editor-auto-toggle"><input name="AutoEnabled" type="checkbox" ${c.Enabled?'checked':''}>自动入库</label><span>转存 · STRM · 扫描入库</span></div><fieldset data-auto-fields ${c.Enabled?'':'disabled hidden'}><div class="feature-grid"><label>目标挂载<select name="MountID" required><option value="">选择网盘账号</option>${mounts.map(m=>`<option value="${esc(m.ID)}" ${m.ID===c.MountID?'selected':''} ${!m.Supported||!m.Enabled?'disabled':''}>${esc(m.Name)} · ${esc(data.CloudTypes[m.Cloud]||m.Driver)}${!m.Enabled?'（已暂停）':!m.Supported?'（不支持转存）':''}</option>`).join('')}</select></label><label>入库媒体库<select name="Library" required><option value="">选择媒体库</option>${libraries.map(l=>`<option value="${esc(l.Id)}" ${l.Id===initialLibrary?'selected':''}>${esc(l.Name)}</option>`).join('')}</select></label></div><div class="cloud-directory-grid">${CloudMounts.directoryField('Source','网盘转存父目录',c.RemotePath||'/')}${CloudMounts.directoryField('Output','本地 STRM 父目录',initialOutput)}</div><section id="cloud-generate-picker" data-directory-picker role="region" hidden></section><div class="feature-grid">${field('PublicURL','播放器可访问的 AI Emby 地址',c.PublicURL||location.origin,'url','required')}${field('ImportLimit','每次最多转存视频数',c.Limit||200,'number','min="1" max="5000" required')}</div><section class="tracking-processing-section"><h3>入库处理</h3>${processingFields(c)}<p class="tracking-error" data-processing-warning role="alert" hidden></p></section><p class="tracking-help">父目录下自动建立「作品名称 (年份)」目录，只补充新视频。115 使用 Cookie 挂载，移动盘使用个人云。</p>${c.ResourceID||s?.Import?.ResourceID?'<label class="feature-check"><input name="ResetResource" type="checkbox">重新按名称和年份自动选择分享</label>':''}</fieldset></section>
      <details class="tracking-editor-card tracking-editor-binding" ${s?.ItemID?'open':''}><summary>关联已有作品<span>可选</span></summary><div class="tracking-editor-binding-content"><div class="tracking-bind"><input type="search" data-bind-query value="${esc(s?.Title||'')}" placeholder="搜索媒体库中的作品" aria-label="搜索媒体库作品"><button type="button" class="secondary" data-bind-search>查找</button></div><select name="ItemID" data-bind-items aria-label="关联已有作品"><option value="">不关联</option>${s?.ItemID?`<option value="${esc(s.ItemID)}" selected>${esc(s.Title)}（已关联）</option>`:''}</select></div></details></div>`,async f=>{
      if(f.has('AutoEnabled'))validateProcessing(f,options);
      const b=await api(base+'/subscription','POST',{ID:s?.ID||'',Title:f.get('Title'),Year:Number(f.get('Year')||0),Query:f.get('Query'),Minutes:Number(f.get('Minutes')),Source:f.get('SearchSource'),CloudTypes:f.getAll('CloudTypes'),Include:words(f.get('Include')),Exclude:words(f.get('Exclude')),ItemID:f.get('ItemID'),Enabled:f.has('Enabled'),AutoImport:f.has('AutoEnabled')?{Enabled:true,MountID:f.get('MountID'),RemotePath:f.get('Source'),Output:f.get('Output'),Library:f.get('Library'),PublicURL:f.get('PublicURL'),Limit:Number(f.get('ImportLimit')),AutoScrape:f.has('AutoScrape'),AutoRename:f.has('AutoRename'),ResourceID:f.has('ResetResource')?'':resourceID,Reselect:f.has('ResetResource')}:{...c,Enabled:false}});
      selected=b.ID;page=1;if(current())await load(host,still);toast('订阅已保存');
    });
    const dialog=$('#modal');dialog.classList.add('tracking-sheet','tracking-auto-sheet','cloud-generate-sheet');
    bindProcessing(dialog,options);
    const heading=UI.el('div',{class:'tracking-editor-heading'}),title=dialog.querySelector('h2');
    title.replaceWith(heading);heading.append(title,UI.IconButton('关闭订阅弹窗','close',()=>closeModal()));
    const autoFields=dialog.querySelector('[data-auto-fields]'),autoEnabled=dialog.querySelector('[name="AutoEnabled"]');
    autoEnabled.onchange=()=>{if(!autoEnabled.checked)dialog.querySelector('[data-picker-close]')?.click();autoFields.hidden=autoFields.disabled=!autoEnabled.checked;};
    CloudMounts.bindDirectoryPicker(dialog,pickerMount,options.FileRoot);
    dialog.querySelector('[name="MountID"]').onchange=e=>{dialog.querySelector('[data-picker-close]')?.click();const mount=mounts.find(m=>m.ID===e.target.value);pickerMount.ID=mount?.ID||'';pickerMount.Name=mount?.Name||'网盘';dialog.querySelector('[name="Source"]').value='/';resourceID='';};
    dialog.querySelector('[name="Library"]').onchange=e=>{dialog.querySelector('[data-picker-close]')?.click();const library=libraries.find(l=>l.Id===e.target.value);if(library?.Locations?.[0])dialog.querySelector('[name="Output"]').value=library.Locations[0];};
    dialog.querySelector('[data-bind-search]').onclick=run(async e=>{
      const q=dialog.querySelector('[data-bind-query]').value.trim();if(!q)return;const b=e.currentTarget;b.disabled=true;
      try {const result=await api.search(q,{IncludeItemTypes:'Series,Movie',Limit:20});if(!dialog.open||!dialog.querySelector('[data-bind-items]'))return;const select=dialog.querySelector('[data-bind-items]');select.innerHTML='<option value="">不关联</option>'+(result.Items||[]).map(item=>`<option value="${esc(item.Id)}">${esc(item.Name)}${item.ProductionYear?' · '+item.ProductionYear:''} · ${item.Type==='Movie'?'电影':'剧集'}</option>`).join('');if(s?.ItemID)select.value=s.ItemID;if(!result.Items?.length)toast('媒体库没有匹配作品');}finally{b.disabled=false;}
    });
    dialog.querySelector('[name="Title"]').focus();
  }
  return {load};
})();
