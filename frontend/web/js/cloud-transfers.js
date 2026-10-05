const CloudTransfers = (() => {
  const base = '/admin/features/cloud-transfer';
  const states = {queued:'等待执行',running:'处理中',paused:'已暂停',cancelled:'已取消',error:'需要处理',complete:'已完成'};
  const stages = {plan:'解析分享',transfer:'转存原始文件',download:'下载视频',remux:'无损重封装',upload:'上传网盘',process:'下载、处理与上传',complete:'已完成'};
  let host, still, data, timer, generation=0, selected='', query='', filter='';
  const field = (name,title,value,type='text',extra='') => `<label>${esc(title)}<input name="${esc(name)}" type="${type}" value="${esc(value)}" ${extra}></label>`;
  const size = bytes => bytes>=1073741824?(bytes/1073741824).toFixed(2)+' GiB':bytes>=1048576?(bytes/1048576).toFixed(1)+' MiB':(bytes||0)+' B';
  const date = value => new Date(value*1000).toLocaleString();
  const active = task => ['queued','running'].includes(task.State);
  const current = () => host?.isConnected && still?.();
  const mountName = id => data?.Mounts?.find(m=>m.ID===id)?.Name || '已移除的网盘';
  function actions(task) {
    return `<button type="button" class="secondary" data-detail="${esc(task.ID)}">查看详情</button>${active(task)?`<button type="button" class="secondary" data-action="pause" data-id="${esc(task.ID)}">暂停</button>`:''}${['paused','error','cancelled'].includes(task.State)?`<button type="button" data-action="resume" data-id="${esc(task.ID)}">${task.State==='error'?'重试未完成':'继续任务'}</button>`:''}${task.State!=='complete'&&task.State!=='cancelled'?`<button type="button" class="text-button" data-action="cancel" data-id="${esc(task.ID)}">取消</button>`:''}`;
  }
  async function load(target,isCurrent) {
    clearTimeout(timer);const serial=++generation;host=target;still=isCurrent;
    data=await api(base);if(serial!==generation||!current())return;
    host.classList.add('transfer-page');
    host.innerHTML=`<div class="transfer-toolbar"><div><h2>资源搬运</h2><span>转存原始文件 · 无损重封装 · 多盘上传</span></div><div><button type="button" class="secondary" data-refresh>刷新</button><button type="button" data-search>去搜索资源</button></div></div><div class="transfer-summary" data-summary></div><div class="transfer-filters"><input type="search" data-query aria-label="搜索搬运任务" placeholder="搜索任务名称" value="${esc(query)}"><select data-filter aria-label="筛选任务状态"><option value="">全部任务</option>${Object.entries(states).map(([key,name])=>`<option value="${key}" ${filter===key?'selected':''}>${name}</option>`).join('')}</select><span class="transfer-cache">临时目录：${esc(data.CacheRoot)}</span></div><div class="transfer-task-list" data-tasks></div>`;
    host.querySelector('[data-search]').onclick=()=>navigateAdminSection(24);
    host.querySelector('[data-refresh]').onclick=run(refresh);
    host.querySelector('[data-query]').oninput=e=>{query=e.target.value;render()};
    host.querySelector('[data-filter]').onchange=e=>{filter=e.target.value;render()};
    render();schedule();if(selected){const id=selected;selected='';await details(id)}
  }
  function render() {
    if(!current())return;
    const tasks=data.Tasks||[];
    host.querySelector('[data-summary]').innerHTML=[['处理中',tasks.filter(active).length],['已完成',tasks.filter(t=>t.State==='complete').length],['需要处理',tasks.filter(t=>['error','paused'].includes(t.State)).length],['全部任务',tasks.length]].map(([name,value])=>`<article><span>${name}</span><strong>${value}</strong></article>`).join('');
    const visible=tasks.filter(t=>(!filter||t.State===filter)&&t.Config.Title.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
    const list=host.querySelector('[data-tasks]');
    list.innerHTML=visible.length?visible.map(task=>`<article class="transfer-task"><div class="transfer-task-heading"><div><h3>${esc(task.Config.Title)}</h3><span>${date(task.Created)}</span></div><span class="transfer-badge is-${esc(task.State)}">${states[task.State]||esc(task.State)}</span></div><div class="transfer-route"><div><span>转存盘</span><strong>${esc(mountName(task.Config.SourceMountID))}</strong></div><span aria-hidden="true">→</span><div><span>处理方式</span><strong>${esc(task.Config.Format.toUpperCase())} 无损重封装</strong></div><span aria-hidden="true">→</span><div><span>上传到</span><strong>${task.Config.Targets.map(t=>esc(mountName(t.MountID))).join('、')}</strong></div></div><div class="transfer-task-progress"><strong>${task.Done} <span>/ ${task.Total||'待解析'} 个视频</span></strong><span>${esc(stages[task.Stage]||task.Stage)}${task.Failed?' · '+task.Failed+' 个未完成':''}</span></div><progress max="${Math.max(1,task.Total)}" value="${task.Done}" aria-label="任务完成进度"></progress>${task.Error?`<p class="transfer-error">${esc(task.Error)}</p>`:''}${task.Limited?`<p class="transfer-note">本次按上限处理前 ${task.Config.Limit} 个视频。</p>`:''}<div class="transfer-task-bottom"><span class="transfer-folder">${esc(task.Folder)}</span><div>${actions(task)}</div></div></article>`).join(''):`<div class="transfer-empty"><h3>${tasks.length?'没有符合条件的任务':'把喜欢的资源留在自己的网盘'}</h3><p>${tasks.length?'调整搜索或筛选条件。':'在追新索引的搜索结果中，点击「下载处理」创建任务。'}</p></div>`;
    bindActions(list);
  }
  function bindActions(container) {
    container.querySelectorAll('[data-detail]').forEach(button=>button.onclick=run(()=>details(button.dataset.detail)));
    container.querySelectorAll('[data-action]').forEach(button=>button.onclick=run(async()=>{
      button.disabled=true;
      try {await api(base+'/action','POST',{ID:button.dataset.id,Action:button.dataset.action});toast(button.dataset.action==='resume'?'已加入恢复队列':'请求已提交');if(current())await refresh()}
      finally {if(button.isConnected)button.disabled=false}
    }));
  }
  async function refresh() {
    if(!current())return;
    const serial=generation;const result=await api(base);
    if(serial!==generation||!current())return;data=result;render();schedule();
  }
  function schedule() {
    clearTimeout(timer);if(!current())return;
    timer=setTimeout(()=>refresh().catch(error=>{if(current()){toast(error.message,'error');schedule()}}),(data.Tasks||[]).some(active)?3000:15000);
  }
  async function create(resource) {
    const options=await api.query(base+'/options',{ResourceID:resource.ID});
    const source=options.Mounts.filter(m=>m.Enabled&&m.TransferSupported&&m.Cloud===resource.Cloud);
    const targets=options.Mounts.filter(m=>m.Enabled);
    const mobile=targets.find(m=>m.Driver==='139Yun');
    const ready=source.length&&targets.length&&options.RemuxReady;
    const title=options.SuggestedTitle||resource.Title;
    const pickerMount={ID:source[0]?.ID||'',Name:source[0]?.Name||'转存网盘'};
    fmDialog('下载、无损处理与上传',`<div class="transfer-source"><span>所选分享</span><h3>${esc(resource.Title)}</h3></div><ol class="transfer-steps"><li><b>1</b>转存网盘</li><li><b>2</b>下载视频</li><li><b>3</b>无损重封装</li><li><b>4</b>上传保存</li></ol>${!source.length?'<p class="transfer-error">请先添加并启用与此分享同平台的网盘账号。</p>':''}${!options.RemuxReady?'<p class="transfer-error">服务器尚未安装 FFmpeg 和 FFprobe。</p>':''}<section class="transfer-form-block"><h3>原始资源</h3><div class="feature-grid">${field('Title','任务名称',title,'text','required maxlength="180"')}<label>转存账号<select name="SourceMountID" required>${source.map(m=>`<option value="${esc(m.ID)}">${esc(m.Name)}</option>`).join('')}</select></label></div>${CloudMounts.directoryField('Source','网盘转存父目录','/AI Emby/原始资源')}<p class="transfer-note">分享先转存到同平台账号，原始文件会保留。</p></section><section class="transfer-form-block"><h3>上传目标</h3><div class="transfer-targets">${targets.map(m=>`<div class="transfer-target"><label class="transfer-target-check"><input type="checkbox" name="TargetMount" value="${esc(m.ID)}" ${m.ID===mobile?.ID?'checked':''}><span><strong>${esc(m.Name)}</strong><small>${esc(m.Driver==='139Yun'?'移动云盘':options.Mounts.find(x=>x.ID===m.ID)?.Driver||'网盘')}</small></span></label><div data-target-path="${esc(m.ID)}" ${m.ID===mobile?.ID?'':'hidden'}>${CloudMounts.directoryField('Target'+m.ID,'上传父目录','/AI Emby/处理完成')}</div></div>`).join('')}</div><p class="transfer-note">可选多个网盘，默认选择移动云盘。</p></section><section id="cloud-generate-picker" data-directory-picker role="region" hidden></section><section class="transfer-form-block"><h3>处理设置</h3><div class="transfer-options-grid"><label>封装格式<select name="Format"><option value="mkv">MKV（优先兼容多音轨和字幕）</option><option value="mp4">MP4</option></select></label>${field('Limit','本次最多视频数',200,'number','required min="1" max="5000"')}${field('Concurrency','文件处理并发',1,'number','required min="1" max="4"')}${field('CacheLimitGB','任务临时空间上限（GiB）',100,'number','required min="1" max="100000"')}</div><label class="transfer-keep"><input type="checkbox" name="KeepLocal">上传后保留本地下载和处理文件</label><p class="transfer-note">不重新编码，完整复制音视频流、字幕和章节；不支持的编码会停止并保留下载副本。下载、上传使用服务器流量。所有上传目标确认成功后才清理临时文件。</p></section>`,ready?async form=>{
      const selectedTargets=form.getAll('TargetMount');
      if(!selectedTargets.length)throw Error('请至少选择一个上传网盘');
      const task=await api(base+'/start','POST',{ResourceID:resource.ID,Title:form.get('Title'),SourceMountID:form.get('SourceMountID'),SourcePath:form.get('Source'),Targets:selectedTargets.map(id=>({MountID:id,Path:form.get('Target'+id)})),Format:form.get('Format'),Limit:Number(form.get('Limit')),Concurrency:Number(form.get('Concurrency')),CacheLimitGB:Number(form.get('CacheLimitGB')),KeepLocal:form.has('KeepLocal')});
      await closeModal();selected=task.ID;toast('已加入资源搬运队列');await navigateAdminSection(25);return false;
    }:null,'开始任务');
    const dialog=$('#modal');dialog.classList.add('transfer-sheet','cloud-generate-sheet');
    dialog.querySelectorAll('[name="TargetMount"]').forEach(input=>input.onchange=()=>{
      dialog.querySelector(`[data-target-path="${input.value}"]`).hidden=!input.checked;
      dialog.querySelector('[data-picker-close]')?.click();
    });
    dialog.querySelector('[name="SourceMountID"]').onchange=()=>dialog.querySelector('[data-picker-close]')?.click();
    const chooseMount=event=>{
      const control=event.target.closest('[data-pick-directory],[data-path-input]');if(!control)return;
      const name=control.dataset.pickDirectory||control.dataset.pathInput;
      const id=name==='Source'?dialog.querySelector('[name="SourceMountID"]').value:name.slice(6);
      const mount=options.Mounts.find(m=>m.ID===id);pickerMount.ID=mount?.ID||'';pickerMount.Name=mount?.Name||'网盘';
    };
    if(ready){
      dialog.addEventListener('click',chooseMount,true);
      dialog.addEventListener('close',()=>dialog.removeEventListener('click',chooseMount,true),{once:true});
      CloudMounts.bindDirectoryPicker(dialog,pickerMount,options.CacheRoot,{newCloudPaths:['/AI Emby/原始资源','/AI Emby/处理完成']});
    }
  }
  async function details(id) {
    if(!data)data=await api(base);
    const task=await api.query(base+'/task',{ID:id});
    fmDialog('资源搬运详情','<div data-transfer-detail></div>',null);
    const dialog=$('#modal');dialog.classList.add('transfer-sheet','transfer-detail-sheet');
    let poll, serial=0;const container=dialog.querySelector('[data-transfer-detail]');
    const visible=()=>container.isConnected&&dialog.open;
    function draw(task) {
      if(!visible())return;
      container.innerHTML=`<div class="transfer-detail-heading"><div><h3>${esc(task.Config.Title)}</h3><span>${esc(task.Folder)}</span></div><span class="transfer-badge is-${esc(task.State)}">${states[task.State]||esc(task.State)}</span></div><div class="transfer-detail-summary"><strong>${task.Done} / ${task.Total||'待解析'} <span>个视频完成</span></strong><span>${esc(stages[task.Stage]||task.Stage)}</span></div>${task.Error?`<p class="transfer-error" role="alert">${esc(task.Error)}</p>`:''}<div class="transfer-detail-actions">${actions(task).replace(/<button[^>]*data-detail[^>]*>.*?<\/button>/,'')}</div>${['paused','error','cancelled'].includes(task.State)?`<div class="transfer-options-grid transfer-resume-options">${field('ResumeConcurrency','继续时的文件并发',task.Config.Concurrency,'number','required min="1" max="4"')}${field('ResumeCacheLimit','临时空间上限（GiB）',task.Config.CacheLimitGB,'number','required min="1" max="100000"')}</div>`:''}<div class="transfer-files"><table><thead><tr><th>视频文件</th><th>阶段</th><th>进度</th><th>上传结果</th></tr></thead><tbody>${task.Files.length?task.Files.map(file=>`<tr><td><strong>${esc(file.Relative)}</strong><small>${esc(file.Output)}</small>${file.Error?`<p class="transfer-error">${esc(file.Error)}</p>`:''}</td><td><span class="transfer-badge is-${esc(file.State)}">${file.State==='error'?'失败':file.State==='complete'?'已完成':stages[file.Stage]||esc(file.Stage)}</span></td><td>${file.State==='complete'?size(file.OutputSize):file.Stage==='download'?`${size(file.Bytes)} / ${size(file.SourceSize)}`:file.Stage==='upload'?`${size(file.Bytes)} / ${size(file.OutputSize)}`:file.Stage==='remux'?size(file.Bytes):'—'}</td><td>${task.Config.Targets.map(target=>`<span class="transfer-upload-result">${esc(mountName(target.MountID))} · ${file.Uploads?.[target.MountID]==='complete'?'已确认':file.Uploads?.[target.MountID]==='pending'?'上传 / 等待确认':'待上传'}</span>`).join('')}</td></tr>`).join(''):'<tr><td colspan="4">正在解析分享并生成文件列表…</td></tr>'}</tbody></table></div><details class="transfer-path-details"><summary>存放目录与处理设置</summary><dl><dt>临时目录</dt><dd>${esc(task.CachePath)}</dd><dt>转存目录</dt><dd>${esc(task.Config.SourcePath)}/${esc(task.Folder)}</dd>${task.Config.Targets.map(target=>`<dt>${esc(mountName(target.MountID))}</dt><dd>${esc(target.Path)}/${esc(task.Folder)}</dd>`).join('')}<dt>处理设置</dt><dd>${esc(task.Config.Format.toUpperCase())} · 并发 ${task.Config.Concurrency} · 临时空间上限 ${task.Config.CacheLimitGB} GiB · ${task.Config.KeepLocal?'保留本地文件':'上传完成后清理临时文件'}</dd></dl></details>`;
      container.querySelectorAll('[data-action]').forEach(button=>button.onclick=run(async()=>{
        button.disabled=true;try {
          const request={ID:id,Action:button.dataset.action};
          if(request.Action==='resume'){
            const concurrency=container.querySelector('[name="ResumeConcurrency"]'),space=container.querySelector('[name="ResumeCacheLimit"]');
            if(!concurrency.reportValidity()||!space.reportValidity())return;
            request.Concurrency=Number(concurrency.value);request.CacheLimitGB=Number(space.value);
          }
          await api(base+'/action','POST',request);await read();if(current())await refresh()}finally{if(button.isConnected)button.disabled=false}
      }));
    }
    async function read() {
      if(!visible())return;
      clearTimeout(poll);const ticket=++serial;
      const next=await api.query(base+'/task',{ID:id});
      if(ticket!==serial||!visible())return;
      draw(next);if(active(next))poll=setTimeout(()=>read().catch(error=>{if(visible())toast(error.message,'error')}),2500);
    }
    draw(task);if(active(task))poll=setTimeout(()=>read().catch(error=>{if(visible())toast(error.message,'error')}),2500);
    dialog.addEventListener('close',()=>{clearTimeout(poll);serial++},{once:true});
  }
  return {load,create,details};
})();
