/* Local management frontend. Backend endpoints and media playback stay independent. */
const Panel = (() => {
  const pages = {
    0: ['libraries','媒体库','CONTENT'],
    1: ['users','用户与权限','SYSTEM'],
    2: ['order','媒体库','CONTENT'],
    3: ['media-info','媒体信息','PROCESSING'],
    4: ['general','基础设置','SYSTEM'],
    5: ['api','API 密钥','SYSTEM'],
    6: ['tmdb','外部服务','CONNECTIONS'],
    7: ['telegram','外部服务','CONNECTIONS'],
    8: ['scraper','元数据刮削','PROCESSING'],
    9: ['subtitles','播放增强','PLAYBACK'],
    10: ['chapters','播放增强','PLAYBACK'],
    11: ['proxy','外部服务','CONNECTIONS'],
    12: ['overview','控制台','OVERVIEW'],
    13: ['system','系统与更新','SYSTEM'],
    14: ['tasks','任务中心','PROCESSING'],
    15: ['schedule','定时任务','PROCESSING'],
    16: ['library-policy','媒体库设置','CONTENT'],
    17: ['playback','播放与记录','PLAYBACK'],
    18: ['cache','缓存管理','PLAYBACK'],
    19: ['devices','登录设备','SYSTEM'],
    20: ['import','资料导入','CONTENT'],
    21: ['access','访问与网络','SYSTEM'],
    22: ['covers','封面展示','CONTENT'],
    23: ['cloud','网盘挂载','CONTENT']
  };
  const groups = [
    ['工作空间', [['控制台','dashboard',12]]],
    ['内容管理', [['媒体库','media',0,[0,2]],['网盘挂载','network',23],['每库设置','settings',16],['封面展示','media',22],['资料导入','refresh',20],['文件管理','files','files']]],
    ['处理任务', [['任务中心','sort',14],['定时任务','refresh',15],['元数据刮削','scraper',8],['媒体信息','info',3],['实时日志','info','logs']]],
    ['播放管理', [['播放与记录','chapter',17],['缓存管理','files',18],['播放增强','chapter',9,[9,10]]]],
    ['系统设置', [['基础设置','settings',4],['外部服务','network',6,[6,7,11]],['用户与权限','users',1],['登录设备','users',19],['访问与网络','network',21],['API 密钥','api',5],['系统与更新','refresh',13]]]
  ];
  let current = 12;
  function sectionFromHash() {
    const key = location.hash.slice(7).split('/')[0];
    return Number(Object.keys(pages).find(n => pages[n][0] === key) ?? 12);
  }
  function folderFromHash() {
    if(!location.hash.startsWith('#admin/libraries/'))return '';
    try{return decodeURIComponent(location.hash.slice(17))}catch{return ''}
  }
  function libraryDetails(lib) {
    const title=$('#panel-page-heading h1');
    if(title){title.innerHTML=`<span class="${lib.Hidden?'library-hidden':''}"><span class="library-name">${esc(lib.Name)}</span></span>`}
    document.title='媒体库目录 · '+(serverName||'AI Emby');
    const hash='#admin/libraries/'+encodeURIComponent(lib.Id);
    if(location.hash!==hash)history.pushState({page:'admin',library:lib.Id},'',hash);
  }
  function drawer() {
    return `<button id="drawer-backdrop" type="button" aria-label="关闭导航" onclick="toggleDrawer(false)"></button><aside id="drawer" aria-label="管理导航"><nav class="panel-navigation">${groups.map(([name,links])=>`<div class="nav-group"><p>${name}</p>${links.map(([label,icon,target,active])=>`<button type="button" data-panel-target="${target}" ${active?`data-panel-indices="${active.join(',')}"`:''} onclick="${target==='files'?'filesPage(\'\')':target==='logs'?'showLogs()':`navigateAdminSection(${target})`}">${drawerIcon(icon)}<span>${label}</span><span class="nav-active-mark" aria-hidden="true"></span></button>`).join('')}</div>`).join('')}</nav></aside>`;
  }
  function syncNavigation() {
    document.querySelectorAll('[data-panel-target]').forEach(button=>{
      const target=button.dataset.panelTarget;
      const active=view==='files'?target==='files':view==='admin'&&(button.dataset.panelIndices||target).split(',').includes(String(current));
      if(active)button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');
    });
  }
  function applyTheme() {
    const management=['admin','files','login','password'].includes(view);
    document.body.classList.toggle('panel-view',management);
    document.body.classList.toggle('entry-view',view==='login'||view==='password');
    document.documentElement.dataset.theme=localStorage.getItem(appearanceThemeKey)||'light';
  }
  function header() {
    let host=$('#panel-page-heading');
    if(!host){host=document.createElement('div');host.id='panel-page-heading';$('#app').prepend(host)}
    return host;
  }
  function activate(n, route=true) {
    current=n;
    const [key,title]=pages[n]||pages[12];
    const tabs=[0,2].includes(n)?[[0,'媒体库'],[2,'展示顺序']]:[9,10].includes(n)?[[9,'字幕'],[10,'片头片尾']]:[6,7,11].includes(n)?[[6,'TMDB'],[7,'Telegram Bot'],[11,'网络代理']]:[];
    const overview=n===12;
    header().innerHTML=`<div class="page-heading"><h1 tabindex="-1">${title}</h1>${overview?`<div class="page-heading-actions"><button type="button" class="secondary" onclick="navigateAdminSection(0)">${drawerIcon('media')}管理媒体库</button><button type="button" onclick="navigateAdminSection(8)">${drawerIcon('scraper')}元数据刮削</button></div>`:''}</div>${tabs.length?`<nav class="section-tabs" aria-label="${title}">${tabs.map(([index,label])=>`<button type="button" aria-current="${n===index?'page':'false'}" onclick="navigateAdminSection(${index})">${label}</button>`).join('')}</nav>`:''}`;
    document.body.dataset.panelSection=key;
    document.title=`${title} · ${serverName||'AI Emby'}`;
    syncNavigation();
    if(route&&location.hash!==`#admin/${key}`)history.pushState({page:'admin',section:n},'',`#admin/${key}`);
    window.scrollTo(0,0);
  }
  function tabs(host, items, name) {
    const nav=document.createElement('div');nav.className='section-tabs local-tabs';nav.setAttribute('role','tablist');nav.setAttribute('aria-label',name);
    const activate=index=>items.forEach(([label,pane],i)=>{const button=nav.children[i];pane.hidden=i!==index;button.setAttribute('aria-selected',String(i===index));button.tabIndex=i===index?0:-1});
    items.forEach(([label,pane],i)=>{
      pane.id=pane.id||`panel-${name}-${i}`;pane.setAttribute('role','tabpanel');
      const b=document.createElement('button');b.type='button';b.textContent=label;b.id=`${pane.id}-tab`;b.setAttribute('role','tab');b.setAttribute('aria-controls',pane.id);pane.setAttribute('aria-labelledby',b.id);b.onclick=()=>activate(i);
      b.onkeydown=e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const next=e.key==='Home'?0:e.key==='End'?items.length-1:(i+(e.key==='ArrowRight'?1:-1)+items.length)%items.length;activate(next);nav.children[next].focus()};nav.append(b);
    });
    host.prepend(nav);activate(0);return activate;
  }
  function card(title) {
    const c=document.createElement('section');c.className='setting-card';c.innerHTML=`<div class="card-heading"><h3>${title}</h3></div>`;return c;
  }
  // Move existing controls, preserving form membership, IDs and bound handlers.
  function organizeForm(form, definitions) {
    if(!form||form.querySelector(':scope > .setting-card'))return;
    form.dataset.organized='true';
    const children=[...form.children];
    const starts=definitions.map(([selector,title])=>{
      let node=form.querySelector(selector);if(!node)return null;while(node.parentElement!==form)node=node.parentElement;
      return {index:children.indexOf(node),title};
    }).filter(Boolean).sort((a,b)=>a.index-b.index);
    if(!starts.length)return;
    starts[0].index=0;
    const footer=document.createElement('div');footer.className='settings-footer';
    let active;
    children.forEach((node,index)=>{
      const start=starts.find(x=>x.index===index);
      if(start){active=card(start.title);form.append(active)}
      if(node.matches('.tmdb-actions,.form-actions')||node.matches('button:not([type="button"])'))footer.append(node);
      else (active||form).append(node);
    });
    if(footer.children.length)form.append(footer);
    if(!form.dataset.validityBound){
      form.dataset.validityBound='true';
      form.addEventListener('invalid',e=>{
        const pane=e.target.closest('[role="tabpanel"]');
        if(pane?.hidden)document.getElementById(pane.getAttribute('aria-labelledby'))?.click();
      },true);
    }
  }
  function prepareForm(id) {
    const form=document.getElementById(id);
    if(!form)return;
    delete form.dataset.tabbed;
    const specs={
      enhancements:[['[name="serverName"]','服务名称'],['.favorite-cover-control','显示与检索'],['[name="mergeFolder"]','媒体版本与同步'],['fieldset','播放路径'],['[name="proxyDebug"]','开发诊断']],
      'tmdb-settings':[['[name="Enabled"]','按需元数据'],['[name="APIBase"]','服务连接'],['[name="Directory"]','缓存与频率']],
      'subtitle-settings':[['[name="enabled"]','字幕体验'],['.tmdb-token-field','服务认证'],['[name="directory"]','缓存与保存']],
      'intro-settings':[['[name="enabled"]','学习与跳过'],['[name="window"]','识别参数'],['[name="directory"]','数据保存']],
      'proxy-settings':[['[name="enabled"]','代理连接'],['[name="username"]','连接认证'],['fieldset','代理范围']],
      'telegram-settings':[['[name="enabled"]','启用机器人'],['.telegram-field','连接信息'],['.telegram-notify','通知与验证']]
    };
    organizeForm(document.getElementById(id),specs[id]||[]);
    if(id==='enhancements'){
      const f=document.getElementById(id);if(f.dataset.tabbed)return;f.dataset.tabbed='true';
      const cards=[...f.querySelectorAll(':scope > .setting-card')];
      const initials=f.querySelector('[name="initials"]')?.closest('label');
      if(initials){const help=initials.nextElementSibling;cards[1].append(initials);if(help?.tagName==='P')cards[1].append(help)}
      const panes=['显示与检索','媒体同步','播放与诊断'].map(()=>{const p=document.createElement('div');p.className='settings-pane';f.insertBefore(p,f.querySelector('.settings-footer'));return p});
      cards.forEach((c,i)=>panes[i<=1?0:i===2?1:2].append(c));tabs(f,panes.map((p,i)=>[['显示与检索','媒体同步','播放与诊断'][i],p]),'基础设置');
    }
  }
  function prepareMedia() {
    const host=$('#media-content');if(!host||host.dataset.organized)return;host.dataset.organized='true';
    const task=document.createElement('div'),settings=document.createElement('div');task.className=settings.className='settings-pane';
    const automation=card('任务与自动处理');automation.append($('#media-automation'));
    const queue=card('提取队列');queue.append($('#media-queue'));task.append(automation,queue);
    const config=card('提取与保存');config.append($('#media-settings'));
    const concurrency=card('执行并发');concurrency.append($('#media-concurrency'));settings.append(config,concurrency);host.append(task,settings);
    tabs(host,[['任务与队列',task],['提取设置',settings]],'媒体信息');
  }
  function prepareScraper(host) {
    tabs(host,[['手动任务',host.querySelector('[data-scraper-pane="manual"]')],['待处理',host.querySelector('[data-scraper-pane="issues"]')],['实时监控',host.querySelector('[data-scraper-pane="monitor"]')],['刮削配置',host.querySelector('[data-scraper-pane="settings"]')]],'刮削');
    const summary=host.querySelector('.scraper-master');host.prepend(summary);
  }
  async function loadModule(n, loader) {
    const section=document.querySelectorAll('.admin-section')[n];
    section.querySelector(':scope > .module-load-status')?.remove();
    const status=UI.el('div',{class:'module-load-status',role:'status'},'正在加载…');
    section.prepend(status);
    section.classList.add('module-loading');
    try {await loader();status.remove()}
    catch(error){
      if(status.isConnected){status.setAttribute('role','alert');status.replaceChildren(document.createTextNode('读取失败：'+error.message+' '));status.append(UI.el('button',{type:'button',class:'secondary',onclick:()=>adminSection(n)},'重新加载'))}
    }
    finally{if(!section.querySelector(':scope > .module-load-status:not([role="alert"])'))section.classList.remove('module-loading')}
  }
  function system() {
    const s=document.querySelectorAll('.admin-section')[13];
    s.innerHTML=`<div class="system-layout"><section class="setting-card system-edition"><div class="card-heading"><h3>版本信息</h3></div><dl><div><dt>前端主题</dt><dd>Azure · 浅蓝白</dd></div><div><dt>当前版本</dt><dd>${esc(buildVersion)}</dd></div><div><dt>更新仓库</dt><dd><a href="https://github.com/LLL198/ai-emby" target="_blank" rel="noopener noreferrer">LLL198/ai-emby</a></dd></div></dl></section><section class="setting-card"><div class="card-heading"><h3>系统更新</h3></div><p id="system-update-status" role="status">读取更新服务状态…</p><button type="button" onclick="run(checkUpdates)()">检测更新 ${drawerIcon('refresh')}</button></section></div>`;
    api('/System/Updates/Status').then(status=>{const node=s.querySelector('#system-update-status');if(node)node.textContent=status.State==='failed'?status.Message:status.State==='idle'||status.State==='succeeded'?(status.Enabled?'更新服务已连接':'宿主机更新服务未启用'):status.Message||'更新任务正在执行'}).catch(error=>{const node=s.querySelector('#system-update-status');if(node)node.textContent=error.message});
  }
  function filterLogs() {
    const q=($('#log-search')?.value||'').trim().toLowerCase();
    const onlyErrors=$('#log-errors')?.checked;
    document.querySelectorAll('#log-content .log-entry').forEach(entry=>{
      entry.hidden=!!((q&&!entry.textContent.toLowerCase().includes(q))||(onlyErrors&&!entry.dataset.error));
    });
    const visible=[...document.querySelectorAll('#log-content .log-entry')].filter(e=>!e.hidden).length;
    const count=$('#log-count');if(count)count.textContent=`显示 ${visible} 条`;
  }
  document.addEventListener('keydown',e=>{if(e.key==='Escape'&&document.body.classList.contains('drawer-open')){toggleDrawer(false);$('#hamburger')?.focus()}});
  return {pages,drawer,activate,sectionFromHash,folderFromHash,libraryDetails,syncNavigation,applyTheme,prepareForm,prepareMedia,prepareScraper,loadModule,system,filterLogs};
})();
