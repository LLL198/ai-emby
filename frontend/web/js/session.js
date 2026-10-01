// Remember only the session token and user profile; never store passwords.
const DeviceSession = (() => {
  const key = "go-emby-device-session";
  const empty = () => ({ token: "", user: null, persistent: false });
  function decode(raw) {
    try {
      const value = JSON.parse(raw);
      if (typeof value?.token === "string" && value.token && typeof value.user?.Id === "string" && value.user.Id)
        return { token: value.token, user: value.user, persistent: value.persistent === true };
    } catch {}
    return empty();
  }
  function clearLegacy() {
    try { sessionStorage.removeItem("token"); sessionStorage.removeItem("user"); } catch {}
  }
  function read() {
    try {
      const saved = localStorage.getItem(key);
      // An empty saved entry is a logout marker. Never revive a stale tab's
      // former sessionStorage login after another tab signs out.
      if (saved !== null) { clearLegacy(); return decode(saved); }
    } catch {}
    try {
      return decode(JSON.stringify({ token: sessionStorage.getItem("token"), user: JSON.parse(sessionStorage.getItem("user") || "null") }));
    } catch { clearLegacy(); return empty(); }
  }
  function save(accessToken, profile, persistent = false) {
    const value = JSON.stringify({ version: 1, token: accessToken, user: profile, persistent });
    try {
      localStorage.setItem(key, value);
      clearLegacy();
      return true;
    } catch {
      // A browser that disables persistent storage can still use this tab.
      try { sessionStorage.setItem("token", accessToken); sessionStorage.setItem("user", JSON.stringify(profile)); } catch {}
      return false;
    }
  }
  function clear() {
    clearLegacy();
    try { localStorage.setItem(key, JSON.stringify({ version: 1, ...empty() })); } catch {}
  }
  function deviceID() {
    try {
      const saved = localStorage.getItem("device");
      if (saved) return saved;
    } catch {}
    const id = globalThis.crypto?.randomUUID ? crypto.randomUUID() : String(Math.random()).slice(2);
    try { localStorage.setItem("device", id); } catch {}
    return id;
  }
  return { key, read, decode, save, clear, deviceID };
})();
