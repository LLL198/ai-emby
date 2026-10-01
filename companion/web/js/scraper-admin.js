// Shared manual planning/start/polling used by settings and the locked file dialog.
const ScraperManual = {
  async plan({ file, itemID, manualRecognition, alive = () => true, onTask = () => {} } = {}) {
    const query = new URLSearchParams({async: 'true'});
    if (file !== undefined) query.set('file', file);
    const response = await api('/admin/scraper/plan?' + query, 'POST', {itemID, manualRecognition});
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
    return api('/admin/logs?category=scraper').then(data => (data.Entries || []).filter(x => x.Category === 'scraper' && (!taskID || x.TaskID === taskID)).slice(0, 100));
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
async function openFileScraper(item, poster = false) {
  const content = UI.el('div', {class: 'file-scraper-content'});
  content.innerHTML = `<p>仅刮削：${esc(item.name)}${poster ? '' : '（' + esc(item.path) + '）'}</p>${poster ? `<label>片名<input name="title" maxlength="256" required value="${esc(item.name)}"></label><label>年份<input name="year" type="number" min="0" max="9999" value="${esc(item.year)}"></label><label>TMDB ID<input name="tmdb" inputmode="numeric" pattern="[0-9]*" value="${esc(item.tmdb)}"></label>` : ''}<p>复用刮削管理的内容与覆盖设置；关闭窗口不会取消任务。</p><div class="tmdb-actions"><button type="button" data-plan>扫描刮削任务</button><button type="button" data-start disabled>开始刮削</button></div><p role="status" data-status>扫描计划后开始刮削</p><div data-logs aria-live="polite"></div>`;
  const dialog = UI.Modal('刮削', content);
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
        if (['complete', 'error'].includes(task.State)) { started = false; controls(); }
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
        ...(poster ? {itemID:item.id, manualRecognition} : {file:item.path}),
        alive:()=>dialog.open, onTask:id=>{taskID=id; poll();}
      });
      if (dialog.open) status.textContent = `媒体 ${plan.TotalObjects ?? plan.Objects.length} · 待刮削 ${plan.Pending} · 覆盖 ${plan.Overwrite} · 跳过 ${plan.Skipped}`;
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
}

async function loadScraperSettings() {
  const result = await api('/admin/scraper');
  const host = $('#scraper-settings');
  if (!host) return;
  let scanCancelled = false;
  let config = { ManualEnabled: false, MonitorAutoRefresh: true, ChineseMetadata: true, OriginalPosters: false, ...result.Settings }, plan = null, busy = false, taskActive = !!(result.Running || result.Planning);
  const kinds = { Series: '电视剧 Series', Movie: '电影 Movie', Season: '季 Season', Episode: '集 Episode' };
  const contents = { Series: ['NFO', 'Poster', 'Backdrop', 'Logo', 'Banner'], Movie: ['NFO', 'Poster', 'Backdrop', 'Logo', 'Disc', 'Banner'], Season: ['NFO', 'Poster', 'Banner'], Episode: ['NFO', 'Still'] };
  const contentName = (kind, value) => ({ NFO: 'NFO元数据', Poster: '海报', Backdrop: '背景图', Logo: 'Logo', Still: '缩略图', Disc: '光盘图（如有）', Banner: '横幅图（如有）' })[value] || value;
  host.innerHTML = `<div class="scraper-switch-line"><label class="tmdb-switch-row"><input class="switch" role="switch" name="enabled" type="checkbox" ${config.Enabled ? 'checked' : ''}><span>开启刮削</span></label><button type="button" class="secondary icon-button" data-settings title="刮削设置" aria-label="刮削设置">${fmIcon('gear')}</button><button type="button" class="secondary" data-concurrency>刮削并发</button></div><div class="scraper-switch-line scraper-monitor-line"><label class="tmdb-switch-row"><input class="switch" role="switch" name="monitor" type="checkbox" ${config.MonitorEnabled ? 'checked' : ''}><span>实时监控</span></label><button type="button" class="secondary icon-button" data-monitor-settings title="监控目录设置" aria-label="监控目录设置">${fmIcon('gear')}</button></div><div class="scraper-switch-line scraper-monitor-child"><label class="tmdb-switch-row"><input class="switch" role="switch" name="monitor-auto-refresh" type="checkbox" ${config.MonitorAutoRefresh ? 'checked' : ''}><span>自动刷新</span></label><span class="tmdb-help">与增强管理共用刷新队列；两处均开启时优先使用刮削管理，刷新本地元数据</span></div><div class="scraper-switch-line"><label class="tmdb-switch-row"><input class="switch" role="switch" name="manual" type="checkbox" ${config.ManualEnabled ? 'checked' : ''}><span>手动刮削</span></label><button type="button" class="secondary icon-button" data-manual-scopes title="手动刮削目录设置" aria-label="手动刮削目录设置">${fmIcon('gear')}</button></div><fieldset><legend>刮削器（至少选择一个）</legend><div data-scrapers></div><p class="tmdb-help">拖动 ☰ 调整优先级，也可使用方向键。排前面的优先刮削，后面的只补全缺失内容。Fanart.tv 提供图片，需配置 API Key；Bangumi 提供动画电影和剧集元数据、海报。</p></fieldset><p>仅处理当前媒体库索引中的媒体。扫描刮削任务只检查本地文件，得到计划后再开始刮削。实时监控独立于普通扫描；新目录稳定后等待媒体索引再自动刮削。写入 NFO、JPG/PNG；已有 WebP 保留并跳过。</p><p data-policy></p><div class="tmdb-actions"><button type="button" class="secondary" data-plan>扫描刮削任务</button><button type="button" class="secondary" data-start disabled>开始刮削</button><button type="button" class="secondary" data-cancel disabled>停止扫描 / 刮削</button></div><p data-result role="status" aria-live="polite">${result.Running ? '刮削运行中，可在实时日志查看；关闭刮削可取消。' : '尚未生成计划'}</p><details open data-live><summary>扫描 / 刮削实时信息</summary><p data-live-status role="status"></p><div data-live-entries></div></details><div data-details></div>`;
  const manual = host.querySelector('[name=manual]');
  manual.onchange = run(async () => {
    const next = { ...config, ManualEnabled: manual.checked };
    try { await api('/admin/scraper', 'PUT', next); config = next; invalidate(); }
    catch (e) { manual.checked = config.ManualEnabled; throw e; }
  });
  const monitor = host.querySelector('[name=monitor]');
  const autoRefresh = host.querySelector('[name=monitor-auto-refresh]');
  const enabled = host.querySelector('[name=enabled]'), provider = host.querySelector('[data-scrapers]');
  let providerSaving = false;
  const selectedProviders = () => config.Scrapers || [config.Scraper || 'TMDB'];
  const renderProviders = () => {
    const selected = selectedProviders();
    const names = [...selected, ...['TMDB', 'Fanart.tv', 'Bangumi', ...result.Scrapers].filter((name, i, all) => !selected.includes(name) && all.indexOf(name) === i)];
    provider.innerHTML = names.map(name => `<div class="row" data-provider="${esc(name)}"><button type="button" class="drag-handle secondary" aria-label="拖动 ${esc(name)} 排序，键盘方向键调整" style="touch-action:none;cursor:grab">☰</button><label class="row"><input type="checkbox" value="${esc(name)}" ${selected.includes(name) ? 'checked' : ''}>${esc(name)}</label></div>`).join('');
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
  const controls = () => {
    scan.disabled = busy || providerSaving || !config.Enabled || !config.ManualEnabled;
    start.disabled = busy || providerSaving || !config.Enabled || !config.ManualEnabled || !plan || !(plan.Pending + plan.Overwrite);
    cancelButton.disabled = !(busy || taskActive);
    monitor.disabled = busy || providerSaving;
    autoRefresh.disabled = busy || providerSaving;
    manual.disabled = busy || providerSaving;
    host.querySelector('[data-monitor-settings]').disabled = busy || providerSaving;
    host.querySelector('[data-manual-scopes]').disabled = busy || providerSaving;
    provider.querySelectorAll('input, button').forEach(el => { el.disabled = busy || providerSaving; });
    enabled.disabled = providerSaving;
    host.querySelector('[data-settings]').disabled = busy || providerSaving;
    host.querySelector('[data-concurrency]').disabled = busy || providerSaving || taskActive;
    host.querySelector('[data-concurrency]').textContent = `刮削并发：${config.Concurrency || 1}`;
    host.querySelector('[data-policy]').textContent = `覆盖策略：${config.Overwrite ? '覆盖已有文件' : '跳过已有文件（默认）'}`;
  };
  const invalidate = () => { plan = null; status.textContent = `设置已保存 · 刮削${config.Enabled ? '开启' : '关闭'} · 实时监控${config.MonitorEnabled ? '开启' : '关闭'} · 自动刷新${config.MonitorAutoRefresh ? '开启' : '关闭'} · 手动刮削${config.ManualEnabled ? '开启' : '关闭'}，请重新生成手动计划`; toast(status.textContent); host.querySelector('[data-details]').replaceChildren(); controls(); };
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
      ? '实时监控只选择当前媒体库父目录，不展开子目录。媒体库目录变化后这里同步变化。'
      : '手动刮削可选择父目录或子目录。可使用搜索框快速查找已进入媒体库索引的目录。';

    const toolbar = monitorMode
      ? `<div class="tmdb-actions"><button type="button" class="secondary" data-all>全选</button></div>`
      : `<div class="search-form"><input type="search" data-scope-search placeholder="搜索媒体目录"><button type="button" class="secondary" data-all>全选</button></div><div data-search-results></div>`;

    fmDialog(
      monitorMode ? '实时监控目录' : '手动刮削目录',
      `<p>${intro}</p>${toolbar}<div data-scope-tree></div>`,
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
          `<button type="button" class="secondary scraper-folder" aria-expanded="false">📁 <span></span></button>` +
          `</div><div data-children hidden></div>`;

        const input = row.querySelector('input');
        const expand = row.querySelector('button');
        const children = row.querySelector('[data-children]');

        row.querySelector('span').textContent =
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
    fmDialog('刮削并发', `<label class="tmdb-field"><span>同时刮削的媒体数量</span><input name="concurrency" type="number" min="1" max="32" step="1" required value="${config.Concurrency || 1}"></label><p class="tmdb-help">可选 1–32，默认 1。保存后请重新扫描生成计划，对新任务生效。TMDB API 请求频率仍按 TMDB 设置控制。</p>`, async data => {
      const concurrency = Number(data.get('concurrency'));
      if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 32) throw Error('并发数必须是 1–32 的整数');
      const next = {...config, Concurrency: concurrency};
      await api('/admin/scraper', 'PUT', next);
      config = next;
      invalidate();
    });
  };
  host.querySelector('[data-settings]').onclick = () => {
    fmDialog('刮削设置', `<p>内容或覆盖策略保存后，请重新扫描生成计划。</p><div class="scraper-settings-grid">${Object.entries(kinds).map(([kind, title]) => `<fieldset class="scraper-category"><legend class="scraper-category-title">${title}</legend><label class="toggle-label scraper-category-toggle">开启本分类<input class="switch" role="switch" type="checkbox" name="${kind}-enabled" ${config.Categories[kind].Enabled ? 'checked' : ''}></label><div data-category="${kind}">${contents[kind].map(value => `<label class="row"><input type="checkbox" name="${kind}-${value}" ${config.Categories[kind].Content.includes(value) ? 'checked' : ''}>${contentName(kind, value)}</label>`).join('')}</div></fieldset>`).join('')}</div><p class="tmdb-help">勾选 Fanart.tv 后可刮削光盘图及横幅图；无可用图片时保留已有文件。</p><label class="tmdb-field"><span>Fanart.tv API Key</span><input type="password" name="fanart-api-key" autocomplete="off" value="${esc(config.FanartAPIKey || '')}"></label><div class="scraper-settings-options"><label class="tmdb-field"><span>覆盖策略</span><select name="overwrite"><option value="false" ${!config.Overwrite ? 'selected' : ''}>跳过已有文件</option><option value="true" ${config.Overwrite ? 'selected' : ''}>覆盖已有文件</option></select></label><div><label class="tmdb-switch-row"><input class="switch" role="switch" type="checkbox" name="chinese-metadata" ${config.ChineseMetadata ? 'checked' : ''}><span>刮削简体中文</span></label><p class="tmdb-help">默认开启；中文元数据不可用时自动回退影片原语言。</p></div><div><label class="tmdb-switch-row"><input class="switch" role="switch" type="checkbox" name="chinese-posters" ${!config.OriginalPosters ? 'checked' : ''}><span>刮削中文海报</span></label><p class="tmdb-help">默认开启；中文海报不可用时自动回退影片原语言海报。</p></div></div>`, async data => {
      const next = { ...config, FanartAPIKey: data.get('fanart-api-key').trim(), Overwrite: data.get('overwrite') === 'true', ChineseMetadata: data.has('chinese-metadata'), OriginalPosters: !data.has('chinese-posters'), Categories: {} };
      for (const kind of Object.keys(kinds)) next.Categories[kind] = { Enabled: data.has(`${kind}-enabled`), Content: contents[kind].filter(value => data.has(`${kind}-${value}`)) };
      await api('/admin/scraper', 'PUT', next); config = next; invalidate();
    });
    $('#modal').classList.add('scraper-settings-sheet');
  };
  scan.onclick = run(async () => {
    if (!config.Enabled || !config.ManualEnabled || busy) return;
    scanCancelled = false; busy = true; taskActive = true; plan = null; controls(); status.textContent = '扫描刮削任务…';
    try {
      plan = await ScraperManual.plan({alive: () => !scanCancelled && host.isConnected});
      if (scanCancelled) { plan = null; return; }
      const failed = plan.FailedObjects ?? plan.Objects.filter(object => object.Error).length;
      status.textContent = `媒体 ${plan.TotalObjects ?? plan.Objects.length} · 待刮削文件 ${plan.Pending} · 已存在/跳过 ${plan.Skipped} · 将覆盖 ${plan.Overwrite} · 分类关闭 ${plan.Disabled} · 路径异常 ${failed}`;
      // Bound DOM work for large libraries; full item/file progress remains in Activity.
      host.querySelector('[data-details]').innerHTML = `<details open><summary>任务预览（前 100 项）</summary>${plan.Objects.slice(0, 100).map(object => `<p>${esc(object.Name)} · ${esc(object.Kind)} · ${object.Disabled ? '分类关闭' : object.Error ? esc(object.Error) : object.Targets.map(target => `${contentName(object.Kind, target.Content)}：${({ create: '待刮削', skip: '跳过', overwrite: '覆盖' })[target.Action]}`).join('，')}</p>`).join('')}</details>`;
    } catch (e) { if (!scanCancelled) { status.textContent = '任务扫描未完成：' + e.message; } }
    finally { busy = false; taskActive = false; controls(); }
  });
  cancelButton.onclick = run(async () => { if (!busy && !taskActive) return; await api('/admin/scraper/cancel', 'POST', {}); scanCancelled = true; taskActive = false; plan = null; status.textContent = '已请求停止扫描或刮削任务'; controls(); });
  start.onclick = run(async () => {
    if (!plan) return;
    busy = true; controls();
    try { await ScraperManual.start(plan); taskActive = true; plan = null; status.textContent = '刮削已开始，下方实时信息会显示逐项进度。'; }
    catch (e) { plan = null; status.textContent = '未启动，请重新扫描刮削任务'; throw e; }
    finally { busy = false; controls(); }
  });
  const live = host.querySelector('[data-live]');
  let lastTaskStateCheck = 0;
  const poll = async () => {
    if (!live.isConnected) return;
    try {
      const entries = await ScraperManual.logs();
      if (taskActive && Date.now() - lastTaskStateCheck > 2500) {
        lastTaskStateCheck = Date.now();
        const state = await api('/admin/scraper');
        taskActive = !!(state.Running || state.Planning);
        controls();
      }
      if (!live.isConnected) return;
      live.querySelector('[data-live-status]').textContent = '最近刷新 ' + new Date().toLocaleTimeString() + ' · 最近 100 条';
      const list = live.querySelector('[data-live-entries]');
      const html = entries.map(renderScraperLog).join('') || '<p>暂无扫描或刮削记录</p>';
      if (list.innerHTML !== html) list.innerHTML = html;
    } catch (e) {
      if (live.isConnected) live.querySelector('[data-live-status]').textContent = '日志读取失败：' + e.message + '，正在重试';
    } finally {
      if (live.isConnected) setTimeout(poll, 1000);
    }
  };
  poll();
  controls();
}
