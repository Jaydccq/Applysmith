(() => {
  const result = { origin: location.origin };
  window.__ajSmoke = null;
  const render = () => {
    const pre = document.createElement("pre");
    pre.id = "aj-smoke";
    pre.textContent = JSON.stringify(result, null, 2);
    document.body.appendChild(pre);
  };
  // A cookie session also needs the key the dashboard page holds: the redirect
  // fragment before the page adopts it, the page's storage after.
  const sessionHeaders = () => {
    const m = /[#&]k=([A-Za-z0-9_-]+)/.exec(location.hash);
    const key = m ? m[1] : localStorage.getItem("applysmith.sessionKey");
    return key ? { "X-Applysmith-Session-Key": key } : {};
  };
  (async () => {
    try {
      const r = await fetch("/api/health", { credentials: "same-origin", headers: sessionHeaders() });
      const body = await r.json();
      const d = body.data || {};
      result.health = { http: r.status, status: d.status, detailVisible: d.uptimeSeconds !== undefined };
    } catch (e) {
      result.health = { error: String(e) };
    }
    try {
      const r = await fetch("/api/pairing/codes", { method: "POST", credentials: "same-origin", headers: sessionHeaders() });
      result.sessionOnlyWrite = { http: r.status };
    } catch (e) {
      result.sessionOnlyWrite = { error: String(e) };
    }
    result.cookieVisibleToScript = document.cookie.includes("applysmith_session");
    // mygo 0.5 hands the page the desktop's accessibility settings; the
    // dashboard mirrors Reduce Transparency as data-opaque on the root.
    const prefs = window.mygo.preferences;
    result.desktopPreferences = prefs
      ? { reduceTransparency: prefs.reduceTransparency, opaque: document.documentElement.hasAttribute("data-opaque") }
      : { error: "no preferences" };
    try {
      result.binding = await window.mygo.call("Shell.GetShellInfo");
    } catch (e) {
      result.binding = { error: String(e) };
    }
    // Calls the shell must refuse; none of them opens anything.
    const refused = async (name, ...args) => {
      try {
        await window.mygo.call(name, ...args);
        return "accepted";
      } catch (e) {
        return String(e && e.message);
      }
    };
    result.refusals = {
      reveal: await refused("Shell.RevealInFinder", "/etc/hosts"),
      external: await refused("Shell.OpenExternal", "javascript:void(0)"),
      pickFile: await refused("Shell.PickFile", []),
    };
    window.__ajSmoke = result;
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", render);
    else render();
  })();
})();
