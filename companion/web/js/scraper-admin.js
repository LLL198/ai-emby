// Shared manual planning/start/polling used by settings and the locked file dialog.
const ScraperManual = {
  async plan({ file, root, itemID, issueIDs, manualRecognition, alive = () => true, onTask = () => {} } = {}) {
    const query = new URLSearchParams({async: 'true'});
    if (file !== undefined) query.set('file', file);
    if (root !== undefined) query.set('root', root);
    const response = await api('/admin/scraper/plan?' + query, 'POST', {itemID, issueIDs, manualRecognition});
    onTask(response.TaskID || response.ID);
    if (response.ID) return response;
    const deadline = Infinity           ;
    while (alive() && Date.now() < deadline) {
      const state = await api('/admin/scraper/plan?preview=true');
      if (!state.Planning) {
        if (state.Error) throw new Error(state.Error);
        if (!state.Plan || response.TaskID && state.Plan.ID !== response.TaskID) throw new Error('计划已失效，请重新扫描');
        return {...state.Plan, TotalObjects: state.TotalObjects, FailedObjects: state.FailedObjects};
      }
      await new Promise(resolve => setTimeout(resolve, 600));
    }
    throw new Error('计划已取消或超时');
  },
  start(plan) { return api('/admin/scraper/start', 'POST', {ID: plan.ID}); },
  logs(taskID) {
    return api('/admin/logs?category=scraper').then(data => {
      const entries = (data.Entries || []).filter(x => x.Category === 'scraper' && (!taskID || x.TaskID === taskID));
      const recent = entries.slice(0, 100);
      const task = taskID && entries.find(x => x.ItemID === taskID);
      if (task && !recent.includes(task)) recent.unshift(task);
      return recent;
    });
  }
};
async function openPosterScraper(itemID) {
  const item = await api.getItem(itemID);
  if (!['Movie', 'Series'].includes(item.Type)) throw Error('只能刮削电影或剧集');
  openFileScraper({
    id:item.Id, name:item.Name,
    year:item.ProductionYear || '',
    tmdb:item.ProviderIds?.Tmdb || ''
  }, true);
}
async function openFileScraper(item, poster = false, onComplete = () => {}) {
  const content = UI.el('div', {class: 'file-scraper-content'});
  const retry = Array.isArray(item.issueIDs);
  content.innerHTML = `<p>${retry ? `重新刮削已选 ${item.issueIDs.length} 个项目` : `仅刮削：${esc(item.name)}${poster ? '' : '（' + esc(item.path) + '）'}`}</p>${poster ? `<label>片名<input name="title" maxlength="256" required value="${esc(item.name)}"></label><label>年份<input name="year" type="number" min="0" max="9999" value="${esc(item.year)}"></label><label>TMDB ID<input name="tmdb" inputmode="numeric" pattern="[0-9]*" value="${esc(item.tmdb)}"></label>` : ''}<p>使用当前刮削内容、并发和覆盖设置；关闭窗口不会取消任务。</p><div class="tmdb-actions"><button type="button" data-plan>扫描刮削任务</button><button type="button" data-start disabled>开始刮削</button></div><p role="status" data-status>扫描计划后开始刮削</p><div data-logs aria-live="polite"></div>`;
  const dialog = UI.Modal(retry ? '重新刮削' : '刮削', content);
  dialog.classList.add('file-scraper-dialog');
  const scan = content.querySelector('[data-plan]'), start = content.querySelector('[data-start]'), status = content.querySelector('[data-status]');
  let plan = null, taskID = '', timer, busy = false, started = false;
  const controls = () => { scan.disabled = busy || started; start.disabled = busy || !plan || !(plan.Pending + plan.Overwrite); };
  const poll = async () => {
    if (!dialog.open || !taskID) return;
    try {
      const entries = await ScraperManual.logs(taskID);
      if (!dialog.open) return;
      content.querySelector('[data-logs]').innerHTML = entries.map(renderScraperLog).join('') || '<p>等待任务日志…</p>';
      const task = entries.find(x => x.ItemID === taskID);
      if (started && task) {
        status.textContent = '刮削：' + (logStates[task.State] || task.State);
        if (['complete', 'error', 'cancelled'].includes(task.State)) { started = false; controls(); await onComplete(); }
      }
    } catch (e) { if (dialog.open) status.textContent = '日志读取失败：' + e.message + '，正在重试'; }
    finally { if (dialog.open) timer = setTimeout(poll, 1000); }
  };
  dialog.addEventListener('close', () => clearTimeout(timer), {once:true});
  scan.onclick = run(async () => {
    busy = true; plan = null; controls(); status.textContent = '扫描刮削任务…';
    clearTimeout(timer);
    try {
      const manualRecognition = poster ? {
        Title:content.querySelector('[name=title]').value.trim(),
        Year:Number(content.querySelector('[name=year]').value || 0),
        TMDBID:content.querySelector('[name=tmdb]').value.trim()
      } : undefined;
      if (poster && (!manualRecognition.Title || !/^[0-9]*$/.test(manualRecognition.TMDBID))) throw Error('请填写片名和有效 TMDB ID');
      plan = await ScraperManual.plan({
        ...(retry ? {issueIDs:item.issueIDs} : poster ? {itemID:item.id, manualRecognition} : {file:item.path, root:item.root}),
        alive:()=>dialog.open, onTask:id=>{taskID=id; poll();}
      });
      if (dialog.open) status.textContent = `媒体 ${plan.TotalObjects ?? plan.Objects.length} · 待刮削 ${plan.Pending} · 覆盖 ${plan.Overwrite} · 跳过 ${plan.Skipped}${plan.Disabled ? ` · 分类未启用 ${plan.Disabled}` : ''}${plan.FailedObjects ? ` · 路径不可用 ${plan.FailedObjects}` : ''}${plan.RetrySkipped ? ` · 已处理或媒体变更 ${plan.RetrySkipped}` : ''}`;
    } catch (e) { if (dialog.open) status.textContent = e.message; }
    finally { busy = false; controls(); }
  });
  start.onclick = run(async () => {
    if (!plan || busy) return;
    busy = true; controls();
    try { await ScraperManual.start(plan); started = true; plan = null; status.textContent = '刮削已开始'; }
    catch (e) { plan = null; status.textContent = '启动失败：' + e.message; }
    finally { busy = false; controls(); }
  });
  if (retry) scan.click();
  return dialog;
}

function renderScraperRecord(entry) {
  let current = entry.Current || '';
  try {
    const fields = JSON.parse(current);
    current = ['phase', 'title', 'kind', 'directory', 'recognizer', 'scraper', 'reason'].map(key => fields[key]).filter(Boolean).map(scraperLogText).join(' · ');
  } catch { current = scraperLogText(current); }
  const error = scraperLogText(entry.Error || '');
  const state = error || entry.State === 'error' ? 'error' : entry.State;
  const key = JSON.stringify([entry.TaskID, entry.ItemID, entry.Name, entry.Started]);
  return `<article class="scraper-record" data-state="${esc(state)}"><div class="scraper-record-heading"><strong>${esc(entry.Name)}</strong><span class="scraper-record-badge">${esc(logStates[state] || state || '记录')}</span><time>${esc(new Date(entry.Updated || entry.Started).toLocaleString())}</time></div>${entry.Total ? `<div class="scraper-record-progress"><progress max="${Math.max(1, Number(entry.Total) || 1)}" value="${Math.max(0, Number(entry.Done) || 0)}"></progress><span>${esc(entry.Done)}/${esc(entry.Total)}</span></div>` : ''}${current || error ? `<details class="scraper-record-details" data-log-key="${esc(key)}"><summary><span>${esc(error || current)}</span></summary><div>${current ? `<p>${esc(current)}</p>` : ''}${error && error !== current ? `<p class="scraper-record-error">${esc(error)}</p>` : ''}</div></details>` : ''}</article>`;
}

async function loadScraperSettings() {
  const result = await api('/admin/scraper');
  const host = $('#scraper-settings');
  if (!host) return;
  let scanCancelled = false;
  let namingBusy=false,namingUI=null;
  let issuesUI=null;
  let config = { MonitorAutoRefresh: true, ChineseMetadata: true, OriginalPosters: false, ...result.Settings }, plan = null, busy = false, taskActive = !!(result.Running || result.Planning);
  const kinds = { Series: '电视剧 Series', Movie: '电影 Movie', Season: '季 Season', Episode: '集 Episode' };
  const contents = { Series: ['NFO', 'Poster', 'Backdrop', 'Logo', 'Banner'], Movie: ['NFO', 'Poster', 'Backdrop', 'Logo', 'Disc', 'Banner'], Season: ['NFO', 'Poster', 'Banner'], Episode: ['NFO', 'Still'] };
  const contentName = (kind, value) => ({ NFO: 'NFO元数据', Poster: '海报', Backdrop: '背景图', Logo: 'Logo', Still: '缩略图', Disc: '光盘图（如有）', Banner: '横幅图（如有）' })[value] || value;
  host.classList.add('scraper-workspace');
  host.innerHTML = `
    <div class="scraper-master"><div class="scraper-service-title"><span class="scraper-service-icon">${fmIcon('gear')}</span><p>刮削工作台</p><span class="scraper-service-state" data-service-state></span></div><label class="tmdb-switch-row"><input class="switch" role="switch" name="enabled" type="checkbox" ${config.Enabled?'checked':''}><span>启用刮削</span></label></div>
    <div data-scraper-pane="manual">
      <div class="scraper-task-grid">
        <section class="setting-card scraper-setup"><div class="card-heading"><h3>任务设置</h3><span class="scraper-section-number">01</span></div>
          <div class="scraper-scope-control"><span class="scraper-field-label">处理范围</span><strong data-scope-summary>手动刮削目录</strong><button type="button" class="secondary text-icon-button" data-manual-scopes>${fmIcon('folder')} 选择目录 / 磁盘</button></div>
          <div class="scraper-operation"><label><span class="scraper-field-label">任务操作</span><select data-operation><option value="scrape">仅刮削</option><option value="name">仅规范命名</option><option value="both">规范命名后刮削</option></select></label></div>
          <div class="scraper-execution"><span class="scraper-field-label">执行并发</span><button type="button" class="secondary" data-concurrency>刮削并发</button></div><p data-policy class="policy-note"></p>
          <div class="scraper-task-actions"><div data-scrape-actions class="tmdb-actions"><button type="button" class="secondary" data-plan>扫描任务</button><button type="button" data-start disabled>开始刮削</button></div><button type="button" class="secondary" data-cancel disabled>停止任务</button></div>
        </section>
        <section class="setting-card scraper-preview"><div class="card-heading"><h3 data-preview-title>任务预览</h3><span class="scraper-section-number">02</span></div><p data-result class="task-status" role="status" aria-live="polite">${result.Running?'刮削正在运行':'等待扫描'}</p><div data-details></div><div data-naming hidden></div></section>
      </div>
      <section class="setting-card scraper-records"><details open data-live><summary>任务记录</summary><div class="scraper-record-toolbar"><p data-live-status role="status"></p><label><input type="checkbox" data-log-errors>仅看失败</label></div><div data-live-entries></div></details></section>
    </div>
    <div data-scraper-pane="issues"><section class="setting-card"><div class="card-heading"><h3>待处理项目</h3></div><div data-media-issues></div></section></div>
    <div data-scraper-pane="monitor"><section class="setting-card scraper-monitor"><div class="card-heading with-control"><h3>实时监控</h3><label class="tmdb-switch-row"><input class="switch" role="switch" name="monitor" type="checkbox" ${config.MonitorEnabled?'checked':''}><span>启用监控</span></label></div><div class="scraper-monitor-options"><div class="scraper-scope-control"><span class="scraper-field-label">监控范围</span><strong data-monitor-summary>监控目录</strong><button type="button" class="secondary text-icon-button" data-monitor-settings>${fmIcon('folder')} 选择监控目录</button></div><label class="tmdb-switch-row"><input class="switch" role="switch" name="monitor-auto-refresh" type="checkbox" ${config.MonitorAutoRefresh?'checked':''}><span>自动刷新本地元数据</span></label></div></section></div>
    <div data-scraper-pane="settings"><div class="scraper-config-grid"><section class="setting-card"><div class="card-heading"><h3>刮削器优先级</h3></div><div data-scrapers></div><p class="tmdb-help">拖动调整补全顺序，至少启用一个刮削器。</p></section><section class="setting-card"><div class="card-heading"><h3>内容与写入策略</h3></div><div class="scraper-content-kinds">${Object.entries(kinds).map(([kind])=>`<span>${({Series:'电视剧',Movie:'电影',Season:'季',Episode:'单集'})[kind]}</span>`).join('')}</div><button type="button" class="secondary text-icon-button" data-settings>${fmIcon('gear')} 配置刮削内容</button></section></div></div>`;
  Panel.prepareScraper(host);
  const details = host.querySelector('[data-details]');
  const emptyPreview = () => {
    details.innerHTML = `<div class="scraper-empty-preview"><span class="scraper-empty-icon">${fmIcon('folder')}</span><strong>准备好整理你的媒体库</strong><p>选择处理范围，扫描后查看待刮削内容。</p></div>`;
  };
  emptyPreview();
  const monitor = host.querySelector('[name=monitor]');
  const autoRefresh = host.querySelector('[name=monitor-auto-refresh]');
  const enabled = host.querySelector('[name=enabled]'), provider = host.querySelector('[data-scrapers]');
  let providerSaving = false;
  const selectedProviders = () => config.Scrapers || [config.Scraper || 'TMDB'];
  const renderProviders = () => {
    const selected = selectedProviders();
    const names = [...selected, ...['TMDB', 'Fanart.tv', 'Bangumi', ...result.Scrapers].filter((name, i, all) => !selected.includes(name) && all.indexOf(name) === i)];
    provider.innerHTML = names.map((name, index) => `<div class="row" data-provider="${esc(name)}"><button type="button" class="drag-handle secondary" aria-label="拖动 ${esc(name)} 排序，键盘方向键调整" style="touch-action:none;cursor:grab">☰</button><label class="row"><input type="checkbox" value="${esc(name)}" ${selected.includes(name) ? 'checked' : ''}>${esc(name)}</label><span class="scraper-provider-order">${String(index + 1).padStart(2, '0')}</span></div>`).join('');
    const save = async () => {
      const names = [...provider.querySelectorAll('input:checked')].map(el => el.value);
      if (!names.length) { renderProviders(); toast('至少选择一个刮削器'); return; }
      if (JSON.stringify(names) === JSON.stringify(selectedProviders())) return;
      providerSaving = true; controls();
      const next = {...config, Scrapers: names, Scraper: names[0]};
      try { await api('/admin/scraper', 'PUT', next); config = next; invalidate(); }
      catch (e) { renderProviders(); throw e; }
      finally { providerSaving = false; controls(); }
    };
    provider.querySelectorAll('input').forEach(input => { input.onchange = run(save); });
    provider.querySelectorAll('.drag-handle').forEach(handle => {
      handle.onpointerdown = e => {
        if (e.button !== 0 || busy || providerSaving) return;
        e.preventDefault();
        const row = handle.closest('[data-provider]'), before = [...provider.children];
        provider.setPointerCapture(e.pointerId); row.style.opacity = '.5';
        provider.onpointermove = event => {
          for (const other of [...provider.children]) {
            if (other === row) continue;
            const rect = other.getBoundingClientRect();
            if (event.clientY >= rect.top && event.clientY <= rect.bottom) {
              provider.insertBefore(row, event.clientY < rect.top + rect.height / 2 ? other : other.nextSibling); break;
            }
          }
          if (event.clientY < 80) window.scrollBy(0, -20);
          if (event.clientY > innerHeight - 80) window.scrollBy(0, 20);
        };
        const finish = cancelled => {
          provider.onpointermove = provider.onpointerup = provider.onpointercancel = provider.onlostpointercapture = null;
          if (provider.hasPointerCapture(e.pointerId)) provider.releasePointerCapture(e.pointerId);
          row.style.opacity = '';
          if (cancelled) before.forEach(el => provider.appendChild(el));
          else run(save)();
        };
        provider.onpointerup = () => finish(false);
        provider.onpointercancel = provider.onlostpointercapture = () => finish(true);
      };
      handle.onkeydown = run(async e => {
        if (!['ArrowUp', 'ArrowDown'].includes(e.key) || busy || providerSaving) return;
        e.preventDefault();
        const row = handle.closest('[data-provider]');
        const other = e.key === 'ArrowUp' ? row.previousElementSibling : row.nextElementSibling;
        if (!other) return;
        provider.insertBefore(row, e.key === 'ArrowUp' ? other : other.nextSibling);
        await save(); handle.focus();
      });
    });
  };
  const scan = host.querySelector('[data-plan]'), start = host.querySelector('[data-start]'), cancelButton = host.querySelector('[data-cancel]'), status = host.querySelector('[data-result]');
  const operation=host.querySelector('[data-operation]');
  const controls = () => {
    const serviceState = host.querySelector('[data-service-state]');
    serviceState.textContent = taskActive || busy || namingBusy ? '任务运行中' : config.Enabled ? '已就绪' : '未启用';
    serviceState.dataset.active = String(config.Enabled);
    const locked=busy||providerSaving||namingBusy;
    scan.disabled = locked || taskActive || !config.Enabled;
    start.disabled = locked || taskActive || !config.Enabled || !plan || !(plan.Pending + plan.Overwrite);
    cancelButton.disabled = !(busy || taskActive || namingBusy) || !!namingUI?.applying;
    monitor.disabled = locked;
    autoRefresh.disabled = locked;
    operation.disabled=locked||taskActive;
    host.querySelector('[data-monitor-settings]').disabled = locked;
    host.querySelector('[data-manual-scopes]').disabled = locked||taskActive;
    provider.querySelectorAll('input, button').forEach(el => { el.disabled = locked; });
    [...provider.children].forEach((row, index) => { row.querySelector('.scraper-provider-order').textContent = String(index + 1).padStart(2, '0'); });
    enabled.disabled = locked;
    host.querySelector('[data-settings]').disabled = locked;
    host.querySelector('[data-concurrency]').disabled = locked || taskActive;
    namingUI?.setBusy(busy||providerSaving||taskActive);
    host.querySelector('[data-concurrency]').textContent = `刮削并发：${config.Concurrency || 1}`;
    host.querySelector('[data-scope-summary]').textContent = config.ManualScopes?.length ? `已设置 ${config.ManualScopes.length} 条目录选择规则` : '默认范围：全部媒体库目录';
    host.querySelector('[data-monitor-summary]').textContent = `已选择 ${(config.MonitorScopes||[]).filter(x=>x.Enabled).length} 个监控目录`;
    host.querySelector('[data-policy]').textContent = `覆盖策略：${config.Overwrite ? '覆盖已有文件' : '跳过已有文件（默认）'}`;
  };
  const invalidate = () => { plan = null; namingUI?.invalidate(); status.textContent = '设置已保存，请重新扫描'; toast(status.textContent); emptyPreview(); controls(); };
  operation.onchange=()=>{
    plan=null;namingUI?.invalidate();
    host.querySelector('[data-naming]').hidden=operation.value==='scrape';
    host.querySelector('[data-scrape-actions]').hidden=operation.value!=='scrape';
    host.querySelector('[data-policy]').hidden=operation.value==='name';
    details.hidden=operation.value!=='scrape';
    host.querySelector('[data-preview-title]').textContent=operation.value==='scrape'?'任务预览':'规范命名';
    emptyPreview();
    status.textContent=operation.value==='scrape'?'请先扫描生成计划。':operation.value==='both'?'使用上方目录范围，命名完成后自动刮削。':'使用上方目录范围进行规范命名。';
    controls();
  };
  enabled.onchange = run(async () => {
    const next = { ...config, Enabled: enabled.checked };
    try { await api('/admin/scraper', 'PUT', next); config = next; invalidate(); }
    catch (e) { enabled.checked = config.Enabled; throw e; }
  });
  monitor.onchange = run(async () => {
    const next = { ...config, MonitorEnabled: monitor.checked };
    try { await api('/admin/scraper', 'PUT', next); config = next; invalidate(); }
    catch (e) { monitor.checked = config.MonitorEnabled; throw e; }
  });
  autoRefresh.onchange = run(async () => {
    const next = { ...config, MonitorAutoRefresh: autoRefresh.checked };
    try {
      await api('/admin/scraper', 'PUT', next);
      config = next;
      invalidate();
    } catch (e) {
      autoRefresh.checked = config.MonitorAutoRefresh;
      throw e;
    }
  });

  const scopeDialog = async mode => {
    const monitorMode = mode === 'monitor';
    const field = monitorMode ? 'MonitorScopes' : 'ManualScopes';
    const fallback = !monitorMode;

    let overrides = (config[field] || [])
      .filter(value => !monitorMode || value.Relative === '.')
      .map(value => ({ ...value }));

    const response = await api('/admin/scraper/tree');
    const roots = response.Libraries.flatMap(lib =>
      lib.Locations.map(root => ({
        Library: lib.Id,
        Root: root,
        Relative: '.',
        Name: lib.Name
      }))
    );

    const equal = (a, b) =>
      a.Library === b.Library &&
      a.Root === b.Root &&
      a.Relative === b.Relative;

    const inherited = node => {
      let value = fallback;
      let depth = -1;

      for (const item of overrides) {
        if (item.Library !== node.Library || item.Root !== node.Root) continue;

        if (
          item.Relative === '.' ||
          item.Relative === node.Relative ||
          node.Relative.startsWith(item.Relative + '/')
        ) {
          const nextDepth = item.Relative === '.' ? 0 : item.Relative.length;
          if (nextDepth > depth) {
            value = item.Enabled;
            depth = nextDepth;
          }
        }
      }

      return value;
    };

    const intro = monitorMode
      ? '仅支持媒体库父目录。'
      : '';

    const toolbar = monitorMode
      ? `<div class="tmdb-actions"><button type="button" class="secondary" data-all>全选</button></div>`
      : `<div class="search-form"><input type="search" data-scope-search placeholder="搜索媒体目录"><button type="button" class="secondary" data-all>全选</button></div><div data-search-results></div>`;

    fmDialog(
      monitorMode ? '实时监控目录' : '手动刮削目录',
      `${intro ? `<p>${intro}</p>` : ''}${toolbar}<div data-scope-tree></div>`,
      async () => {
        if (monitorMode) {
          overrides = overrides.filter(item => item.Relative === '.');
        }
        const next = { ...config, [field]: overrides };
        await api('/admin/scraper', 'PUT', next);
        config = next;
        invalidate();
      }
    );

    const dialog = $('#modal');
    dialog.classList.add('scraper-scope-sheet');
    const tree = dialog.querySelector('[data-scope-tree]');
    const visible = [];

    const setOverride = (node, enabled) => {
      overrides = overrides.filter(item => !equal(item, node));
      overrides.push({
        Library: node.Library,
        Root: node.Root,
        Relative: node.Relative,
        Enabled: enabled
      });
    };

    const repaint = () => visible.forEach(({ node, input }) => {
      input.checked = inherited(node);
      input.title = overrides.some(item => equal(item, node))
        ? '明确设置'
        : '继承父目录';
    });

    const addSimple = (node, container, label) => {
      const row = document.createElement('div');
      row.className = 'scraper-tree-line';

      const input = document.createElement('input');
      input.type = 'checkbox';
      input.setAttribute('aria-label', '选择目录');
      input.checked = inherited(node);

      const text = document.createElement('span');
      text.textContent = label;

      input.onchange = () => {
        setOverride(node, input.checked);
        repaint();
      };

      visible.push({ node, input });
      row.append(input, text);
      container.append(row);
    };

    if (monitorMode) {
      roots.forEach(root =>
        addSimple(root, tree, `${root.Name} · ${root.Root}`)
      );
    } else {
      const add = (node, container, root = false) => {
        const row = document.createElement('div');
        row.className = 'scraper-tree-node';

        row.innerHTML =
          `<div class="scraper-tree-line">` +
          `<input type="checkbox" aria-label="选择目录">` +
          `<button type="button" class="secondary scraper-folder" aria-expanded="false">${fmIcon("folder")}<span class="scraper-folder-label"></span></button>` +
          `</div><div data-children hidden></div>`;

        const input = row.querySelector('input');
        const expand = row.querySelector('button');
        const children = row.querySelector('[data-children]');

        expand.querySelector('.scraper-folder-label').textContent =
          root ? `${node.Name} · ${node.Root}` : node.Name;

        visible.push({ node, input });
        input.checked = inherited(node);

        input.onchange = () => {
          setOverride(node, input.checked);
          repaint();
        };

        let loaded = false;

        expand.onclick = run(async () => {
          if (!loaded) {
            expand.disabled = true;
            try {
              const query = new URLSearchParams({
                library: node.Library,
                root: node.Root,
                relative: node.Relative
              });

              const result = await api('/admin/scraper/tree?' + query);
              result.Directories.forEach(child =>
                add({ ...node, ...child }, children)
              );
              loaded = true;
            } finally {
              expand.disabled = false;
            }
          }

          children.hidden = !children.hidden;
          expand.setAttribute('aria-expanded', String(!children.hidden));
        });

        container.append(row);
      };

      roots.forEach(root => add(root, tree, true));

      const search = dialog.querySelector('[data-scope-search]');
      const results = dialog.querySelector('[data-search-results]');
      let searchTimer = 0;

      search.oninput = () => {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(async () => {
          const term = search.value.trim();
          results.replaceChildren();

          if (!term) return;

          const query = new URLSearchParams({ search: term });
          const response = await api('/admin/scraper/tree?' + query);

          const matches = response.Results || [];

          if (!matches.length) {
            const empty = document.createElement('p');
            empty.textContent = '没有匹配的媒体目录';
            results.append(empty);
            return;
          }

          matches.forEach(node => {
            addSimple(
              node,
              results,
              `${node.Name} · ${node.Relative === '.' ? node.Root : node.Relative}`
            );
          });

          repaint();
        }, 250);
      };
    }

    const selectAll = dialog.querySelector('[data-all]');

    selectAll.onclick = () => {
      const enable = !(
        roots.length &&
        roots.every(inherited)
      );

      overrides = roots.map(({ Library, Root, Relative }) => ({
        Library,
        Root,
        Relative,
        Enabled: enable
      }));

      repaint();
    };
  };

  host.querySelector('[data-monitor-settings]').onclick = run(() => scopeDialog('monitor'));
  host.querySelector('[data-manual-scopes]').onclick = run(() => scopeDialog('manual'));
  renderProviders();
  host.querySelector('[data-concurrency]').onclick = () => {
    fmDialog('刮削并发', `<label class="tmdb-field"><span>同时刮削的媒体数量（1–32）</span><input name="concurrency" type="number" min="1" max="32" step="1" required value="${config.Concurrency || 1}"></label><p class="tmdb-help">保存后重新扫描；API 频率仍按 TMDB 设置。</p>`, async data => {
      const concurrency = Number(data.get('concurrency'));
      if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 32) throw Error('并发数必须是 1–32 的整数');
      const next = {...config, Concurrency: concurrency};
      await api('/admin/scraper', 'PUT', next);
      config = next;
      invalidate();
    });
  };
  host.querySelector('[data-settings]').onclick = () => {
    fmDialog('刮削设置', `<p>保存后重新扫描生成计划。</p><div class="scraper-settings-grid">${Object.entries(kinds).map(([kind, title]) => `<fieldset class="scraper-category"><legend class="scraper-category-title">${title}</legend><label class="toggle-label scraper-category-toggle">开启本分类<input class="switch" role="switch" type="checkbox" name="${kind}-enabled" ${config.Categories[kind].Enabled ? 'checked' : ''}></label><div data-category="${kind}">${contents[kind].map(value => `<label class="row"><input type="checkbox" name="${kind}-${value}" ${config.Categories[kind].Content.includes(value) ? 'checked' : ''}>${contentName(kind, value)}</label>`).join('')}</div></fieldset>`).join('')}</div><p class="tmdb-help">图片不可用时保留已有文件。</p><label class="tmdb-field"><span>Fanart.tv API Key</span><input type="password" name="fanart-api-key" autocomplete="off" value="${esc(config.FanartAPIKey || '')}"></label><div class="scraper-settings-options"><label class="tmdb-field"><span>覆盖策略</span><select name="overwrite"><option value="false" ${!config.Overwrite ? 'selected' : ''}>跳过已有文件</option><option value="true" ${config.Overwrite ? 'selected' : ''}>覆盖已有文件</option></select></label><div><label class="tmdb-switch-row"><input class="switch" role="switch" type="checkbox" name="chinese-metadata" ${config.ChineseMetadata ? 'checked' : ''}><span>刮削简体中文</span></label></div><div><label class="tmdb-switch-row"><input class="switch" role="switch" type="checkbox" name="chinese-posters" ${!config.OriginalPosters ? 'checked' : ''}><span>刮削中文海报</span></label><p class="tmdb-help">中文元数据或海报缺失时回退原语言。</p></div></div>`, async data => {
      const next = { ...config, FanartAPIKey: data.get('fanart-api-key').trim(), Overwrite: data.get('overwrite') === 'true', ChineseMetadata: data.has('chinese-metadata'), OriginalPosters: !data.has('chinese-posters'), Categories: {} };
      for (const kind of Object.keys(kinds)) next.Categories[kind] = { Enabled: data.has(`${kind}-enabled`), Content: contents[kind].filter(value => data.has(`${kind}-${value}`)) };
      await api('/admin/scraper', 'PUT', next); config = next; invalidate();
    });
    $('#modal').classList.add('scraper-settings-sheet');
  };
  scan.onclick = run(async () => {
    if (!config.Enabled || busy) return;
    scanCancelled = false; busy = true; taskActive = true; plan = null; emptyPreview(); controls(); status.textContent = '正在扫描媒体库…';
    details.querySelector('strong').textContent = '正在生成任务预览';
    try {
      plan = await ScraperManual.plan({alive: () => !scanCancelled && host.isConnected});
      if (scanCancelled) { plan = null; return; }
      const failed = plan.FailedObjects ?? plan.Objects.filter(object => object.Error).length;
      status.textContent = plan.Pending + plan.Overwrite ? '扫描完成，可以开始刮削' : '扫描完成，没有待刮削内容';
      // Bound DOM work for large libraries; full item/file progress remains in Activity.
      details.innerHTML = `<div class="scraper-plan-stats">${[['媒体项目', plan.TotalObjects ?? plan.Objects.length], ['待写入文件', plan.Pending], ['覆盖文件', plan.Overwrite], ['已有 / 跳过', plan.Skipped]].map(([label, value]) => `<div><span>${label}</span><strong>${esc(value ?? 0)}</strong></div>`).join('')}</div><div class="scraper-plan-heading"><strong>媒体明细</strong><span>前 ${Math.min(100, plan.Objects.length)} 项${plan.Disabled ? ` · 分类关闭 ${esc(plan.Disabled)}` : ''}${failed ? ` · 路径异常 ${esc(failed)}` : ''}</span></div><div class="scraper-plan-list">${plan.Objects.slice(0, 100).map(object => `<div class="scraper-plan-item"><div><strong>${esc(object.Name)}</strong><span>${esc(object.Disabled ? '分类关闭' : object.Error || object.Targets.map(target => `${contentName(object.Kind, target.Content)}：${({ create: '待刮削', skip: '跳过', overwrite: '覆盖' })[target.Action] || target.Action}`).join('，'))}</span></div><span class="scraper-kind-badge">${esc(({Movie:'电影',Series:'电视剧',Season:'季',Episode:'单集'})[object.Kind] || object.Kind)}</span></div>`).join('') || '<p class="scraper-list-empty">当前范围没有媒体项目</p>'}</div>`;
    } catch (e) { if (!scanCancelled) { status.textContent = '任务扫描未完成：' + e.message; } }
    finally { busy = false; taskActive = false; controls(); }
  });
  cancelButton.onclick = run(async () => {
    if (!busy && !taskActive && !namingBusy) return;
    if(namingUI?.applying)return;
    scanCancelled=true;namingUI?.cancel();
    if(taskActive)await api('/admin/scraper/cancel', 'POST', {});
    taskActive=false;plan=null;status.textContent='已停止后续处理，已完成的改名保留';controls();
  });
  start.onclick = run(async () => {
    if (!plan) return;
    busy = true; controls();
    try { await ScraperManual.start(plan); taskActive = true; plan = null; status.textContent = '刮削已开始，下方实时信息会显示逐项进度。'; }
    catch (e) { plan = null; status.textContent = '未启动，请重新扫描刮削任务'; throw e; }
    finally { busy = false; controls(); }
  });
  const activePage=()=>host.isConnected&&view==='admin'&&!host.closest('.admin-section')?.hidden&&!host.querySelector('[data-scraper-pane="manual"]').hidden;
  namingUI=FileNaming.mount(host.querySelector('[data-naming]'),{
    useScraperScopes:true,
    onPreview:()=>issuesUI?.refresh(),
    alive:activePage,
    onError:error=>{if(!scanCancelled)status.textContent='任务未继续：'+error.message;},
    label:()=>operation.value==='both'?'命名并刮削':'自动命名',
    applyLabel:()=>operation.value==='both'?'改名并刮削':'执行改名',
    onBusy:value=>{namingBusy=value;controls();},
    onBeforeRun:async()=>{
      const state=await api('/admin/scraper');
      if(state.Running||state.Planning||state.Settings.MonitorEnabled)throw Error('请先结束刮削任务并关闭实时刮削监控');
      if(operation.value==='both'&&!state.Settings.Enabled)throw Error('请先开启刮削服务');
      config={...config,...state.Settings};scanCancelled=false;
    },
    onComplete:async data=>{
      const state=await api('/admin/scraper');config={...config,...state.Settings};controls();
      if(operation.value!=='both')return;
      const continuing=()=>!scanCancelled&&activePage();
      if(!continuing())return;
      status.textContent='命名已完成，正在刷新媒体库…';
      const refresh=await api('/admin/features/naming/refresh','POST',{scopeVersion:data.scopeVersion});
      const ids=new Set(refresh.Libraries);
      while(continuing()){
        const live=await api('/admin/scan/status');
        if(!live.Libraries.some(lib=>ids.has(lib.Id)))break;
        await new Promise(resolve=>setTimeout(resolve,1000));
      }
      if(!continuing())return;
      const libraries=await api('/admin/libraries');
      const failed=libraries.find(lib=>ids.has(lib.Id)&&(lib.Error||lib.Status!=='idle'));
      if(failed)throw Error('命名已完成，媒体库刷新未完成：'+(failed.Error||failed.Name));
      await api('/admin/features/naming/refresh','POST',{scopeVersion:refresh.scopeVersion,checkOnly:true});
      if(!continuing())return;
      status.textContent='媒体库已刷新，正在生成刮削计划…';
      taskActive=true;controls();
      try { plan=await ScraperManual.plan({alive:continuing}); }
      finally { taskActive=false;controls(); }
      if(!continuing()){plan=null;return;}
      if(!(plan.Pending+plan.Overwrite)){plan=null;status.textContent='命名完成，当前范围没有待刮削内容。';return;}
      await ScraperManual.start(plan);taskActive=true;plan=null;
      status.textContent='命名完成，刮削已开始。';controls();
    }
  });
  issuesUI=ScraperIssues.mount(host.querySelector('[data-media-issues]'),count=>{
    const pane=host.querySelector('[data-scraper-pane="issues"]');
    const tab=host.querySelector(`[aria-controls="${pane.id}"]`);
    if(tab)tab.textContent=count?`待处理 (${count})`:'待处理';
  });
  const live = host.querySelector('[data-live]');
  const onlyErrors = live.querySelector('[data-log-errors]');
  let recentEntries = [], lastRecordHTML = '';
  const paintRecords = () => {
    const list = live.querySelector('[data-live-entries]');
    const filtered = onlyErrors.checked ? recentEntries.filter(entry => entry.Error || entry.State === 'error') : recentEntries;
    const html = filtered.map(renderScraperRecord).join('') || `<p class="scraper-list-empty">${onlyErrors.checked ? '最近记录中没有失败项目' : '暂无任务记录'}</p>`;
    if (lastRecordHTML === html) return;
    const opened = new Set([...list.querySelectorAll('details[open]')].map(row => row.dataset.logKey));
    list.innerHTML = html;
    lastRecordHTML = html;
    list.querySelectorAll('details').forEach(row => { row.open = opened.has(row.dataset.logKey); });
  };
  onlyErrors.onchange = paintRecords;
  let lastTaskStateCheck = 0;
  const poll = async () => {
    if (!live.isConnected || view!=='admin' || host.closest('.admin-section')?.hidden) return;
    try {
      const entries = await ScraperManual.logs();
      if (taskActive && Date.now() - lastTaskStateCheck > 2500) {
        lastTaskStateCheck = Date.now();
        const state = await api('/admin/scraper');
        taskActive = !!(state.Running || state.Planning);
        controls();
      }
      if (!live.isConnected) return;
      live.querySelector('[data-live-status]').textContent = `最近 ${entries.length} 条 · 更新于 ${new Date().toLocaleTimeString()}`;
      recentEntries = entries;
      paintRecords();
    } catch (e) {
      if (live.isConnected) live.querySelector('[data-live-status]').textContent = '日志读取失败：' + e.message + '，正在重试';
    } finally {
      if (live.isConnected && view==='admin' && !host.closest('.admin-section')?.hidden) setTimeout(poll, 1000);
    }
  };
  poll();
  controls();
}

const ScraperIssues = (()=>{
  const endpoint='/admin/features/media-issues';
  const sourceNames={naming:'规范命名',scraper:'元数据刮削'};
  const statusNames={review:'无法确定',conflict:'命名冲突',failed:'刮削失败'};
  function select(label,values){return UI.el('select',{'aria-label':label},values.map(([value,name])=>UI.el('option',{value},name)));}
  function mount(host,onCount=()=>{}){
    let page=1,sequence=0,controller=null,timer=null,searchTimer=null,loading=false,busy=false,current=[];
    const chosen=new Set();
    const search=UI.el('input',{type:'search',placeholder:'搜索名称、路径或原因','aria-label':'搜索待处理项目'});
    const source=select('任务来源',[['','全部来源'],['naming','规范命名'],['scraper','元数据刮削']]);
    const status=select('问题类型',[['','全部问题'],['review','无法确定'],['conflict','命名冲突'],['failed','刮削失败']]);
    const state=select('处理状态',[['','待处理'],['ignored','已忽略'],['all','全部状态']]);
    const refresh=UI.el('button',{type:'button',class:'secondary'},'刷新');
    const summary=UI.el('p',{class:'media-issues-summary',role:'status','aria-live':'polite'});
    const list=UI.el('div',{class:'media-issues-list'});
    const pagination=UI.el('div',{class:'media-issues-pagination'});
    const filters=UI.el('div',{class:'media-issues-filters'},[search,source,status,state,refresh]);
    const all=UI.el('input',{type:'checkbox','aria-label':'全选本页待处理项目'});
    const selectionCount=UI.el('span',{class:'media-issues-selected',role:'status','aria-live':'polite'});
    const clear=UI.el('button',{type:'button',class:'secondary'},'取消选择');
    const batchRetry=UI.el('button',{type:'button'},'批量重新刮削');
    const batchIgnore=UI.el('button',{type:'button',class:'secondary'},'批量忽略');
    const batchRestore=UI.el('button',{type:'button',class:'secondary'},'恢复待处理');
    const selection=UI.el('div',{class:'media-issues-selection'},[UI.el('label',{class:'media-issues-select-all'},[all,UI.el('span',{},'全选本页')]),selectionCount,batchRetry,clear,batchIgnore,batchRestore]);
    host.replaceChildren(filters,summary,selection,list,pagination);
    const alive=()=>host.isConnected&&view==='admin'&&!host.closest('.admin-section')?.hidden;
    const visible=()=>alive()&&!host.closest('[data-scraper-pane]')?.hidden;
    function updateSelection(){
      const locked=busy||loading;
      all.checked=current.length>0&&chosen.size===current.length;
      all.indeterminate=chosen.size>0&&chosen.size<current.length;
      all.disabled=locked||!current.length;
      const retryCount=current.filter(issue=>chosen.has(issue.id)&&issue.source==='scraper').length;
      selectionCount.textContent=`已选 ${chosen.size} 项${chosen.size ? ` · 可重刮 ${retryCount} 项` : ''}`;
      batchRetry.disabled=locked||!retryCount;
      clear.disabled=locked||!chosen.size;
      batchIgnore.disabled=locked||!current.some(issue=>chosen.has(issue.id)&&!issue.ignored);
      batchRestore.disabled=locked||!current.some(issue=>chosen.has(issue.id)&&issue.ignored);
      for(const input of list.querySelectorAll('[data-issue-select]')){input.checked=chosen.has(input.dataset.issueSelect);input.disabled=locked||!current.length;input.closest('.media-issue').classList.toggle('is-selected',input.checked);}
      for(const button of list.querySelectorAll('button'))button.disabled=locked||!current.length;
      for(const button of pagination.querySelectorAll('button'))button.disabled=locked||button.dataset.unavailable==='true';
      for(const input of [search,source,status,state])input.disabled=busy;
      refresh.disabled=locked;
    }
    function resetSelection(){sequence++;controller?.abort();loading=false;chosen.clear();current=[];updateSelection();}
    all.onchange=()=>{if(all.checked)current.forEach(issue=>chosen.add(issue.id));else chosen.clear();updateSelection();};
    clear.onclick=()=>{chosen.clear();updateSelection();};
    batchRetry.onclick=run(()=>{
      const issueIDs=current.filter(issue=>chosen.has(issue.id)&&issue.source==='scraper').map(issue=>issue.id);
      if(busy||loading||!issueIDs.length)return;
      return openFileScraper({issueIDs},false,()=>load(true));
    });
    async function setIgnored(ids,ignored){
      if(busy||loading||!ids.length)return;
      busy=true;controller?.abort();sequence++;updateSelection();
      try {
        const result=await api(endpoint,'PUT',{ids,ignored});
        ids.forEach(id=>chosen.delete(id));
        toast(`${ignored?'已忽略':'已恢复待处理'} ${result.updated} 项${result.missing?`，${result.missing} 项已处理或移除`:''}`);
      }finally{busy=false;await load(true);updateSelection();}
    }
    batchIgnore.onclick=run(()=>setIgnored([...chosen],true));
    batchRestore.onclick=run(()=>setIgnored([...chosen],false));
    function render(data){
      current=data.items;
      const present=new Set(current.map(issue=>issue.id));
      for(const id of chosen)if(!present.has(id))chosen.delete(id);
      summary.textContent=`待处理 ${data.pending} 项 · 当前筛选 ${data.total} 项`;
      list.replaceChildren();
      for(const issue of data.items){
        const name=issue.path.split('/').at(-1);
        const actions=UI.el('div',{class:'media-issue-actions'});
        const open=UI.el('button',{type:'button',class:'secondary',onclick:()=>filesPage(issue.directory?issue.path:issue.path.split('/').slice(0,-1).join('/'))},'打开目录');
        const fix=UI.el('button',{type:'button',class:'secondary',onclick:()=>{
          const path=issue.path.split('/').slice(0,-1).join('/');
          FileNaming.open(path,[issue.path],()=>load(true),{
            initialKind:['Episode','Season','Series'].includes(issue.kind)?'tv':issue.kind==='Movie'?'movie':'auto',
            onPreview:()=>load(true),
            onBeforeRun:async()=>{
              const task=await api('/admin/scraper');
              if(task.Running||task.Planning||task.Settings.MonitorEnabled)throw Error('请先结束刮削任务并关闭实时刮削监控');
            }
          });
        }},'修正命名');
        actions.append(fix,open);
        if(issue.source==='scraper')actions.append(UI.el('button',{type:'button',class:'secondary',onclick:run(()=>openFileScraper({issueIDs:[issue.id]},false,()=>load(true)))},'重试刮削'));
        const ignore=UI.el('button',{type:'button',class:'secondary',onclick:run(()=>setIgnored([issue.id],!issue.ignored))},issue.ignored?'恢复待处理':'忽略');
        actions.append(ignore);
        const badge=UI.el('span',{class:'media-issue-badge media-issue-badge--'+issue.status},issue.ignored?'已忽略':statusNames[issue.status]||issue.status);
        const heading=UI.el('div',{class:'media-issue-heading'},[UI.el('strong',{},name),badge]);
        const path=UI.el('p',{class:'media-issue-path'},'/media/'+issue.path);
        const meta=UI.el('small',{class:'media-issue-meta'},`${sourceNames[issue.source]||issue.source} · ${issue.directory?'文件夹':'媒体文件'} · ${new Date(issue.updated/1e6).toLocaleString()}`);
        const body=UI.el('div',{class:'media-issue-body'},[heading,path,UI.el('p',{class:'media-issue-reason'},issue.reason),meta]);
        if(issue.proposed)body.append(UI.el('details',{class:'media-issue-proposed'},[UI.el('summary',{},'目标名称'),UI.el('p',{},issue.proposed)]));
        const checkbox=UI.el('input',{type:'checkbox','aria-label':`选择 ${issue.path}`,'data-issue-select':issue.id,onchange:()=>{if(checkbox.checked)chosen.add(issue.id);else chosen.delete(issue.id);updateSelection();}});
        list.append(UI.el('article',{class:'media-issue'},[UI.el('label',{class:'media-issue-select'},checkbox),body,actions]));
      }
      if(!data.items.length)list.append(UI.el('p',{class:'empty'},data.total?'此页没有项目，请返回上一页。':state.value==='ignored'?'没有已忽略的项目。':'没有符合条件的待处理项目。命名识别失败或刮削失败后会自动显示在这里。'));
      const pages=Math.max(1,Math.ceil(data.total/data.pageSize));
      const prev=UI.el('button',{type:'button',class:'secondary','data-unavailable':String(page<=1),onclick:()=>{page--;resetSelection();load(true);}},'上一页');
      const next=UI.el('button',{type:'button',class:'secondary','data-unavailable':String(page>=pages),onclick:()=>{page++;resetSelection();load(true);}},'下一页');
      pagination.replaceChildren(prev,UI.el('span',{},`${page} / ${pages}`),next);
      pagination.hidden=pages===1&&page===1;
      updateSelection();
    }
    async function load(full=visible()){
      if(!host.isConnected||busy)return;
      const request=++sequence;controller?.abort();controller=new AbortController();loading=true;updateSelection();
      const query=new URLSearchParams({page,search:search.value.trim(),source:source.value,status:status.value,state:state.value});
      if(!full)query.set('summary','true');
      try {
        const data=await api(endpoint+'?'+query,'GET',undefined,{signal:controller.signal});
        if(request!==sequence||!host.isConnected)return;
        onCount(data.pending);
        if(full){
          const pages=Math.max(1,Math.ceil(data.total/data.pageSize));
          if(page>pages){page=pages;resetSelection();await load(true);return;}
          render(data);
        }
      }catch(error){if(request===sequence&&error.name!=='AbortError')summary.textContent='待处理列表读取失败：'+error.message;}
      finally{if(request===sequence){loading=false;updateSelection();}}
    }
    search.oninput=()=>{resetSelection();clearTimeout(searchTimer);searchTimer=setTimeout(()=>{page=1;load(true);},300);};
    for(const input of [source,status,state])input.onchange=()=>{page=1;resetSelection();load(true);};
    refresh.onclick=()=>load(true);
    const pane=host.closest('[data-scraper-pane]');
    const tab=document.getElementById(pane?.getAttribute('aria-labelledby'));
    tab?.addEventListener('click',()=>load(true));
    tab?.parentElement.addEventListener('keydown',()=>queueMicrotask(()=>{if(visible())load(true);}));
    async function poll(){
      if(!alive()){controller?.abort();clearTimeout(searchTimer);return;}
      await load();
      if(alive())timer=setTimeout(poll,10000);
    }
    updateSelection();poll();
    return {refresh:()=>load(),dispose(){clearTimeout(timer);clearTimeout(searchTimer);controller?.abort();}};
  }
  return {mount};
})();
