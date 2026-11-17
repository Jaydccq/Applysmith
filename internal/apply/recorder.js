// The passive handoff recorder, installed into a tab kept for the user: a
// function the system calls with its session's key in the observer's isolated
// world. It makes no request and sends nothing: each entry waits in the page's
// session storage, sealed with an HMAC-SHA256 under the key, until the system
// reads it and keeps the sealed ones. The buffer names the document last armed
// (at), so a read tells when a navigation dropped this script.
//
// Only trusted events are recorded (event.isTrusted), so the chain's own
// synthetic writes never count: the committed value of a form control on a
// trusted change, a trusted click on a button of a group that holds its
// choice in aria-pressed or aria-checked, under the label above the group, and
// a pick in a custom listbox (a combobox whose options are a list of their
// own, as React-Select draws them). A pick changes no form control, so what
// the widget shows is read a moment after a trusted click on one of its
// options, or Enter or Tab while its list is open. An entry carries the
// control's options, at most 25, and the captions of the form sections
// around it.
//
// The same listeners are attached to every same-origin iframe the page holds
// or loads later (an iframe's load reaches the parent document's capture
// listener); an entry from one carries the top page's URL and the frame's own
// as frameUrl. A cross-origin frame cannot be read and is not covered.
(function (sealKey) {
  var G = "__ajHandoff", KEY = "__ajhr", CAP = 100, MAXV = 500, MAXQ = 300, MAXO = 25, MAXOL = 200;
  // The arming runs in every frame; a same-origin frame is watched from the top.
  if (window !== window.top) return "frame";
  function load() {
    try { var raw = sessionStorage.getItem(KEY); if (raw) return JSON.parse(raw); } catch (x) {}
    return null;
  }
  function save(s) { sessionStorage.setItem(KEY, JSON.stringify(s)); }
  function mark() {
    try {
      var s = load() || { epoch: Math.random().toString(36).slice(2, 10), n: 0, e: [] };
      s.at = performance.timeOrigin; save(s);
      return "armed";
    } catch (x) { return "no-storage"; }
  }
  // The world outlives its session, so a recorder of another key is replaced.
  var prev = window[G];
  if (prev && prev.sealKey === sealKey) return mark();
  if (prev) prev.dispose();
  if (mark() !== "armed") return "no-storage";
  var offs = [];
  function on(doc, type, fn, capture) {
    doc.addEventListener(type, fn, capture);
    offs.push(function () { doc.removeEventListener(type, fn, capture); });
  }

  // seal is the hex HMAC-SHA256 (RFC 2104, FIPS 180-4) of a text's UTF-8 bytes;
  // the constants are the fractional parts of the roots of the first primes.
  var H0 = [], K0 = [], KB = [];
  for (var p = 2, i = 0; i < 64; p++) {
    for (var j = 2; j * j <= p && p % j; j++);
    if (j * j > p) { if (i < 8) H0[i] = Math.sqrt(p) % 1 * 4294967296 | 0; K0[i++] = Math.cbrt(p) % 1 * 4294967296 | 0; }
  }
  for (i = 0; i < 64; i++) KB[i] = parseInt(sealKey.substr(i * 2, 2), 16) || 0;
  function sha256(m) {
    var h = H0.slice(), w = [], x = [], l = m.length, i, j, t, u;
    for (i = 0; i < l; i++) x[i >> 2] |= m[i] << (24 - i % 4 * 8);
    x[l >> 2] |= 0x80 << (24 - l % 4 * 8);
    for (i = (l >> 2) + 1, j = ((l + 8) >> 6) * 16 + 15; i < j; i++) x[i] = 0;
    x[j] = l * 8;
    for (j = 0; j < x.length; j += 16) {
      var a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], k = h[7];
      for (i = 0; i < 64; i++) {
        if (i < 16) w[i] = x[j + i] | 0;
        else {
          t = w[i - 15]; u = w[i - 2];
          w[i] = (((t >>> 7 | t << 25) ^ (t >>> 18 | t << 14) ^ t >>> 3) + w[i - 16] + ((u >>> 17 | u << 15) ^ (u >>> 19 | u << 13) ^ u >>> 10) + w[i - 7]) | 0;
        }
        t = (k + ((e >>> 6 | e << 26) ^ (e >>> 11 | e << 21) ^ (e >>> 25 | e << 7)) + (e & f ^ ~e & g) + K0[i] + w[i]) | 0;
        u = (((a >>> 2 | a << 30) ^ (a >>> 13 | a << 19) ^ (a >>> 22 | a << 10)) + (a & b ^ a & c ^ b & c)) | 0;
        k = g; g = f; f = e; e = d + t | 0; d = c; c = b; b = a; a = t + u | 0;
      }
      h[0] = h[0] + a | 0; h[1] = h[1] + b | 0; h[2] = h[2] + c | 0; h[3] = h[3] + d | 0;
      h[4] = h[4] + e | 0; h[5] = h[5] + f | 0; h[6] = h[6] + g | 0; h[7] = h[7] + k | 0;
    }
    return h;
  }
  function seal(text) {
    var inner = sha256(KB.map(function (b) { return b ^ 0x36; }).concat(Array.from(new TextEncoder().encode(text)))), out = [];
    inner.forEach(function (v) { out.push(v >>> 24, v >>> 16 & 255, v >>> 8 & 255, v & 255); });
    return sha256(KB.map(function (b) { return b ^ 0x5c; }).concat(out)).map(function (v) { return (v >>> 0).toString(16).padStart(8, "0"); }).join("");
  }

  // The epoch and sequence number are in the sealed text: a copy is the same entry.
  function record(en, key) {
    try {
      var s = load(); if (!s) return;
      // The pressed button replaces an earlier press of the same group: Yes then No leaves No.
      if (key) s.e = s.e.filter(function (x) { return x.k !== key; });
      en.epoch = s.epoch; en.seq = ++s.n;
      var d = JSON.stringify(en);
      s.e.push({ n: en.seq, k: key, d: d, m: seal(d) });
      if (s.e.length > CAP) s.e.splice(0, s.e.length - CAP);
      save(s);
    } catch (x) {}
  }

  function clean(t, n) {
    t = String(t == null ? "" : t).replace(/\s+/g, " ").trim();
    t = t.replace(/\s*\*+\s*$/, "").replace(/\s*\(required\)\s*$/i, "").replace(/:\s*$/, "").trim();
    return t.length > n ? t.slice(0, n) : t;
  }
  function text(n) { return n ? clean(n.innerText || n.textContent, MAXQ) : ""; }
  function byIds(ids, doc) {
    var out = [];
    String(ids || "").split(/\s+/).forEach(function (id) {
      var n = id && doc.getElementById(id); if (n) out.push(text(n));
    });
    return clean(out.join(" "), MAXQ);
  }
  function ownLabel(el) {
    var t = byIds(el.getAttribute("aria-labelledby"), el.ownerDocument) || clean(el.getAttribute("aria-label"), MAXQ);
    if (t) return t;
    if (el.labels && el.labels.length) t = text(el.labels[0]);
    if (!t) {
      var w = el.closest("label");
      if (w) {
        var c = w.cloneNode(true);
        c.querySelectorAll("input,select,textarea").forEach(function (n) { n.remove(); });
        t = text(c);
      }
    }
    return t;
  }
  // above is the text of the nearest block before the node that holds no control.
  function above(node) {
    for (var n = node, d = 0; n && n !== node.ownerDocument.body && d < 5; n = n.parentElement, d++) {
      for (var p = n.previousElementSibling; p; p = p.previousElementSibling) {
        if (p.querySelector("input,select,textarea,button")) break;
        var t = text(p); if (t) return t;
      }
    }
    return "";
  }
  // labels is at most 25 option labels, as many as the engine sends.
  function labels(list) {
    var out = [];
    for (var i = 0; i < list.length && out.length < MAXO; i++) { var t = clean(list[i], MAXOL); if (t) out.push(t); }
    return out;
  }
  // SECTION is the engine's vocabulary of the captions that name a part of an
  // application form.
  var SECTION = /education|employment|work history|work experience|experience|reference|personal|contact|address|equal opportunity|eeo|self[- ]identif|voluntary|legal|authorization|emergency|skills|links|additional|application questions|^entry\s*#?\s*\d+$/i;
  // sectionOf is the captions of the sections around a node, outermost first:
  // a fieldset's legend, and the caption of a section, region or group (the
  // text it is labelled by, else its own heading). A caption is one only in
  // the engine's vocabulary, and the question's own is none.
  function sectionOf(node, q) {
    var out = [];
    function push(t) { if (t && t !== q && SECTION.test(t)) out.unshift(t); }
    for (var a = node; a && a.tagName !== "BODY" && a.tagName !== "HTML"; a = a.parentElement) {
      var role = a.getAttribute("role");
      if (a.tagName === "FIELDSET") push(text(a.querySelector(":scope > legend")));
      if (a.tagName === "SECTION" || role === "region" || role === "group")
        push(byIds(a.getAttribute("aria-labelledby"), a.ownerDocument) || text(a.querySelector(":scope > h1, :scope > h2, :scope > h3, :scope > h4, :scope > header > h1, :scope > header > h2, :scope > header > h3")));
    }
    return out;
  }
  function groupLabel(node) {
    var g = node.closest("fieldset,[role=radiogroup],[role=group]");
    if (g) {
      var lg = g.querySelector("legend");
      var t = (lg && text(lg)) || byIds(g.getAttribute("aria-labelledby"), g.ownerDocument) || clean(g.getAttribute("aria-label"), MAXQ);
      if (t) return t;
    }
    return above(g || node);
  }

  function isControl(el) {
    if (!el || !el.tagName) return false;
    if (el.tagName === "SELECT" || el.tagName === "TEXTAREA") return true;
    if (el.tagName !== "INPUT") return false;
    return !/^(hidden|submit|button|reset|image|file)$/i.test(el.type);
  }
  function optionText(el) {
    var o = el.options && el.options[el.selectedIndex];
    return o ? clean(o.textContent, MAXQ) : "";
  }
  // current is the control's value as the system would have written it: an
  // option's label for a choice, "true" or "false" for a box.
  function current(el) {
    if (el.tagName === "SELECT") return optionText(el);
    if (el.type === "checkbox") return String(el.checked);
    if (el.type === "radio") return el.checked ? ownLabel(el) : "";
    return el.value;
  }
  // optionsOf is the labels of a choice control's options: a menu's, but for a
  // first one with no value (its placeholder), or those of the radios of its
  // group.
  function optionsOf(el) {
    if (el.tagName === "SELECT") return labels(Array.prototype.filter.call(el.options, function (o, i) { return i > 0 || o.value !== ""; }).map(function (o) { return o.textContent; }));
    if (el.type !== "radio") return [];
    return labels(Array.prototype.filter.call(el.ownerDocument.querySelectorAll("input[type=radio]"), function (r) {
      return r === el || (el.name && r.name === el.name && r.form === el.form);
    }).map(ownLabel));
  }
  function kindOf(el) {
    if (el.tagName === "SELECT") return "select";
    if (el.type === "radio") return "radio-group";
    if (el.type === "checkbox") return "checkbox";
    return "text";
  }
  // secret reads the controls whose value never leaves the page. A word of a
  // self-identification question is matched whole, as the form writes it.
  var SECRET_AC = /(^|\s)(one-time-code|current-password|new-password|cc-[a-z-]+|bday[a-z-]*)(\s|$)/i;
  var SECRET_Q = /\b(password|passcode|pass\s*phrase|pin|otp|one[- ]time|verification code|security code|authenticator|card number|cvv|cvc|credit card|debit card|iban|routing number|account number|ssn|social security|national (id|insurance)|passport|date of birth|birth ?date|birthday|dob|(trans)?gender|sex|race|ethnic(ity|ities)?|hispanic|veteran|disabilit(y|ies)|sexual orientation|pronouns?|e-?mail|phone|mobile|telephone|(first|last|middle|full|legal|preferred) name)\b/i;
  function secret(el, q) {
    var ac = el.getAttribute("autocomplete") || "";
    return el.type === "password" || el.type === "email" || el.type === "tel" || SECRET_AC.test(ac) ||
      SECRET_Q.test(q + " " + (el.name || "") + " " + (el.id || ""));
  }

  // radios holds, per document, the label of each group's checked radio.
  var seen = new WeakMap(), radios = new WeakMap(), watched = new WeakSet();
  function groups(doc) { if (!radios.has(doc)) radios.set(doc, {}); return radios.get(doc); }
  function rkey(el) { return (el.form ? Array.prototype.indexOf.call(el.ownerDocument.forms, el.form) : "-") + "|" + el.name; }
  function snap(el) {
    if (el.type === "radio") { if (el.checked) groups(el.ownerDocument)[rkey(el)] = ownLabel(el); return; }
    if (!seen.has(el)) seen.set(el, current(el));
  }

  // pathOf is a document's address without query or hash.
  function pathOf(doc) {
    var l = doc.location;
    return l.protocol === "http:" || l.protocol === "https:" ? l.origin + l.pathname : l.protocol + l.pathname;
  }
  function base(en, doc) {
    en.url = location.origin + location.pathname;
    if (doc !== document) en.frameUrl = pathOf(doc);
    return en;
  }

  // COMBO is a combobox, CONTROL any control a person answers with.
  var COMBO = "[role=combobox],input[aria-autocomplete]";
  var CONTROL = "input:not([type=hidden]),select,textarea,[role=combobox]";
  // widget is the largest box around a combobox that holds no other control.
  function widget(cb) {
    var w = cb;
    for (var n = cb.parentElement, d = 0; n && n.tagName !== "BODY" && d < 5; n = n.parentElement, d++) {
      if (n.querySelectorAll(CONTROL).length > 1) break;
      w = n;
    }
    return w;
  }
  // SHOWN names the boxes a React-Select shows its chosen options in, by its
  // own class names or the generated ones (css-<hash>-singleValue).
  var SHOWN = /(__single-value|__multi-value|-singleValue|-multiValue)$/;
  function isShown(n) {
    for (var i = 0; i < n.classList.length; i++) if (SHOWN.test(n.classList[i])) return !n.closest("[role=listbox]");
    return false;
  }
  // shown is what a combobox shows as chosen, one entry per chip; one that
  // draws no such box shows its own value or text.
  function shown(cb) {
    var out = [];
    widget(cb).querySelectorAll("*").forEach(function (n) { var t = isShown(n) && text(n); if (t) out.push(t); });
    if (out.length) return out;
    var t = cb.tagName === "INPUT" ? clean(cb.value, MAXV) : text(cb);
    return t ? [t] : [];
  }
  // focused is the combobox that holds the focus: a widget keeps it in its box
  // while its list is open, and a click on an option does not take it away.
  function focused(doc) {
    var f = doc.activeElement;
    return f && f.closest ? f.closest(COMBO) : null;
  }
  // listboxOf is the list a combobox names as the one it controls.
  function listboxOf(cb) {
    var ids = ((cb.getAttribute("aria-controls") || "") + " " + (cb.getAttribute("aria-owns") || "")).split(/\s+/);
    for (var i = 0; i < ids.length; i++) {
      var n = ids[i] && cb.ownerDocument.getElementById(ids[i]);
      if (n) return n.matches("[role=listbox]") ? n : n.querySelector("[role=listbox]");
    }
    return null;
  }
  // picks holds what each combobox showed when it was last read: when it
  // gained focus, and after each pick.
  var picks = new WeakMap();
  // pick reads what the combobox shows a moment after a trusted pick. picked
  // is the chosen option's label when the pick names the option; a key names
  // none, and what the widget then shows is a pick only when it is one of the
  // list's options, so a key that only typed or moved on is none.
  function pick(cb, lb, picked) {
    var prior = picks.has(cb) ? picks.get(cb) : [];
    var all = lb ? Array.prototype.map.call(lb.querySelectorAll("[role=option]"), text) : [];
    setTimeout(function () {
      var now = shown(cb);
      picks.set(cb, now);
      if (!now.length || now.join("\n") === prior.join("\n")) return;
      var label = picked || now.filter(function (t) { return prior.indexOf(t) < 0; }).pop() || now[now.length - 1];
      if (!picked && all.indexOf(label) < 0) return;
      var w = widget(cb), q = ownLabel(cb) || above(w), skip = secret(cb, q);
      record(base({
        question: q, kind: "custom-listbox", inputType: cb.tagName === "INPUT" ? cb.type : "", autocomplete: clean(cb.getAttribute("autocomplete"), 60),
        value: skip ? "" : label, optionLabel: skip ? "" : label, prior: skip ? "" : clean(prior.join(", "), MAXV), skipped: skip,
        options: skip ? [] : labels(all), section: skip ? [] : sectionOf(w, q)
      }, cb.ownerDocument));
    }, 120);
  }

  function onFocus(e) {
    var el = e.target;
    if (isControl(el)) snap(el);
    if (el.matches && el.matches(COMBO)) picks.set(el, shown(el));
  }

  function onChange(e) {
    var el = e.target;
    if (!isControl(el)) return;
    var now = current(el), prior;
    if (el.type === "radio") { var gs = groups(el.ownerDocument); prior = gs[rkey(el)] || ""; gs[rkey(el)] = now; }
    else { prior = seen.has(el) ? seen.get(el) : ""; seen.set(el, now); }
    if (!e.isTrusted) return;
    // A combobox's text is a search until the widget keeps it: a search the
    // widget empties when it is left answered nothing.
    if (el.matches(COMBO)) setTimeout(function () { if (current(el) === now) commit(el, now, prior); }, 120);
    else commit(el, now, prior);
  }
  function commit(el, now, prior) {
    var q = el.type === "radio" || el.type === "checkbox" ? (groupLabel(el) || ownLabel(el)) : (ownLabel(el) || clean(el.placeholder || el.name, MAXQ));
    var skip = secret(el, q);
    // A cleared box commits nothing worth learning.
    if (!skip && clean(el.type === "radio" ? el.value : now, MAXV) === "") return;
    var label = el.type === "radio" || el.type === "checkbox" ? ownLabel(el) : el.tagName === "SELECT" ? now : "";
    record(base({
      question: q, kind: kindOf(el), inputType: el.type || el.tagName.toLowerCase(), autocomplete: clean(el.getAttribute("autocomplete"), 60),
      value: skip ? "" : clean(el.type === "radio" ? el.value : now, MAXV), optionLabel: skip ? "" : label,
      prior: skip ? "" : clean(prior, MAXV), skipped: skip, options: skip ? [] : optionsOf(el), section: skip ? [] : sectionOf(el, q)
    }, el.ownerDocument));
  }

  function isOn(b) { return b.getAttribute("aria-pressed") === "true" || b.getAttribute("aria-checked") === "true"; }
  function onClick(e) {
    if (!e.isTrusted || !e.target.closest) return;
    var o = e.target.closest("[role=option]");
    if (o) {
      var lb = o.closest("[role=listbox]"), cb = lb && focused(o.ownerDocument);
      if (cb) pick(cb, lb, text(o));
      return;
    }
    var b = e.target.closest("[aria-pressed],[aria-checked]");
    if (!b || b.tagName === "INPUT" || !b.parentElement) return;
    var group = Array.prototype.filter.call(b.parentElement.children, function (c) {
      return c.hasAttribute("aria-pressed") || c.hasAttribute("aria-checked");
    });
    if (group.length < 2 || isOn(b)) return;
    var priorEl = group.filter(isOn)[0], prior = priorEl ? text(priorEl) : "";
    setTimeout(function () {
      if (!isOn(b)) return;
      var doc = b.ownerDocument;
      var q = clean(b.parentElement.getAttribute("aria-label"), MAXQ) || byIds(b.parentElement.getAttribute("aria-labelledby"), doc) || groupLabel(b.parentElement);
      var t = text(b), skip = SECRET_Q.test(q);
      record(base({
        question: q, kind: "radio-group", inputType: "button", autocomplete: "",
        value: skip ? "" : t, optionLabel: skip ? "" : t, prior: skip ? "" : prior, skipped: skip,
        options: skip ? [] : labels(group.map(text)), section: skip ? [] : sectionOf(b.parentElement, q)
      }, doc), "pressed|" + pathOf(doc) + "|" + q);
    }, 120);
  }
  function onKey(e) {
    if (!e.isTrusted || (e.key !== "Enter" && e.key !== "Tab") || !e.target.closest) return;
    var cb = e.target.closest(COMBO);
    if (cb && cb.getAttribute("aria-expanded") === "true") pick(cb, listboxOf(cb), "");
  }

  // frame attaches to an iframe's document when this page may read it: a
  // cross-origin frame has none to give.
  function frame(f) {
    var doc = null;
    try { doc = f.contentDocument; } catch (x) {}
    if (doc && doc.defaultView) watch(doc);
  }
  function onLoad(e) {
    var t = e.target;
    if (t && (t.tagName === "IFRAME" || t.tagName === "FRAME")) frame(t);
  }
  // watch attaches the listeners to one document and the frames it holds, once.
  function watch(doc) {
    if (watched.has(doc)) return;
    watched.add(doc);
    doc.querySelectorAll("input,select,textarea").forEach(function (el) { if (isControl(el)) snap(el); });
    on(doc, "focusin", onFocus, true);
    on(doc, "change", onChange, true);
    on(doc, "click", onClick, true);
    on(doc, "keydown", onKey, true);
    on(doc, "load", onLoad, true);
    doc.querySelectorAll("iframe,frame").forEach(frame);
  }
  watch(document);

  window[G] = { sealKey: sealKey, dispose: function () { offs.forEach(function (o) { o(); }); } };
  return "armed";
})
