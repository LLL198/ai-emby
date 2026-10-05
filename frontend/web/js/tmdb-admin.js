async function loadTMDBSettings() {
  const c = await api("/admin/tmdb");
  const f = $("#tmdb-settings");
  if (!f) return;
  f.className = "tmdb-settings-form";
  f.innerHTML = `
    <label class="tmdb-switch-row">
      <input class="switch" role="switch" name="Enabled" type="checkbox" ${c.Enabled ? "checked" : ""}>
      <span>启用 TMDB 按需元数据</span>
    </label>
    <label class="tmdb-field"><span>TMDB API 基础地址</span><input name="APIBase" type="url" required value="${esc(c.APIBase)}"></label>
    <div class="tmdb-field tmdb-token-field"><label for="secret-APIKey">API Key / Read Access Token</label>${secretField('APIKey', 'TMDB Token', '输入 TMDB 密钥；无鉴权代理可留空')}</div>
    <label class="tmdb-field tmdb-inline-field"><span>缓存目录</span><input name="Directory" required value="${esc(c.Directory)}"></label>
    <p class="tmdb-help">目录须位于 /app/data 下；更换目录保留旧缓存。</p>
    <label class="tmdb-field tmdb-inline-field tmdb-rate-field"><span>请求频率（次/秒）</span><input name="RequestsPerSecond" type="number" min="1" max="100" required value="${c.RequestsPerSecond}"></label>
    <div class="tmdb-actions"><button>保存设置</button><button type="button" class="danger" id="clear-tmdb">清空当前目录缓存</button></div>
    <div id="tmdb-validation" class="tmdb-validation" hidden role="status" aria-live="polite"></div>`;
  Panel.prepareForm("tmdb-settings");
  const secret = bindSecretField(f.elements.APIKey, '/admin/tmdb/secret', 'TMDB Token', c.HasAPIKey);
  f.onsubmit = run(async (e) => {
    e.preventDefault();
    const d = new FormData(f);
    const result = await api("/admin/tmdb", "PUT", {
      Enabled: d.has("Enabled"),
      APIBase: d.get("APIBase"),
      APIKey: secret.value(),
      Directory: d.get("Directory"),
      RequestsPerSecond: Number(d.get("RequestsPerSecond")),
    });
    secret.reset(!!result.HasAPIKey);
    if (result.Valid) {
      const status = $("#tmdb-validation");
      status.hidden = false;
      status.innerHTML = `<span class="tmdb-validation-icon" aria-hidden="true">✓</span><span><strong>TMDB API 有效，设置已保存。</strong><small>可在「实时日志」的 TMDB 中查看验证记录。</small></span>`;
    } else {
      toast(result.Validation || "TMDB 设置已保存");
    }
  });
  $("#clear-tmdb").onclick = run(async () => {
    if (!(await confirmDialog("清空 TMDB 缓存", "清空已保存设置中当前目录的 TMDB 缓存？下次浏览将重新获取。"))) return;
    const result = await api("/admin/tmdb", "DELETE");
    toast(`已清理 ${result.Removed} 条缓存`);
  });
}
