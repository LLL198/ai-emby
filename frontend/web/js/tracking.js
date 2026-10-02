const Tracking = (() => {
  const base = '/admin/features/tracking';
  const stateNames = {idle:'等待搜索',running:'搜索中',complete:'搜索完成',error:'搜索失败',interrupted:'已中断'};
  const importStates={transfer:'转存中',strm:'生成 STRM',scan:'扫描入库',scrape:'刮削中',complete:'入库完成','waiting-resource':'等待合适资源',interrupted:'已中断'};
  const resourceStates = {new:'未读',seen:'已读',ignored:'已忽略'};
  let host, still, data, selected='', status='', cloud='', page=1, results, timer, generation=0, requestSerial=0, summarySerial=0;
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
      <div class="tracking-workspace"><aside class="tracking-subscriptions" aria-label="作品订阅"><div class="tracking-subscriptions-head"><h3>我的订阅 <span>${data.Subscriptions.length}</span></h3><button type="button" class="secondary" data-run-all ${data.Busy||!data.Settings.URL||!data.Subscriptions.some(s=>s.Enabled)?'disabled':''}>搜索全部</button></div><div data-subscriptions></div></aside>
      <section class="tracking-results"><div data-selected></div><div class="tracking-filters"><nav aria-label="资源状态">${[['','全部'],['new','未读'],['seen','已读'],['ignored','忽略']].map(([key,label])=>`<button type="button" data-filter="${key}" aria-current="${status===key?'page':'false'}">${label}</button>`).join('')}</nav><label>网盘<select data-cloud aria-label="筛选网盘"><option value="">全部网盘</option>${Object.entries(data.CloudTypes).map(([key,label])=>`<option value="${key}" ${key===cloud?'selected':''}>${esc(label)}</option>`).join('')}</select></label></div>
      <div class="tracking-batch"><label><input type="checkbox" data-select-all aria-label="全选本页">全选本页</label><span data-selection>未选择</span><button type="button" class="secondary" data-batch="seen" disabled>标为已读</button><button type="button" class="secondary" data-batch="ignored" disabled>忽略</button></div><div data-resources aria-live="polite"></div><div data-pagination></div></section></div>`;
    renderSubscriptions();renderSelected();
    host.querySelector('[data-add]').onclick=()=>edit();host.querySelector('[data-settings]').onclick=settings;
    host.querySelector('[data-config]')?.addEventListener('click',settings);
    host.querySelector('[data-refresh]').onclick=run(()=>refresh(true));host.querySelector('[data-run-all]').onclick=run(()=>search(''));
    host.querySelectorAll('[data-filter]').forEach(b=>b.onclick=run(async()=>{status=b.dataset.filter;page=1;checked.clear();host.querySelectorAll('[data-filter]').forEach(x=>x.setAttribute('aria-current',String(x===b?'page':'false')));await resourceList();}));
    host.querySelector('[data-cloud]').onchange=run(async e=>{cloud=e.target.value;page=1;checked.clear();await resourceList();});
    host.querySelector('[data-select-all]').onchange=e=>{checked.clear();if(e.target.checked)results.Items.forEach(x=>checked.add(x.ID));host.querySelectorAll('[data-resource-check]').forEach(x=>x.checked=e.target.checked);updateSelection();};
    host.querySelectorAll('[data-batch]').forEach(b=>b.onclick=run(()=>mark([...checked],b.dataset.batch)));
  }
  function renderSubscriptions() {
    const unread=data.Subscriptions.reduce((n,s)=>n+s.NewCount,0);
    host.querySelector('[data-subscriptions]').innerHTML=`<button type="button" class="tracking-sub ${!selected?'is-active':''}" data-sub=""><span><strong>全部资源</strong><small>${data.Subscriptions.reduce((n,s)=>n+s.ResourceCount,0)} 条资源</small></span>${unread?`<span class="tracking-unread">${unread}</span>`:''}</button>${data.Subscriptions.map(s=>`<button type="button" class="tracking-sub ${s.ID===selected?'is-active':''}" data-sub="${s.ID}"><span><strong>${esc(s.Title)}${s.Year?` <small>${s.Year}</small>`:''}</strong><small class="${s.State==='error'?'tracking-error':''}">${s.Enabled?esc(stateNames[s.State]||s.State):'已暂停'}</small></span>${s.NewCount?`<span class="tracking-unread">${s.NewCount}</span>`:''}</button>`).join('')}${!data.Subscriptions.length?'<p class="tracking-empty-small">订阅电影或剧集，资源会集中显示在这里。</p>':''}`;
    host.querySelectorAll('[data-sub]').forEach(b=>b.onclick=run(async()=>{selected=b.dataset.sub;page=1;checked.clear();renderSubscriptions();renderSelected();await resourceList();}));
  }
  function renderSelected() {
    const s=selectedSub();
    host.querySelector('[data-selected]').innerHTML=s?`<div class="tracking-selected-head"><div><h3>${esc(s.Title)}${s.Year?` <span>${s.Year}</span>`:''}</h3><p>关键词：${esc(s.Query)} · 每 ${s.Minutes>=60?(s.Minutes/60)+' 小时':s.Minutes+' 分钟'}</p></div><div class="tracking-actions"><button type="button" class="secondary" data-edit ${data.Busy?'disabled':''}>编辑</button><button type="button" data-run ${data.Busy||!data.Settings.URL?'disabled':''}>立即搜索</button></div></div>${s.Owned?`<p class="tracking-owned">已入库：${esc(s.Owned)}</p>`:''}${s.AutoImport?.Enabled?`<div class="tracking-import-status"><div><strong>自动入库${s.ImportMount?' · '+esc(s.ImportMount):''}</strong><span>${esc(importStates[s.Import?.Stage]||'等待执行')}${s.Import?.Saved?' · 已转存 '+s.Import.Saved+' 个视频':''}</span></div>${s.Import?.Error?`<p class="tracking-error" role="alert">${esc(s.Import.Error)}</p>`:''}${s.Import?.PendingTask?'<p>网盘任务待确认，下次执行会继续核对。</p>':''}<div class="tracking-actions"><button type="button" class="secondary" data-retry-import ${data.Busy?'disabled':''}>${s.Import?.Error?'重试入库':'检查更新并入库'}</button>${s.AutoImport.ResourceID||s.Import?.ResourceID?`<button type="button" class="text-button" data-unpin ${data.Busy?'disabled':''}>重新自动选择分享</button>`:''}</div></div>`:''}<div class="tracking-search-meta"><span>上次：${date(s.LastSearch)}</span>${data.Settings.Enabled&&s.Enabled?`<span>下次：${date(s.NextSearch)}</span>`:''}<span>上次找到 ${s.LastCount} 条</span></div>${s.Error?`<p class="tracking-error" role="alert">${esc(s.Error)}</p>`:''}<div class="tracking-selected-actions"><button type="button" class="text-button" data-read-all ${!s.NewCount?'disabled':''}>本订阅全部标为已读</button><button type="button" class="text-button" data-pause ${data.Busy?'disabled':''}>${s.Enabled?'暂停订阅':'恢复订阅'}</button><button type="button" class="text-button" data-delete ${data.Busy?'disabled':''}>删除订阅</button></div>`:`<div class="tracking-selected-head"><div><h3>资源索引</h3><p>按作品和网盘筛选，选择合适的分享资源。</p></div></div>`;
    if(!s)return;
    host.querySelector('[data-edit]').onclick=()=>edit(s);host.querySelector('[data-retry-import]')?.addEventListener('click',run(()=>search(s.ID)));host.querySelector('[data-unpin]')?.addEventListener('click',run(async()=>{await api(base+'/import/resource','POST',{ID:s.ID,ResourceID:''});await refresh(true);}));host.querySelector('[data-run]').onclick=run(()=>search(s.ID));
    host.querySelector('[data-read-all]').onclick=run(async()=>{await api(base+'/resources','PUT',{Subscription:s.ID,Status:'seen',IDs:[]});await refresh(true);});
    host.querySelector('[data-pause]').onclick=run(async()=>{await api(base+'/subscription','POST',{...s,Enabled:!s.Enabled});await refresh(true);});
    host.querySelector('[data-delete]').onclick=run(async()=>{if(!(await confirmDialog('删除订阅',`删除「${s.Title}」及其资源索引？`,'删除')))return;await api(base+'/subscription?ID='+encodeURIComponent(s.ID),'DELETE');selected='';page=1;await refresh(true);});
  }
  async function refresh(reset) {
    const viewGeneration=generation,serial=++summarySerial,b=await api(base);if(serial!==summarySerial||viewGeneration!==generation||!current())return;
    const changed=JSON.stringify(data.Subscriptions.map(s=>[s.ID,s.NewCount,s.ResourceCount,s.LastSearch,s.State,s.Import]))!==JSON.stringify(b.Subscriptions.map(s=>[s.ID,s.NewCount,s.ResourceCount,s.LastSearch,s.State,s.Import]));
    data=b;if(selected&&!selectedSub())selected='';if(reset)checked.clear();
    host.querySelector('[data-run-state]').textContent=data.Busy?'正在执行追新任务…':data.Settings.Enabled?'定时追新已开启':'定时追新未开启';
    host.querySelector('[data-run-all]').disabled=data.Busy||!data.Settings.URL||!data.Subscriptions.some(s=>s.Enabled);
    renderSubscriptions();renderSelected();if(reset||changed||data.Busy||results==null)await resourceList();schedule();
  }
  async function resourceList() {
    const serial=++requestSerial,viewGeneration=generation;
    const response=await api.query(base+'/resources',{Subscription:selected,Status:status,Cloud:cloud,Page:page,Limit:30});
    if(serial!==requestSerial||viewGeneration!==generation||!current())return;results=response;
    if(page>1&&!results.Items.length){page--;await resourceList();return;}
    const ids=new Set(results.Items.map(x=>x.ID));for(const id of checked)if(!ids.has(id))checked.delete(id);
    host.querySelector('[data-resources]').innerHTML=results.Items.length?results.Items.map((r,i)=>`<article class="tracking-resource ${r.Status==='new'?'is-new':''}"><label class="tracking-resource-select"><input type="checkbox" data-resource-check="${r.ID}" aria-label="选择 ${esc(r.Title)}" ${checked.has(r.ID)?'checked':''}></label><div class="tracking-resource-content"><div class="tracking-resource-tags"><span class="tracking-cloud">${esc(data.CloudTypes[r.Cloud]||r.Cloud)}</span>${r.Status==='new'?'<span class="tracking-new-label">新资源 / 更新</span>':`<span>${resourceStates[r.Status]||esc(r.Status)}</span>`}${!selected?`<span>${esc(data.Subscriptions.find(s=>s.ID===r.Subscription)?.Title||'')}</span>`:''}</div><h4><a href="${esc(r.URL)}" target="_blank" rel="noopener noreferrer" data-open="${i}">${esc(r.Title)}</a></h4><div class="tracking-resource-meta"><span>${esc(r.Source||'PanSou')}</span><span>发现：${date(r.Updated)}</span>${r.Password?`<span>提取码：<strong>${esc(r.Password)}</strong></span>`:''}</div><div class="tracking-resource-actions"><button type="button" class="secondary" data-copy="${i}">复制链接</button>${r.Status!=='ignored'&&data.Subscriptions.find(s=>s.ID===r.Subscription)?.AutoImport?.Enabled&&data.Subscriptions.find(s=>s.ID===r.Subscription)?.ImportCloud===r.Cloud?`<button type="button" class="secondary" data-import="${i}" ${data.Busy?'disabled':''}>${data.Subscriptions.find(s=>s.ID===r.Subscription)?.Import?.ResourceID===r.ID?'继续此资源入库':'使用此资源入库'}</button>`:''}${r.Status!=='seen'?`<button type="button" class="text-button" data-mark="seen" data-id="${r.ID}">已读</button>`:''}<button type="button" class="text-button" data-mark="${r.Status==='ignored'?'new':'ignored'}" data-id="${r.ID}">${r.Status==='ignored'?'恢复':'忽略'}</button></div></div></article>`).join(''):`<div class="tracking-empty"><h3>${data.Subscriptions.length?'暂无符合条件的资源':'开始追你喜欢的作品'}</h3><p>${data.Subscriptions.length?'尝试搜索订阅或切换筛选条件。':'添加订阅，按周期寻找新的网盘分享。'}</p>${!data.Subscriptions.length?'<button type="button" data-empty-add>添加订阅</button>':''}</div>`;
    host.querySelector('[data-empty-add]')?.addEventListener('click',()=>edit());
    host.querySelectorAll('[data-resource-check]').forEach(input=>input.onchange=()=>{if(input.checked)checked.add(input.dataset.resourceCheck);else checked.delete(input.dataset.resourceCheck);updateSelection();});
    host.querySelectorAll('[data-copy]').forEach(button=>button.onclick=run(async()=>{const r=results.Items[Number(button.dataset.copy)],text=r.URL+(r.Password?'\n提取码：'+r.Password:'');try {await navigator.clipboard.writeText(text);toast('链接已复制');}catch{fmDialog('分享链接',`<label>链接<textarea rows="4" readonly>${esc(text)}</textarea></label>`,null);}if(r.Status==='new')await mark([r.ID],'seen');}));
    host.querySelectorAll('[data-open]').forEach(link=>link.addEventListener('click',()=>{const r=results.Items[Number(link.dataset.open)];if(r.Status==='new')mark([r.ID],'seen').catch(error=>toast(error.message,'error'));}));
    host.querySelectorAll('[data-import]').forEach(button=>button.onclick=run(async()=>{const resource=results.Items[Number(button.dataset.import)];if(!(await confirmDialog('选择入库资源',`将「${resource.Title}」设为此订阅的固定分享并启动自动入库？`,'开始入库')))return;await api(base+'/import/resource','POST',{ID:resource.Subscription,ResourceID:resource.ID});toast('已选择分享，正在加入入库任务');await refresh(true);}));
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
  function settings() {
    const c=data.Settings;
    fmDialog('盘搜连接',`<section class="tracking-sheet-block"><div class="feature-grid">${field('URL','PanSou 服务地址',c.URL,'url','required placeholder="http://pansou:8888"')}<label class="feature-check"><input name="Enabled" type="checkbox" ${c.Enabled?'checked':''}>启用定时追新</label></div><p class="tracking-help">填写运行 PanSou 的服务地址。Windows Docker 访问宿主机服务可用 http://host.docker.internal:8888。</p></section><section class="tracking-sheet-block"><h3>认证（未开启认证时留空）</h3><div class="feature-grid">${field('Username','账号',c.Username,'text','autocomplete="off"')}${field('Password','密码','','password',`autocomplete="new-password" placeholder="${c.HasPassword?'已保存，留空保留':'可选'}"`)}${field('Token','或填写 Bearer Token','','password',`autocomplete="new-password" placeholder="${c.HasToken?'已保存，留空保留':'可选'}"`)}</div><label class="feature-check"><input name="ClearCredentials" type="checkbox">清除已保存认证</label></section><div class="tracking-connect-check"><button type="button" class="secondary" data-connect>检查已保存的连接</button><span data-connect-status role="status"></span></div>`,async f=>{
      await api(base+'/settings','PUT',{URL:f.get('URL'),Username:f.get('Username'),Password:f.get('Password'),Token:f.get('Token'),Enabled:f.has('Enabled'),ClearCredentials:f.has('ClearCredentials')});
      if(current()){await load(host,still);}toast('盘搜配置已保存');
    });
    const dialog=$('#modal');dialog.classList.add('tracking-sheet');
    dialog.querySelector('[data-connect]').onclick=run(async e=>{const b=e.currentTarget,display=dialog.querySelector('[data-connect-status]');b.disabled=true;display.classList.remove('tracking-error');display.textContent='正在连接…';try {const r=await api(base+'/connect','POST',{});display.textContent=`已连接 · ${r.Plugins} 个插件 · ${r.Channels} 个频道`;}catch(error){display.textContent=error.message;display.classList.add('tracking-error');}finally{b.disabled=false;}});
  }
  async function edit(s) {
    let options;try {options=await api(base+'/import/options');}catch(error){toast(error.message,'error');return;}if(!current())return;
    const c=s?.AutoImport||{},mounts=options.Mounts,libraries=options.Libraries;
    const pickerMount={ID:c.MountID||'',Name:mounts.find(m=>m.ID===c.MountID)?.Name||'网盘'};
    let resourceID=c.ResourceID||'';
    const initialLibrary=c.Library||libraries[0]?.Id||'',initialOutput=c.Output||libraries.find(l=>l.Id===initialLibrary)?.Locations?.[0]||options.FileRoot;
    const clouds=s?.CloudTypes||['mobile','115','quark','guangya'];
    fmDialog(s?'编辑订阅':'添加订阅',`<section class="tracking-sheet-block"><div class="feature-grid">${field('Title','作品名称',s?.Title||'','text','required maxlength="512"')}${field('Year','年份（可选）',s?.Year||'','number','min="0" max="9999"')}${field('Query','搜索关键词（可选）',s?.Query||'','text','maxlength="1024" placeholder="留空使用作品名称和年份"')}${field('Minutes','搜索间隔（分钟）',s?.Minutes||360,'number','min="15" max="10080" required')}<label>搜索来源<select name="SearchSource">${[['all','全部来源'],['plugin','搜索插件'],['tg','Telegram 频道']].map(([key,label])=>`<option value="${key}" ${key===(s?.Source||'all')?'selected':''}>${label}</option>`).join('')}</select></label><label class="feature-check"><input name="Enabled" type="checkbox" ${!s||s.Enabled?'checked':''}>启用订阅</label></div></section>
      <section class="tracking-sheet-block"><h3>搜索网盘</h3><div class="tracking-cloud-choices">${Object.entries(data.CloudTypes).map(([key,label])=>`<label><input type="checkbox" name="CloudTypes" value="${key}" ${clouds.includes(key)?'checked':''}>${esc(label)}</label>`).join('')}</div><p class="tracking-help">未选择时搜索全部支持的网盘。</p></section><section class="tracking-sheet-block"><div class="feature-grid">${field('Include','包含关键词（逗号分隔）',(s?.Include||[]).join('，'),'text','placeholder="如：1080P，4K"')}${field('Exclude','排除关键词（逗号分隔）',(s?.Exclude||[]).join('，'),'text','placeholder="如：预告，花絮"')}</div></section><section class="tracking-sheet-block tracking-auto-config"><label class="feature-check"><input name="AutoEnabled" type="checkbox" ${c.Enabled?'checked':''}>自动入库</label><p class="tracking-help">转存到所选网盘 → 生成 STRM → 扫库 → 刮削。优先沿用已选分享，只补充新视频。</p><fieldset data-auto-fields ${c.Enabled?'':'disabled hidden'}><div class="feature-grid"><label>目标挂载<select name="MountID" required><option value="">选择网盘账号</option>${mounts.map(m=>`<option value="${esc(m.ID)}" ${m.ID===c.MountID?'selected':''} ${!m.Supported||!m.Enabled?'disabled':''}>${esc(m.Name)} · ${esc(data.CloudTypes[m.Cloud]||m.Driver)}${!m.Enabled?'（已暂停）':!m.Supported?'（不支持转存）':''}</option>`).join('')}</select></label><label>入库媒体库<select name="Library" required><option value="">选择媒体库</option>${libraries.map(l=>`<option value="${esc(l.Id)}" ${l.Id===initialLibrary?'selected':''}>${esc(l.Name)}</option>`).join('')}</select></label></div><div class="cloud-directory-grid">${CloudMounts.directoryField('Source','网盘转存父目录',c.RemotePath||'/')}${CloudMounts.directoryField('Output','本地 STRM 父目录',initialOutput)}</div><section id="cloud-generate-picker" data-directory-picker role="region" hidden></section><div class="feature-grid">${field('PublicURL','播放器可访问的 AI Emby 地址',c.PublicURL||location.origin,'url','required')}${field('ImportLimit','每次最多转存视频数',c.Limit||200,'number','min="1" max="5000" required')}</div><p class="tracking-help">网盘与本地父目录下会自动建立「作品名称 (年份)」目录。115 请选择 Cookie 挂载；移动盘请选择个人云。转存使用网盘接口，STRM 沿用挂载的播放方式。</p>${!options.ScraperEnabled?'<p class="tracking-error">刮削总开关尚未开启，请在刮削模块开启后保存自动入库。</p>':''}${c.ResourceID||s?.Import?.ResourceID?'<label class="feature-check"><input name="ResetResource" type="checkbox">重新按名称和年份自动选择分享</label>':''}</fieldset></section><section class="tracking-sheet-block"><h3>关联媒体库作品（可选）</h3><div class="tracking-bind"><input type="search" data-bind-query value="${esc(s?.Title||'')}" placeholder="搜索已有作品" aria-label="搜索媒体库作品"><button type="button" class="secondary" data-bind-search>查找</button></div><select name="ItemID" data-bind-items aria-label="关联已有作品"><option value="">不关联</option>${s?.ItemID?`<option value="${esc(s.ItemID)}" selected>${esc(s.Title)}（已关联）</option>`:''}</select><p class="tracking-help">关联后可查看已入库的季和集数。搜索结果需要自行确认作品与集数。</p></section>`,async f=>{
      const b=await api(base+'/subscription','POST',{ID:s?.ID||'',Title:f.get('Title'),Year:Number(f.get('Year')||0),Query:f.get('Query'),Minutes:Number(f.get('Minutes')),Source:f.get('SearchSource'),CloudTypes:f.getAll('CloudTypes'),Include:words(f.get('Include')),Exclude:words(f.get('Exclude')),ItemID:f.get('ItemID'),Enabled:f.has('Enabled'),AutoImport:f.has('AutoEnabled')?{Enabled:true,MountID:f.get('MountID'),RemotePath:f.get('Source'),Output:f.get('Output'),Library:f.get('Library'),PublicURL:f.get('PublicURL'),Limit:Number(f.get('ImportLimit')),ResourceID:f.has('ResetResource')?'':resourceID,Reselect:f.has('ResetResource')}:{...c,Enabled:false}});
      selected=b.ID;page=1;if(current())await load(host,still);toast('订阅已保存');
    });
    const dialog=$('#modal');dialog.classList.add('tracking-sheet','tracking-auto-sheet');
    const autoFields=dialog.querySelector('[data-auto-fields]'),autoEnabled=dialog.querySelector('[name="AutoEnabled"]');
    autoEnabled.onchange=()=>{if(!autoEnabled.checked)dialog.querySelector('[data-picker-close]')?.click();autoFields.hidden=autoFields.disabled=!autoEnabled.checked;};
    CloudMounts.bindDirectoryPicker(dialog,pickerMount,options.FileRoot);
    dialog.querySelector('[name="MountID"]').onchange=e=>{dialog.querySelector('[data-picker-close]')?.click();const mount=mounts.find(m=>m.ID===e.target.value);pickerMount.ID=mount?.ID||'';pickerMount.Name=mount?.Name||'网盘';dialog.querySelector('[name="Source"]').value='/';resourceID='';};
    dialog.querySelector('[name="Library"]').onchange=e=>{dialog.querySelector('[data-picker-close]')?.click();const library=libraries.find(l=>l.Id===e.target.value);if(library?.Locations?.[0])dialog.querySelector('[name="Output"]').value=library.Locations[0];};
    dialog.querySelector('[data-bind-search]').onclick=run(async e=>{
      const q=dialog.querySelector('[data-bind-query]').value.trim();if(!q)return;const b=e.currentTarget;b.disabled=true;
      try {const result=await api.search(q,{IncludeItemTypes:'Series,Movie',Limit:20});if(!dialog.open||!dialog.querySelector('[data-bind-items]'))return;const select=dialog.querySelector('[data-bind-items]');select.innerHTML='<option value="">不关联</option>'+(result.Items||[]).map(item=>`<option value="${esc(item.Id)}">${esc(item.Name)}${item.ProductionYear?' · '+item.ProductionYear:''} · ${item.Type==='Movie'?'电影':'剧集'}</option>`).join('');if(s?.ItemID)select.value=s.ItemID;if(!result.Items?.length)toast('媒体库没有匹配作品');}finally{b.disabled=false;}
    });
  }
  return {load};
})();
