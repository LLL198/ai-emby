const CloudMounts = (() => {
  const base = '/admin/features/cloud';
  const labels = {
    address:'WebDAV 地址', root_folder_path:'根目录路径', vendor:'服务类型', tls_insecure_skip_verify:'跳过证书校验',
    authorization:'Authorization', username:'登录账号', password:'密码', sms_code:'短信验证码', mail_cookies:'移动邮箱 Cookie',
    cookie:'Cookie', qrcode_token:'二维码登录令牌', qrcode_source:'登录设备', root_folder_id:'根目录 ID', root_path:'根目录路径',
    mobile_request_url:'移动端请求 URL（修复夸克 302）',
    access_token:'Access Token', refresh_token:'Refresh Token', phone_number:'手机号码', captcha_token:'验证码令牌',
    send_code:'保存时发送短信验证码', verify_code:'短信验证码', client_id:'Client ID', device_id:'设备 ID', device_sign:'设备签名',
    type:'空间类型', link_id:'分享链接 ID', cloud_id:'家庭 / 群空间 ID', user_domain_id:'用户域 ID',
    use_transcoding_address:'使用网盘转码直链（适合 302 播放）', only_list_video_file:'只列出视频文件',
    page_size:'每页文件数量', limit_rate:'网盘请求间隔（秒）', order_by:'排序字段', order_direction:'排序方向', sort_type:'排序方式',
    custom_upload_part_size:'上传分片大小', report_real_size:'获取真实文件大小', use_large_thumbnail:'使用大缩略图', use_old_stream_upload:'使用旧版上传接口',
  };
  const primary = {
    '139Yun':['authorization','root_folder_id','type'],
    '115 Cloud':['cookie','root_folder_id'],
    '115 Open':['access_token','refresh_token','root_folder_id'],
    Quark:['cookie','root_folder_id','use_transcoding_address','mobile_request_url'],
    WebDav:['address','username','password','root_folder_path'],
    GuangYaPan:['client_id','phone_number','captcha_token','send_code','verify_code','access_token','refresh_token','root_path'],
  };
  const tips = {
    '139Yun':'可填写移动云盘 Authorization；使用邮箱登录时，在高级设置里填写移动邮箱 Cookie 和账号信息。',
    '115 Cloud':'填写已登录账号的 Cookie。115 的直链与播放客户端 User-Agent 绑定。',
    '115 Open':'使用 115 开放平台授权得到的 Access Token 和 Refresh Token。',
    Quark:'302 出现 plf_invalid 时，填写登录自己账号后捕获的 https://drive-m.quark.cn/1/clouddrive/… 请求 URL（需含 kps、sign、vcode、ut）。保存后沿用已有 STRM。留空保留已保存凭据，过期时重新获取并替换。',
    WebDav:'填写 WebDAV 地址、账号和密码，根目录默认 /。文件通过服务器读取，播放流量会经过 AI Emby。',
    GuangYaPan:'可使用 Token 登录；短信登录先填手机号和 Client ID，勾选发送短信后保存，再编辑挂载填写短信验证码。',
  };
  const sensitive = name => name==='mobile_request_url'||/token|cookie|password|authorization|verify_code|sms_code|device_sign|verification_id|username|phone_number/i.test(name);
  const states = {waiting:'等待中',running:'生成中',complete:'已完成',error:'失败',cancelled:'已取消',interrupted:'已中断'};
  const active = task => ['waiting','running','counting'].includes(task.State);
  let host, still, data, selected, currentPath='/', page=1, listing, browseSerial=0, loadSerial=0, pollTimer;
  const icon = (name) => typeof fmIcon==='function' ? fmIcon(name) : '';
  const size = n => n>=1073741824 ? (n/1073741824).toFixed(1)+' GB' : n>=1048576 ? (n/1048576).toFixed(0)+' MB' : '—';
  const join = (parent,name) => (parent==='/'?'':parent)+'/'+name;
  const parent = p => p.slice(0,p.lastIndexOf('/')) || '/';
  const field = (name,title,value='',type='text',extra='') => `<label>${esc(title)}<input name="${esc(name)}" type="${type}" value="${esc(value)}" ${extra}></label>`;

  async function load(target,isCurrent) {
    const serial=++loadSerial;
    clearTimeout(pollTimer);host=target;still=isCurrent;
    const result = await api(base);if(serial!==loadSerial||!isCurrent())return;data=result;
    selected=data.Mounts.find(m=>m.ID===selected?.ID) || data.Mounts.find(m=>m.Enabled) || data.Mounts[0];
    render();if(selected?.Enabled) await browse(currentPath,page);
    schedule();
  }
  function render() {
    host.innerHTML=`<div class="cloud-toolbar"><div><h2>我的网盘 <span class="cloud-count">${data.Mounts.length}</span></h2></div><div class="cloud-actions"><button type="button" class="secondary" data-refresh>刷新</button><button type="button" data-add>＋ 添加网盘</button></div></div>
      ${!data.EngineReady?'<p class="cloud-notice" role="status">网盘引擎正在连接，请稍后刷新。</p>':''}
      ${data.Mounts.length ? `<div class="cloud-mount-grid">${data.Mounts.map((m,i)=>`<article class="cloud-mount ${m.ID===selected?.ID?'is-selected':''}" data-card="${i}"><button type="button" class="cloud-mount-select" data-select="${i}"><span class="cloud-drive-symbol" aria-hidden="true">${icon('folder')}</span><span><strong>${esc(m.Name)}</strong><small>${esc(data.Drivers[m.Driver]||m.Driver)}</small></span><span class="cloud-status ${m.Status==='已连接'?'is-connected':''}">${esc(m.Status)}</span></button><div class="cloud-mount-actions"><button type="button" class="secondary" data-edit="${i}">配置</button><button type="button" class="secondary" data-toggle="${i}">${m.Enabled?'暂停':'启用'}</button><button type="button" class="cloud-remove" data-delete="${i}" aria-label="移除 ${esc(m.Name)}">移除</button></div></article>`).join('')}</div>` : `<div class="cloud-empty">${icon('folder')}<h3>连接你的影视网盘</h3><p>移动云盘 · 115 · 夸克 · 光鸭 · WebDAV</p><button type="button" data-empty-add>添加第一个网盘</button></div>`}
      <section class="cloud-browser" ${selected?'':'hidden'}><div class="cloud-browser-head"><h3>网盘文件</h3><button type="button" data-generate ${!selected?.Enabled?'disabled':''}>生成 STRM</button></div><div data-browser></div></section>
      <section class="cloud-task-section"><h3>生成记录</h3><div data-tasks></div></section><footer class="cloud-credits"><a href="/web/cloud-engine-notice.html" target="_blank" rel="noopener noreferrer">网盘组件许可与源码</a></footer>`;
    host.querySelector('[data-add]').onclick=run(()=>edit());host.querySelector('[data-empty-add]')?.addEventListener('click',run(()=>edit()));
    host.querySelector('[data-refresh]').onclick=run(()=>load(host,still));host.querySelector('[data-generate]').onclick=run(generate);
    host.querySelectorAll('[data-select]').forEach(b=>b.onclick=run(async()=>{selected=data.Mounts[Number(b.dataset.select)];currentPath='/';page=1;render();if(selected.Enabled)await browse('/',1);else host.querySelector('[data-browser]').innerHTML='<p class="cloud-empty-small">挂载已暂停，启用后即可浏览。</p>';}));
    host.querySelectorAll('[data-edit]').forEach(b=>b.onclick=run(()=>edit(data.Mounts[Number(b.dataset.edit)])));
    host.querySelectorAll('[data-toggle]').forEach(b=>b.onclick=run(()=>action(data.Mounts[Number(b.dataset.toggle)],'toggle')));
    host.querySelectorAll('[data-delete]').forEach(b=>b.onclick=run(()=>action(data.Mounts[Number(b.dataset.delete)],'delete')));
    renderTasks(data.Tasks);if(selected&&!selected.Enabled)host.querySelector('[data-browser]').innerHTML='<p class="cloud-empty-small">挂载已暂停，启用后即可浏览。</p>';
  }
  async function action(m,action) {
    if(action==='delete' && !(await confirmDialog('移除网盘',`移除「${m.Name}」后，该挂载生成的 STRM 链接将停用。云端文件和本地 STRM 文件会保留。`,'移除')))return;
    await api(base+'/action','POST',{ID:m.ID,Action:action==='toggle'?(m.Enabled?'disable':'enable'):action});
    if(action==='delete'&&m.ID===selected?.ID){selected=null;currentPath='/';page=1;}await load(host,still);
  }
  async function edit(m) {
    const drivers=Object.entries(data.Drivers);
    let schema,driver=m?.Driver||'139Yun',serial=0;
    const mode=m?.PlaybackMode||(driver==='WebDav'?'proxy':'redirect');
    fmDialog(m?'编辑网盘':'添加网盘',`<section class="cloud-sheet-block"><h3>基本信息</h3><div class="feature-grid">${field('Name','挂载名称',m?.Name||'','text','required maxlength="256"')}<label>网盘类型<select name="Driver" ${m?'disabled':''}>${drivers.map(([id,title])=>`<option value="${esc(id)}" ${id===driver?'selected':''}>${esc(title)}</option>`).join('')}</select></label><label class="cloud-span">播放方式<select name="PlaybackMode" ${driver==='WebDav'?'disabled':''}><option value="redirect" ${mode==='redirect'?'selected':''}>直链 302（视频不经过服务器）</option><option value="proxy" ${mode==='proxy'?'selected':''}>服务器中转（消耗服务器流量）</option></select></label></div></section><section class="cloud-sheet-block cloud-account-fields" data-fields></section>`,async form=>{
      if(!schema) return false;
      const addition={};for(const f of schema.Fields){if(f.name==='verification_id')continue;const v=form.get('account:'+f.name);if(sensitive(f.name)&&!v)continue;addition[f.name]=f.type==='bool'?form.has('account:'+f.name):['number','float'].includes(f.type)?Number(v):String(v??'');}
      const saved=await api(base+'/save','POST',{ID:m?.ID||'',Name:form.get('Name'),Driver:driver,PlaybackMode:form.get('PlaybackMode')||(driver==='WebDav'?'proxy':'redirect'),Addition:addition});
      toast(saved.Message,{type:saved.Connected?'success':'info'});selected={ID:saved.ID};currentPath='/';page=1;await load(host,still);
    },'保存挂载');
    const dialog=document.getElementById('modal'), fields=dialog.querySelector('[data-fields]');
    const playback=dialog.querySelector('[name="PlaybackMode"]');
    const applyPlayback=()=>{for(const name of ['use_transcoding_address','mobile_request_url']){const input=fields.querySelector(`[name="account:${name}"]`);if(input){const proxy=driver==='Quark'&&playback.value==='proxy';input.disabled=proxy;input.closest('label').hidden=proxy;}}};
    playback.onchange=applyPlayback;
    dialog.classList.add('cloud-sheet','cloud-account-sheet');
    const draw=async()=>{
      const ticket=++serial;schema=null;fields.innerHTML='<p role="status">读取账号配置…</p>';
      const s=await api(base+'/fields?'+new URLSearchParams({Driver:driver,ID:m?.ID||''}));if(ticket!==serial||!dialog.open)return;schema=s;
      const items=s.Fields.filter(f=>f.name!=='verification_id'&&!(driver==='WebDav'&&f.name==='tls_insecure_skip_verify'));
      const html=f=>{
        const saved=s.Saved.includes(f.name), name='account:'+f.name, title=labels[f.name]||f.name.replaceAll('_',' ');
        let value=s.Values[f.name] ?? (sensitive(f.name)?'':f.default);
        if(!m&&driver==='Quark'&&f.name==='use_transcoding_address')value=true;
        const required=f.required&&!saved?'required':'', placeholder=saved?'placeholder="已保存，留空保留"':'';
        if(f.type==='bool')return `<label class="feature-check cloud-option cloud-span"><span>${esc(title)}</span><input name="${esc(name)}" type="checkbox" ${value===true||value==='true'?'checked':''}></label>`;
        if(f.type==='select'){
          const options=f.options.split(',');if(value&&!options.includes(String(value)))options.unshift(String(value));
          const names={other:'通用 WebDAV',sharepoint:'SharePoint',personal_new:'个人空间（新版）',personal:'个人空间',family:'家庭空间',group:'群空间',share:'分享空间',asc:'升序',desc:'降序'};
          return `<label>${esc(title)}<select name="${esc(name)}" ${required}>${options.map(v=>`<option value="${esc(v)}" ${String(value)===v?'selected':''}>${esc(names[v]||v)}</option>`).join('')}</select></label>`;
        }
        const addressHint=driver==='WebDav'&&f.name==='address'?'placeholder="https://dav.example.com/dav/"':'';
        const markup=field(name,title,value??'',sensitive(f.name)?'password':['number','float'].includes(f.type)?'number':'text',`${required} ${placeholder} ${addressHint} autocomplete="off" ${['number','float'].includes(f.type)?'min="0" step="any"':''}`);
        return f.name==='mobile_request_url'?markup.replace('<label>','<label class="cloud-span">'):markup;
      };
      const common=items.filter(f=>primary[driver]?.includes(f.name)), advanced=items.filter(f=>!primary[driver]?.includes(f.name));
      fields.innerHTML=`<h3>账号与目录</h3><p class="cloud-account-tip">${esc(tips[driver])}</p><div class="feature-grid">${common.map(html).join('')}</div>${advanced.length?`<details class="cloud-advanced"><summary>高级配置</summary><div class="feature-grid">${advanced.map(html).join('')}</div></details>`:''}`;
      applyPlayback();
      fields.addEventListener('invalid',e=>{const details=e.target.closest('details');if(details)details.open=true},true);
    };
    dialog.querySelector('[name="Driver"]').onchange=run(async e=>{driver=e.target.value;playback.disabled=driver==='WebDav';playback.value=driver==='WebDav'?'proxy':'redirect';await draw()});await draw();
  }
  async function browse(p='/',n=1,refresh=false) {
    const ticket=++browseSerial,mount=selected;const target=host.querySelector('[data-browser]');if(!mount||!target)return;
    host.querySelector('[data-generate]').disabled=true;
    target.innerHTML='<p class="cloud-empty-small" role="status">读取目录…</p>';
    try {
      const b=await api(base+'/list?'+new URLSearchParams({ID:mount.ID,Path:p,Page:n,Refresh:refresh}));
      if(!still()||ticket!==browseSerial||selected?.ID!==mount.ID)return;
      listing=b;currentPath=b.Path;page=b.Page;
      host.querySelector('[data-generate]').disabled=false;
      const parts=currentPath.split('/').filter(Boolean),crumbs=[`<button type="button" data-path="/">${esc(mount.Name)}</button>`];
      parts.forEach((part,i)=>crumbs.push(`<span aria-hidden="true">/</span><button type="button" data-path="${esc('/'+parts.slice(0,i+1).join('/'))}">${esc(part)}</button>`));
      target.innerHTML=`<div class="cloud-pathbar"><nav aria-label="网盘目录">${crumbs.join('')}</nav><button type="button" class="secondary" data-reload>刷新目录</button></div><div class="cloud-file-table"><table><thead><tr><th>名称</th><th>大小</th><th>类型</th></tr></thead><tbody>${b.Items.length?b.Items.map((f,i)=>`<tr><td>${f.is_dir?`<button type="button" class="cloud-file-name" data-directory="${i}">${icon('folder')}<span>${esc(f.name)}</span><span class="cloud-arrow">›</span></button>`:`<span class="cloud-file-name">${icon('file')}<span>${esc(f.name)}</span></span>`}</td><td>${f.is_dir?'—':size(f.size)}</td><td>${f.is_dir?'文件夹':esc(f.name.split('.').pop().toUpperCase())}</td></tr>`).join(''):'<tr><td colspan="3" class="cloud-empty-small">目录为空</td></tr>'}</tbody></table></div><div class="cloud-pagination"><span>共 ${b.Total} 项 · 第 ${b.Page} 页</span><div><button type="button" class="secondary" data-up ${currentPath==='/'?'disabled':''}>上一级</button><button type="button" class="secondary" data-prev ${page===1?'disabled':''}>上一页</button><button type="button" class="secondary" data-next ${page*b.PerPage>=b.Total?'disabled':''}>下一页</button></div></div>`;
      target.querySelectorAll('[data-path]').forEach(b=>b.onclick=run(()=>browse(b.dataset.path)));
      target.querySelectorAll('[data-directory]').forEach(b=>b.onclick=run(()=>browse(join(currentPath,listing.Items[Number(b.dataset.directory)].name))));
      target.querySelector('[data-up]').onclick=run(()=>browse(parent(currentPath)));target.querySelector('[data-prev]').onclick=run(()=>browse(currentPath,page-1));target.querySelector('[data-next]').onclick=run(()=>browse(currentPath,page+1));target.querySelector('[data-reload]').onclick=run(()=>browse(currentPath,page,true));
    } catch(error) {
      if(!still()||ticket!==browseSerial)return;target.innerHTML=`<div class="cloud-notice" role="alert">${esc(error.message)}<button type="button" class="secondary" data-retry>重试</button></div>`;target.querySelector('[data-retry]').onclick=run(()=>browse(p,n));
    }
  }
  function directoryField(name,title,value) {
    const id='cloud-generate-'+name.toLowerCase();
    return `<section class="cloud-directory-card"><div class="cloud-directory-label"><label for="${id}">${esc(title)}</label><button type="button" class="cloud-path-edit" data-path-edit="${name}">输入路径</button></div><div class="cloud-directory-control"><span aria-hidden="true">${icon('folder')}</span><input id="${id}" name="${name}" value="${esc(value)}" type="text" readonly required autocomplete="off" data-path-input="${name}" aria-expanded="false" aria-controls="cloud-generate-picker"><button type="button" class="secondary" data-pick-directory="${name}" aria-expanded="false" aria-controls="cloud-generate-picker">浏览选择</button></div></section>`;
  }
  function bindDirectoryPicker(dialog,mount,outputRoot,options={}) {
    const picker=dialog.querySelector('[data-directory-picker]');
    const form=dialog.querySelector('form'),submit=form.querySelector('[type="submit"]');
    const normalize=(p,local=false)=>{
      p=String(p||'');if(local)p=p.replaceAll('\\','/');
      const clean=p.split('/').filter(Boolean).join('/');
      return local&&/^[A-Za-z]:(\/|$)/.test(p)?(clean.length===2?clean+'/':clean):('/'+clean);
    };
    outputRoot=normalize(outputRoot,true);
    const insideOutput=p=>outputRoot==='/'||p===outputRoot||p.startsWith(outputRoot.replace(/\/$/,'')+'/');
    let request=0,controller,state=null;
    const inputFor=kind=>dialog.querySelector(`[name="${kind}"]`);
    const isCurrent=ticket=>ticket===request&&dialog.open&&picker.isConnected;
    function close() {
      request++;controller?.abort();picker.hidden=true;
      submit.disabled=form.getAttribute('aria-busy')==='true';
      dialog.querySelectorAll('[data-pick-directory],[data-path-input]').forEach(button=>button.setAttribute('aria-expanded','false'));
    }
    function select(p) {
      const input=inputFor(state.kind);input.value=p;input.readOnly=true;
      dialog.querySelector(`[data-path-edit="${state.kind}"]`).textContent='输入路径';
      close();dialog.querySelector(`[data-pick-directory="${state.kind}"]`).focus();
    }
    function draw() {
      const local=state.kind==='Output',root=local?outputRoot:'/',parts=state.path.split('/').filter(Boolean);
      const crumbs=[`<button type="button" data-crumb="${esc(root)}">${esc(local?'媒体目录':mount.Name)}</button>`];
      parts.forEach((name,i)=>{const p=normalize((local&&/^[A-Za-z]:/.test(state.path)?'':'/')+parts.slice(0,i+1).join('/'),local);if(local&&(p===root||!insideOutput(p)))return;crumbs.push(`<span aria-hidden="true">/</span><button type="button" data-crumb="${esc(p)}">${esc(name)}</button>`)});
      const pickerTitle=local?'选择本地输出目录':state.kind==='Source'?'选择网盘源目录':'选择上传目录';
      picker.setAttribute('aria-label',pickerTitle);
      picker.innerHTML=`<div class="cloud-picker-heading"><h3>${pickerTitle}</h3><button type="button" class="secondary" data-picker-close>收起</button></div><nav class="cloud-picker-crumbs" aria-label="${local?'本地目录':'网盘目录'}">${crumbs.join('')}</nav><div class="cloud-picker-toolbar"><button type="button" class="secondary" data-picker-up ${state.path===root?'disabled':''}>上一级</button><label class="cloud-picker-search"><input type="search" data-picker-search placeholder="筛选本页文件夹" aria-label="筛选本页文件夹" autocomplete="off"></label><button type="button" class="secondary" data-picker-refresh>刷新</button></div><div class="cloud-picker-list" data-picker-list aria-busy="${state.loading}">${state.loading?'<p class="cloud-picker-empty" role="status">正在读取文件夹…</p>':state.error?`<div class="cloud-picker-empty" role="alert">${esc(state.error)}<button type="button" class="secondary" data-picker-retry>重试</button></div>`:state.items.length?state.items.map((item,i)=>`<button type="button" class="cloud-picker-folder" data-folder="${i}">${icon('folder')}<span>${esc(item.name)}</span><span aria-hidden="true">›</span></button>`).join(''):'<p class="cloud-picker-empty">本页没有子文件夹</p>'}</div><p class="cloud-picker-empty" data-filter-empty hidden>没有匹配的文件夹</p><div class="cloud-picker-pagination"><span>${state.loading?'':`第 ${state.page} 页 · 本页 ${state.items.length} 个文件夹`}</span><div><button type="button" class="secondary" data-picker-prev ${state.loading||state.error||state.page===1?'disabled':''}>上一页</button><button type="button" class="secondary" data-picker-next ${state.loading||state.error||!state.next?'disabled':''}>下一页</button></div></div>${local?`<details class="cloud-picker-new"><summary>设置新的输出子文件夹</summary><div><input type="text" data-new-directory placeholder="文件夹名称，例如 电影" aria-label="新输出子文件夹名称" autocomplete="off"><button type="button" class="secondary" data-use-new ${state.loading||state.error?'disabled':''}>使用此子目录</button></div><p>开始生成时自动创建。</p></details>`:''}<footer class="cloud-picker-footer"><div><span>${state.fallback?'已打开上级目录':'当前目录'}</span><strong>${esc(state.path)}</strong></div><button type="button" data-use-directory ${state.loading||state.error?'disabled':''}>使用此目录</button></footer>`;
      picker.querySelector('[data-picker-close]').onclick=close;
      picker.querySelectorAll('[data-crumb]').forEach(button=>button.onclick=()=>read(state.kind,button.dataset.crumb));
      picker.querySelector('[data-picker-up]').onclick=()=>read(state.kind,parent(state.path));
      picker.querySelector('[data-picker-refresh]').onclick=()=>read(state.kind,state.path,{refresh:true});
      picker.querySelector('[data-picker-retry]')?.addEventListener('click',()=>read(state.kind,state.path));
      picker.querySelectorAll('[data-folder]').forEach(button=>button.onclick=()=>read(state.kind,state.items[Number(button.dataset.folder)].path));
      picker.querySelector('[data-picker-prev]').onclick=()=>local
        ?read(state.kind,state.path,{page:state.page-1,cursor:state.history.at(-1)||'',history:state.history.slice(0,-1)})
        :read(state.kind,state.path,{page:state.page-1});
      picker.querySelector('[data-picker-next]').onclick=()=>local
        ?read(state.kind,state.path,{page:state.page+1,cursor:state.next,history:[...state.history,state.cursor]})
        :read(state.kind,state.path,{page:state.page+1});
      picker.querySelector('[data-use-directory]').onclick=()=>select(state.path);
      picker.querySelector('[data-picker-search]').oninput=e=>{
        const search=e.target.value.trim().toLocaleLowerCase();let visible=0;
        picker.querySelectorAll('[data-folder]').forEach(button=>{button.hidden=!state.items[Number(button.dataset.folder)].name.toLocaleLowerCase().includes(search);if(!button.hidden)visible++});
        picker.querySelector('[data-filter-empty]').hidden=state.loading||!!state.error||!state.items.length||visible>0;
      };
      picker.querySelector('[data-use-new]')?.addEventListener('click',run(()=>{
        const input=picker.querySelector('[data-new-directory]'),name=input.value.trim();
        if(!name||name==='.'||name==='..'||/[\\/:*?"<>|\x00\r\n]/.test(name)||name.endsWith('.')){input.focus();throw Error('请输入有效的子文件夹名称')}
        select(join(state.path,name));
      }));
      picker.querySelectorAll('input').forEach(input=>input.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();if(input.matches('[data-new-directory]'))picker.querySelector('[data-use-new]').click()}}));
    }
    async function read(kind,p,options={}) {
      const ticket=++request;controller?.abort();controller=new AbortController();
      p=normalize(p,kind==='Output');if(kind==='Output'&&!insideOutput(p))p=outputRoot;
      state={kind,path:p,page:options.page||1,cursor:options.cursor||'',history:options.history||[],items:[],next:'',loading:true,error:'',fallback:false};
      picker.hidden=false;submit.disabled=true;
      dialog.querySelectorAll('[data-pick-directory],[data-path-input]').forEach(button=>button.setAttribute('aria-expanded',String((button.dataset.pickDirectory||button.dataset.pathInput)===kind)));
      draw();
      try {
        let result,attempt=0;
        while(true) {
          try {
            const endpoint=kind!=='Output'?base+'/list?'+new URLSearchParams({ID:mount.ID,Path:p,Page:state.page,Refresh:!!options.refresh}):'/admin/directories?'+new URLSearchParams({Path:p,After:state.cursor});
            result=await api(endpoint,'GET',undefined,{signal:controller.signal});break;
          } catch(error) {
            if(!isCurrent(ticket))return;
            if(kind!=='Output'||!options.fallback||p===outputRoot||![400,404].includes(error.status)||attempt++>=64)throw error;
            p=parent(p);state.path=p;state.fallback=true;
          }
        }
        if(!isCurrent(ticket))return;
        state.path=normalize(result.Path,kind==='Output');
        if(kind==='Output'&&!insideOutput(state.path))throw Error('输出目录必须位于媒体目录内');
        state.items=kind!=='Output'?result.Items.filter(item=>item.is_dir).map(item=>({name:item.name,path:join(state.path,item.name)})):result.Directories.map(item=>({name:item.Name,path:normalize(item.Path,true)})).filter(item=>insideOutput(item.path));
        state.next=kind!=='Output'?state.page*result.PerPage<result.Total:result.Next;
      } catch(error) {
        if(!isCurrent(ticket))return;state.error=error.message;
      } finally {
        if(isCurrent(ticket)){state.loading=false;draw()}
      }
    }
    function open(kind) {
      const value=inputFor(kind).value;
      read(kind,kind!=='Output'&&options.newCloudPaths?.includes(value)?'/':value,{fallback:kind==='Output'});
    }
    dialog.querySelectorAll('[data-pick-directory]').forEach(button=>button.onclick=()=>open(button.dataset.pickDirectory));
    dialog.querySelectorAll('[data-path-input]').forEach(input=>input.onclick=()=>{if(input.readOnly)open(input.dataset.pathInput)});
    dialog.querySelectorAll('[data-path-input]').forEach(input=>input.addEventListener('keydown',e=>{
      if(e.key==='Enter'){e.preventDefault();if(input.readOnly)input.click();else dialog.querySelector(`[data-path-edit="${input.dataset.pathInput}"]`).click()}
    }));
    dialog.querySelectorAll('[data-path-edit]').forEach(button=>button.onclick=()=>{
      close();const input=inputFor(button.dataset.pathEdit);
      if(input.readOnly){input.readOnly=false;button.textContent='完成输入';input.focus();input.select()}
      else if(input.reportValidity()){input.value=input.value.trim();if(!input.value){input.focus();return}input.readOnly=true;button.textContent='输入路径'}
    });
    dialog.addEventListener('close',close,{once:true});
  }
  async function generate() {
    if(!selected?.Enabled)return;const m=selected,source=currentPath;
    const libs=await api('/admin/features/libraries');
    const suffix=m.Name.replace(/[\\/:*?"<>|\r\n]/g,'_');
    const outputRoot=(data.OutputRoot||'/media').replace(/\/+$/,'')||'/';
    fmDialog('生成 STRM',`<div class="cloud-directory-grid">${directoryField('Source','网盘源目录',source)}${directoryField('Output','本地输出目录',join(outputRoot,'网盘/'+suffix))}</div><section id="cloud-generate-picker" data-directory-picker role="region" hidden></section><div class="feature-grid"><label class="cloud-span">AI Emby 服务地址<input name="PublicURL" type="url" required value="${esc(data.PublicURL||location.origin)}" placeholder="https://emby.example.com"></label>${field('Limit','本次最多生成（0 为不限）',0,'number','min="0" max="100000" required')}${field('Concurrency','文件写入并发',4,'number','min="1" max="8" required')}<label class="cloud-span">完成后扫描<select name="Library"><option value="">只生成 STRM</option>${libs.map(l=>`<option value="${esc(l.Id)}">${esc(l.Name)}</option>`).join('')}</select></label><label class="feature-check"><input name="Recursive" type="checkbox" checked>包含子目录</label><label class="feature-check"><input name="Overwrite" type="checkbox">覆盖已有 STRM</label></div><p class="cloud-account-tip">${m.PlaybackMode==='proxy'?'保留源目录结构，只生成视频的 STRM。服务器携带网盘认证读取视频，播放流量经过 AI Emby；账号凭据不会写入 STRM。':'保留源目录结构，只生成视频的 STRM。服务地址需能被播放设备访问，播放时获取网盘直链并 302 跳转。'}</p>`,async f=>{
      await api(base+'/generate','POST',{ID:m.ID,Source:f.get('Source'),Output:f.get('Output'),PublicURL:f.get('PublicURL'),Library:f.get('Library'),Limit:Number(f.get('Limit')),Concurrency:Number(f.get('Concurrency')),Recursive:f.has('Recursive'),Overwrite:f.has('Overwrite')});
      toast('生成任务已启动');await poll();schedule();
    },'开始生成');
    const dialog=document.getElementById('modal');dialog.classList.add('cloud-sheet','cloud-generate-sheet');bindDirectoryPicker(dialog,m,outputRoot);
  }
  function renderTasks(tasks) {
    const target=host?.querySelector('[data-tasks]');if(!target)return;
    target.innerHTML=tasks?.length?tasks.map(t=>`<article class="cloud-task"><div class="cloud-task-title"><strong>${esc(t.Name)}</strong><span class="cloud-status ${t.State==='error'?'is-error':''}">${esc(states[t.State]||t.State)}</span>${active(t)?`<button type="button" class="secondary" data-cancel="${esc(t.ItemID)}">取消</button>`:''}</div><p>${esc(t.Current||'等待生成')}</p>${t.Error?`<p class="cloud-error">${esc(t.Error)}</p>`:''}<div class="cloud-task-footer"><span>${t.Done||0} / ${t.Total||0} 个视频</span><time>${new Date(t.Started).toLocaleString()}</time></div>${active(t)?`<progress max="${Math.max(1,t.Total)}" value="${t.Done||0}" aria-label="生成进度"></progress>`:''}</article>`).join(''):'<p class="cloud-empty-small">暂无生成任务</p>';
    target.querySelectorAll('[data-cancel]').forEach(b=>b.onclick=run(async()=>{await api(base+'/cancel','POST',{ID:b.dataset.cancel});b.disabled=true;await poll()}));
  }
  async function poll() {if(!still?.())return;const b=await api(base+'/tasks');if(!still())return;data.Tasks=b.Tasks;renderTasks(data.Tasks)}
  function schedule() {clearTimeout(pollTimer);if(!still?.())return;pollTimer=setTimeout(async()=>{try{await poll()}catch{}finally{schedule()}},4000)}
  return {load,directoryField,bindDirectoryPicker};
})();
