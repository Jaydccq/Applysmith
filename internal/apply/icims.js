// The iCIMS email-first step, read and driven by page scripts. The step is the
// tenant's own form (id enterEmailForm, action carrying step=email, one email
// box named css_loginName, a Next button, no password box), either in the top
// document or in the tenant's content frame; a selector from the top document
// cannot reach into that frame, so every command works on the same-origin
// documents it finds. A function expression called with a command and an
// argument, like probe.js; it returns JSON-compatible data.
(function (cmd, arg) {
  function docs() {
    const out = [document];
    for (let i = 0; i < out.length && out.length < 12; i++) {
      for (const f of out[i].querySelectorAll("iframe,frame")) {
        try { if (f.contentDocument) out.push(f.contentDocument); } catch (x) {}
      }
    }
    return out;
  }
  const shown = (e) => {
    const r = e.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    const cs = e.ownerDocument.defaultView.getComputedStyle(e);
    return cs.display !== "none" && cs.visibility !== "hidden";
  };
  const clip = (s, n) => (s || "").replace(/\s+/g, " ").trim().slice(0, n);

  function step() {
    for (const d of docs()) {
      const form = d.querySelector("form#enterEmailForm");
      if (!form || !/[?&]step=email(?:&|$)/.test(form.getAttribute("action") || "")) continue;
      const email = form.querySelector("input[name='css_loginName']");
      const next = form.querySelector("#enterEmailSubmitButton");
      if (!email || !next || form.querySelector("input[type='password']") || !shown(email)) continue;
      return { d, form, email, next };
    }
    return null;
  }

  function find() {
    const s = step();
    if (!s) return { found: false };
    const labels = [];
    for (const l of [s.next.tagName === "INPUT" ? s.next.value : s.next.textContent, s.next.getAttribute("aria-label")]) { const c = clip(l, 120); if (c && !labels.includes(c)) labels.push(c); }
    return { found: true, framed: s.d !== document, labels, euBox: !!s.form.querySelector("input[type='checkbox']#eu_resident") };
  }

  // Types the email, sets the EU-resident box from the profile, ticks the
  // consent box that only the EU branch shows, and presses Next. "disabled"
  // means Next stayed disabled after the email was typed; it is never forced.
  function press(a) {
    const s = step();
    if (!s) return "gone";
    const desc = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(s.email), "value");
    s.email.focus();
    if (desc && desc.set) desc.set.call(s.email, a.email); else s.email.value = a.email;
    for (const type of ["input", "keyup", "change"]) s.email.dispatchEvent(new Event(type, { bubbles: true }));
    const eu = s.form.querySelector("input[type='checkbox']#eu_resident");
    if (eu && eu.checked !== a.eu) eu.click();
    const gdpr = s.form.querySelector("input[type='checkbox']#accept_gdpr");
    if (a.eu && gdpr && !gdpr.checked) gdpr.click();
    if (s.next.disabled) return "disabled";
    s.next.click();
    return "pressed";
  }

  function kindOf(e) {
    const src = e.tagName === "IFRAME" ? e.src || "" : "";
    if (e.classList.contains("g-recaptcha")) return "recaptcha_v2";
    if (e.classList.contains("h-captcha")) return "hcaptcha";
    if (e.classList.contains("cf-turnstile")) return "turnstile_widget";
    if (/recaptcha\/(api2|enterprise)\/(anchor|bframe)/.test(src)) return /enterprise/.test(src) ? "recaptcha_v2_enterprise" : "recaptcha_v2";
    if (/hcaptcha\.com\/captcha/.test(src)) return "hcaptcha";
    if (/challenges\.cloudflare\.com\/.*turnstile/.test(src)) return "turnstile_widget";
    return "";
  }
  function widgets() {
    const out = [];
    for (const d of docs()) {
      for (const e of d.querySelectorAll(".g-recaptcha,.h-captcha,.cf-turnstile,iframe")) {
        const kind = kindOf(e);
        if (!kind) continue;
        const src = e.tagName === "IFRAME" ? e.src || "" : "";
        let key = e.getAttribute("data-sitekey") || "";
        if (!key && src) { try { const u = new URL(src); key = u.searchParams.get("k") || u.searchParams.get("sitekey") || (u.hash.match(/sitekey=([^&]+)/) || [])[1] || ""; } catch (x) {} }
        const r = e.getBoundingClientRect();
        out.push({ kind, siteKey: key, visible: shown(e), invisible: e.getAttribute("data-size") === "invisible", w: r.width, h: r.height, bframeOpen: /bframe|frame=challenge/.test(src) && r.width > 100 && r.height > 100 });
      }
    }
    return out;
  }
  const RESP = /^(g-recaptcha-response|h-captcha-response|cf-turnstile-response)/;
  function resp() {
    const out = [];
    for (const d of docs()) for (const e of d.querySelectorAll("textarea,input")) {
      const n = e.getAttribute("name") || "";
      if (RESP.test(n)) out.push({ name: n, empty: !(e.value || "").length });
    }
    return out;
  }

  function state() {
    return { here: !!step(), widgets: widgets(), respFields: resp() };
  }

  const FIELD = { recaptcha_v2: ["g-recaptcha-response"], recaptcha_v2_enterprise: ["g-recaptcha-response"], hcaptcha: ["h-captcha-response", "g-recaptcha-response"], turnstile_widget: ["cf-turnstile-response"] };
  function setValue(e, v) {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e), "value").set.call(e, v);
    e.dispatchEvent(new Event("input", { bubbles: true }));
    e.dispatchEvent(new Event("change", { bubbles: true }));
  }
  // The token goes into the response fields of the step's form, one created
  // when the widget left none. The tenant's own callback is a closure inside
  // its render call and cannot be reached, so none is called; send() does what
  // the callback would have done.
  function deliver(a) {
    const s = step();
    const out = { fields: 0, created: false, callback: "" };
    if (!s) return out;
    for (const name of FIELD[a.kind] || []) {
      let f = s.form.querySelector("[name='" + name + "']");
      if (!f) {
        f = s.d.createElement("textarea");
        f.name = name;
        f.style.display = "none";
        s.form.appendChild(f);
        out.created = true;
      }
      setValue(f, a.token);
      out.fields++;
    }
    return out;
  }
  function blank(kind) {
    const s = step();
    let n = 0;
    if (!s) return n;
    for (const name of FIELD[kind] || []) {
      const f = s.form.querySelector("[name='" + name + "']");
      if (f) { setValue(f, ""); n++; }
    }
    return n;
  }

  // What the tenant's callback does once the check passes: add
  // behavior-type=other to the form and submit it natively. The native submit
  // skips the page's own submit listener, which would run the check again.
  function send() {
    const s = step();
    if (!s) return false;
    if (!s.form.querySelector("input[name='behavior-type']")) {
      const b = s.d.createElement("input");
      b.type = "hidden";
      b.name = "behavior-type";
      b.value = "other";
      s.form.appendChild(b);
    }
    s.d.defaultView.HTMLFormElement.prototype.submit.call(s.form);
    return true;
  }

  switch (cmd) {
    case "find": return find();
    case "press": return press(arg);
    case "state": return state();
    case "deliver": return deliver(arg);
    case "blank": return blank(arg);
    case "send": return send();
  }
  return null;
})
