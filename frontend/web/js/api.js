// Existing contracts, authentication and errors live here. Callable for legacy admin handlers.
async function api(path, method = "GET", data, options = {}) {
  const requestToken = options.authToken ?? token;
  const headers = {
    "X-Emby-Token": requestToken,
    "X-Emby-Authorization": `Emby Client="AI Emby Web", Device="Browser", DeviceId="${device}", Version="1.0"`,
  };
  if (!options.raw) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method,
    headers,
    body:
      data === undefined
        ? undefined
        : options.raw
          ? data
          : JSON.stringify(data),
    signal: options.signal,
  });
  let body = await res.text();
  try {
    body = JSON.parse(body);
  } catch {}
  if (!res.ok) {
    if (res.status === 401 && requestToken && body?.error !== "license_required" && !path.toLowerCase().includes("/authenticatebyname"))
      handleExpiredSession(requestToken);
    const message = body?.error === "license_required"
      ? body.reason === "license_server_unavailable"
        ? "授权服务器暂时无法连接，请稍后重新加载。"
        : body.message || "服务器授权验证未通过，请检查授权配置。"
      : body?.Message || body?.message || body?.error || `HTTP ${res.status}`;
    const e = new Error(message);
    e.status = res.status;
    throw e;
  }
  return body;
}
api.query = (path, params = {}) =>
  api(path + "?" + new URLSearchParams(params));
api.login = (Username, Pw) =>
  api("/emby/Users/AuthenticateByName", "POST", { Username, Pw });
api.logout = () => api("/Sessions/Logout", "POST", {});
api.getViews = () => api(`/emby/Users/${encodeURIComponent(user.Id)}/Views`);
api.getItems = (params = {}) => api.query("/emby/Items", params);
api.getLatest = (params = {}) => api.query("/emby/Items/Latest", params);
api.search = (SearchTerm, params = {}) =>
  api.getItems({ ...params, SearchTerm, Recursive: true });
api.getItem = (id) =>
  api(
    `/emby/Users/${encodeURIComponent(user.Id)}/Items/${encodeURIComponent(id)}`,
  );
api.getSeasons = (id, params = {}) =>
  api.query(`/emby/Shows/${encodeURIComponent(id)}/Seasons`, params);
api.getEpisodes = (id, params = {}) =>
  api.query(`/emby/Shows/${encodeURIComponent(id)}/Episodes`, params);
api.getResume = () =>
  api.query("/emby/Items/Resume", { Limit: 18, IncludePlayedAtEnd: true, EnableTotalRecordCount: false, Fields: "PrimaryImageAspectRatio,CommunityRating,RunTimeTicks" });
api.getNextUp = series => api.query('/emby/Shows/NextUp', {SeriesId:series,Limit:1});
api.deleteResume = id => api(`/emby/Items/${encodeURIComponent(id)}/Resume`, "DELETE");
api.deleteAllResume = () => api("/emby/Items/Resume", "DELETE");
api.getPlaybackInfo = (id) =>
  api(`/emby/Items/${encodeURIComponent(id)}/PlaybackInfo`, "POST", {});
api.getUsers = () => api("/admin/users");
api.getSettings = () => api("/admin/enhancements");
api.saveSettings = (data) => api("/admin/enhancements", "PUT", data);
api.uploadCover = (id, file) =>
  api("/admin/cover?Id=" + encodeURIComponent(id), "POST", file, { raw: true });
api.image = (id, kind = "Primary", width = 400) =>
  `/emby/Items/${encodeURIComponent(id)}/Images/${kind}?` +
  new URLSearchParams({ MaxWidth: width, api_key: token });
