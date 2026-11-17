// The page probe: one read-only look at the document's facts (text, controls,
// dialogs, captcha widgets, frames, step markers), the helpers that find a
// control again at press time, and `place`, which attaches handles and page
// context to the controls the engine's inspect result names. It lists no
// fields: the walker's field inventory comes only from the engine. It is a
// function expression called with a command and an argument; it returns
// JSON-compatible data and never writes to the page. Element references live
// in a symbol-keyed list on the window; an element keeps its handle for the
// life of the page, so a handle read before a later probe still names it.
(function (cmd, arg) {
  const W = globalThis;
  const KEY = Symbol.for("applysmith.apply.refs");
  const FORMS = Symbol.for("applysmith.apply.forms");
  const HANDLES = Symbol.for("applysmith.apply.handles");
  const handle = (el) => {
    const refs = W[KEY] || (W[KEY] = []);
    const known = W[HANDLES] || (W[HANDLES] = new WeakMap());
    const i = known.get(el);
    if (i !== undefined && refs[i] === el) return i;
    refs.push(el);
    known.set(el, refs.length - 1);
    return refs.length - 1;
  };
  const clip = (s, n) => (s || "").replace(/\s+/g, " ").trim().slice(0, n);
  // A form's id property is its control named "id" when it has one, so the
  // attribute is read instead.
  const idOf = (e) => (e.getAttribute && e.getAttribute("id")) || "";

  function* walk(root) {
    const stack = [root];
    while (stack.length) {
      const node = stack.pop();
      const kids = node.children ? Array.from(node.children) : [];
      for (let i = kids.length - 1; i >= 0; i--) stack.push(kids[i]);
      if (node.shadowRoot) stack.push(node.shadowRoot);
      if (node.nodeType === 1) yield node;
    }
  }
  const all = () => Array.from(walk(document.documentElement));

  const PAGER = '.srccarPager [data-dojo-attach-point="thePagerDualNext"], .srccarPager [data-dojo-attach-point="thePagerSingleGo"]';

  function rectOf(el) {
    const r = el.getBoundingClientRect();
    return { x: r.left, y: r.top, w: r.width, h: r.height };
  }
  function visible(el) {
    if (!el.isConnected) return false;
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    for (let n = el; n; n = n.parentNode ? (n.parentNode.host || n.parentNode) : null) {
      if (n.nodeType !== 1) continue;
      const cs = getComputedStyle(n);
      if (cs.display === "none" || cs.visibility === "hidden" || cs.visibility === "collapse" || Number(cs.opacity) === 0) return false;
      if (n.hasAttribute && n.hasAttribute("hidden") && cs.display === "none") return false;
    }
    return true;
  }
  // A file input is usually hidden behind a styled button: it counts when no
  // ancestor is hidden, even if the input itself has no size.
  function ancestorsShown(el) {
    for (let n = el.parentNode ? (el.parentNode.host || el.parentNode) : null; n; n = n.parentNode ? (n.parentNode.host || n.parentNode) : null) {
      if (n.nodeType !== 1) continue;
      const cs = getComputedStyle(n);
      if (cs.display === "none" || cs.visibility === "hidden") return false;
    }
    return true;
  }
  function up(el, test) {
    for (let n = el; n; n = n.parentNode && (n.parentNode.host || n.parentNode)) {
      if (n.nodeType === 1 && test(n)) return n;
    }
    return null;
  }
  const MODAL = "dialog[open],[role=dialog],[role=alertdialog],[aria-modal=true]";
  // A cookie notice is often marked up as a dialog but blocks nothing: it is
  // site chrome, so it neither hides the form's own controls nor offers any.
  const cookieWords = /cookie|gdpr|onetrust|\bcc-window\b/i;
  const isCookieBanner = (n) => !!n.matches && n.matches(MODAL)
    && (cookieWords.test([idOf(n), typeof n.className === "string" ? n.className : "", n.getAttribute("aria-label") || ""].join(" "))
      || (!n.querySelector("input:not([type=hidden]):not([type=checkbox]),select,textarea") && /\bcookies?\b/i.test(clip(n.textContent, 400))));
  // A site's own menu marked up as a dialog in its header (Two Sigma's
  // hamburger, labelled "navigation", drawn open on a wide window) blocks
  // nothing either.
  const menuWords = /\b(?:nav|navigation|menu)\b/i;
  const isSiteMenu = (n) => !!n.matches && n.matches(MODAL) && !!up(n.parentNode, (m) => m.matches && m.matches("header,[role=banner]"))
    && menuWords.test((n.getAttribute("aria-label") || "") + " " + (typeof n.className === "string" ? n.className.replace(/[_-]+/g, " ") : ""));
  const blocksNothing = (n) => isCookieBanner(n) || isSiteMenu(n);
  const isModal = (el) => !!up(el, (n) => n.matches && n.matches(MODAL) && !blocksNothing(n));
  // A footer inside the page's main region or a form is the form's own action bar
  // (SmartRecruiters draws its Next in one), not the site's footer.
  const inMain = (n) => !!up(n.parentNode && (n.parentNode.host || n.parentNode), (m) => m.matches && m.matches("main,[role=main],form"));
  // An Apply control in a navigation landmark inside the main content is the
  // posting's own action bar (Avature's <div role="navigation"> in the aside).
  const applyShaped = (el) => /\bapply\b/i.test(((el.getAttribute && el.getAttribute("aria-label")) || "") + " " + (el.textContent || ""));
  const isChrome = (el) => !!up(el, (n) => n.matches && ((n.matches("header,nav,menu,[role=banner],[role=navigation]") && !(inMain(n) && applyShaped(el))) || (n.matches("footer,[role=contentinfo]") && !inMain(n)) || blocksNothing(n)));
  // A recruiting assistant renders in a custom element's shadow root (its host is
  // named for the widget) with a hidden upload of its own. The document's own
  // body and root are never the widget, whatever their class says (Phenom
  // marks <body> "ph-chatbot-parent-body").
  const isChat = (el) => !!up(el, (n) => n.matches && !n.matches("html,body") && /(^|[-_ ])(chat|assistant|chatbot|intercom|drift|zendesk)([-_ ]|$)/i.test(idOf(n) + " " + (typeof n.className === "string" ? n.className : "")) || /^(?:apply|chat|assistant|conversation)-widget$|^[a-z]+-chat(?:bot)?$/i.test(n.tagName || ""));

  function textOf(el) {
    return clip(el.innerText || el.textContent || "", 120);
  }
  // A control's own words leave out a copy drawn for assistive tech to skip
  // (Optiver draws "Apply" twice for a hover animation, one copy aria-hidden)
  // and a part the page does not draw (Toast's "Please wait..." and "Done"
  // states beside "Apply now!"); a control whose only words are such a copy
  // reads as before.
  function ownWords(el) {
    if (!el.querySelector('[aria-hidden="true"]')) return textOf(el);
    const parts = [];
    const drawn = (c) => (c.checkVisibility ? c.checkVisibility({ visibilityProperty: true }) : getComputedStyle(c).display !== "none");
    const walk = (n) => {
      for (const c of n.childNodes) {
        if (c.nodeType === 3) parts.push(c.nodeValue);
        else if (c.nodeType === 1 && c.getAttribute("aria-hidden") !== "true" && drawn(c)) walk(c);
      }
    };
    walk(el);
    return clip(parts.join(" "), 120) || textOf(el);
  }
  function labelOf(el) {
    const parts = [];
    if (el.labels && el.labels.length) parts.push(Array.from(el.labels).map((l) => l.innerText || l.textContent).join(" "));
    const lb = el.getAttribute && el.getAttribute("aria-labelledby");
    if (lb) parts.push(lb.split(/\s+/).map((id) => { const t = document.getElementById(id); return t ? t.textContent : ""; }).join(" "));
    if (el.getAttribute) parts.push(el.getAttribute("aria-label") || "", el.getAttribute("placeholder") || "");
    const fs = el.closest && el.closest("fieldset");
    if (fs && fs.querySelector("legend")) parts.push(fs.querySelector("legend").textContent);
    return clip(parts.filter(Boolean).join(" "), 160);
  }

  function probe() {
    const els = all();
    const ref = handle;

    const forms = [];
    const formIndex = new Map();
    for (const f of els.filter((e) => e.tagName === "FORM")) {
      if (!visible(f)) continue;
      formIndex.set(f, forms.length);
      forms.push({ id: idOf(f), name: f.getAttribute("name") || "", action: clip(f.getAttribute("action") || "", 120), role: f.getAttribute("role") || "", cls: clip(typeof f.className === "string" ? f.className : "", 120), text: clip(f.innerText, 300), textLen: clip(f.innerText, 100000).length });
    }

    const controls = [];
    const googleAccounts = location.protocol === "https:" && location.hostname === "accounts.google.com";
    const ctlSel = (googleAccounts ? "[role=link][data-identifier]," : "") + "button,input[type=submit],input[type=button],[role=button],[role=option],[role=menuitem],a[href],summary";
    // A design system's own button element (ukg-button, sdf-button) is a control.
    const customButton = (e) => /^[a-z]+(?:-[a-z]+)*-button$/i.test(e.tagName);
    // The job card a control sits in (an article or list item), numbered from
    // 1: two cards that offer the same Apply are a list of other jobs.
    const cards = [];
    const cardOf = (e) => {
      const c = e.closest('article, li, [role="listitem"]');
      if (!c) return 0;
      if (!cards.includes(c)) cards.push(c);
      return cards.indexOf(c) + 1;
    };
    for (const e of els) {
      if (!(e.matches(ctlSel) || customButton(e))) continue;
      if (!visible(e)) continue;
      const own = e.tagName === "INPUT" ? (e.value || "") : ownWords(e);
      const aria = e.getAttribute("aria-label") || "";
      const title = e.getAttribute("title") || "";
      const labels = [];
      // A button's value is form data; it reads as words only when it holds a
      // letter (Avature's Next posts value="292").
      const data = e.tagName === "INPUT" || !/\p{L}/u.test(e.value || "") ? "" : e.value;
      for (const l of [own, aria, title, googleAccounts ? e.getAttribute("data-identifier") || "" : "", data]) { const c = clip(l, 120); if (c && !labels.includes(c)) labels.push(c); }
      if (!labels.length) continue;
      // A tab link only switches the panel it controls (Fivetran's "Application"), so its words do not decide.
      if (e.tagName === "A" && !googleAccounts && e.getAttribute("role") !== "tab" && !/\b(apply|continue|next|review|submit|start|interested|forgot|resend|terms|privacy)\b|sign in with email|\bwithout (?:a )?(?:resume|cv)\b|\bcopy\s*(?:&|and)\s*paste\b|^(?:create\s+(?:(?:an?|your|new)\s+)?(?:[\w.-]+\s*)?(?:account|profile|id)|sign\s*up|register)\b/i.test(labels.join(" "))) continue;
      const form = e.form && formIndex.has(e.form) ? formIndex.get(e.form) : (up(e, (n) => n.tagName === "FORM" && formIndex.has(n)) ? formIndex.get(up(e, (n) => n.tagName === "FORM" && formIndex.has(n))) : -1);
      const r = rectOf(e);
      controls.push({ googleAccount: googleAccounts ? e.getAttribute("data-identifier") || "" : "", i: ref(e), role: e.getAttribute("role") || "", tag: e.tagName.toLowerCase(), labels, type: (e.getAttribute("type") || "").toLowerCase(), disabled: !!e.disabled || e.getAttribute("aria-disabled") === "true", href: e.tagName === "A" ? clip(e.getAttribute("href") || "", 160) : "", target: e.tagName === "A" ? e.getAttribute("target") || "" : "", form, modal: isModal(e), chrome: isChrome(e), chat: isChat(e), x: r.x + r.w / 2, y: r.y + r.h / 2, order: controls.length, pressed: e.getAttribute("aria-pressed") || "", selected: e.getAttribute("aria-selected") || "", aside: !!e.closest("aside"), card: cardOf(e), name: clip(e.getAttribute("name") || "", 60), id: clip(e.id || "", 60) });
    }

    // The Dojo pager's advance control is an empty div that only its two
    // attach points make a control, so no other textless div is ever offered.
    for (const e of els) {
      if (!e.matches(PAGER) || !visible(e) || textOf(e)) continue;
      const r = rectOf(e);
      controls.push({ googleAccount: googleAccounts ? e.getAttribute("data-identifier") || "" : "", i: ref(e), role: "", tag: "div", labels: ["Next"], type: "", disabled: false, href: "", target: "", form: -1, modal: isModal(e), chrome: isChrome(e), chat: isChat(e), x: r.x + r.w / 2, y: r.y + r.h / 2, order: controls.length, pressed: "", aside: false, card: 0 });
    }

    const captcha = [];
    for (const e of els) {
      const src = e.tagName === "IFRAME" ? e.src || "" : "";
      let kind = "";
      if (e.classList && e.classList.contains("g-recaptcha")) kind = "recaptcha_v2";
      else if (e.classList && e.classList.contains("h-captcha")) kind = "hcaptcha";
      else if (e.classList && e.classList.contains("cf-turnstile")) kind = "turnstile_widget";
      else if (/recaptcha\/(api2|enterprise)\/(anchor|bframe)/.test(src)) kind = /enterprise/.test(src) ? "recaptcha_v2_enterprise" : "recaptcha_v2";
      else if (/hcaptcha\.com\/captcha/.test(src)) kind = "hcaptcha";
      else if (/challenges\.cloudflare\.com\/.*turnstile/.test(src) || /challenges\.cloudflare\.com\/cdn-cgi\/challenge-platform/.test(src)) kind = "turnstile_widget";
      else if (/^https?:\/\/(?:[\w-]+\.)*captcha-delivery\.com\//.test(src)) kind = "datadome";
      if (!kind) continue;
      let key = e.getAttribute("data-sitekey") || "";
      if (!key && src) { try { const u = new URL(src); key = u.searchParams.get("k") || u.searchParams.get("sitekey") || (u.hash.match(/sitekey=([^&]+)/) || [])[1] || ""; } catch (x) {} }
      const host = e.tagName === "IFRAME" ? e : e;
      const r = rectOf(host);
      const invisible = e.getAttribute("data-size") === "invisible";
      captcha.push({ kind, siteKey: key, visible: visible(host), invisible, w: r.w, h: r.h, bframeOpen: /bframe/.test(src) && r.w > 100 && r.h > 100 });
    }
    const respFields = els.filter((e) => /^(g-recaptcha-response|h-captcha-response|cf-turnstile-response)/.test(e.getAttribute("name") || "") || e.id === "g-recaptcha-response").map((e) => ({ name: e.getAttribute("name") || e.id, empty: !(e.value || "").length }));

    const text = clip(document.body ? document.body.innerText : "", 20000);
    const lines = (document.body ? document.body.innerText : "").split(/\n+/).map((s) => s.trim()).filter(Boolean).slice(0, 400);
    const step = (text.match(/\bstep\s+(\d+)\s+(?:of|\/)\s+(\d+)/i) || []);
    const stepEl = els.find((e) => e.getAttribute("aria-current") === "step");
    // A wizard's own marker (aria-current true or step, never page): a shown
    // one wins, one hidden only by layout counts when none is shown, and an
    // aria-hidden one never does. Its place among its siblings says whether it
    // is the last step.
    const marker = (() => {
      const cands = els.filter((e) => (e.getAttribute("aria-current") === "true" || e.getAttribute("aria-current") === "step") && !up(e, (n) => n.getAttribute && n.getAttribute("aria-hidden") === "true"));
      const m = cands.find((e) => visible(e)) || cands[0];
      if (!m || !m.parentElement) return null;
      const sibs = Array.from(m.parentElement.children).filter((c) => c.tagName === m.tagName);
      return sibs.length > 1 ? { current: sibs.indexOf(m) + 1, total: sibs.length, marker: clip(m.innerText || m.textContent || "", 60) } : null;
    })();
    // An error summary lists each refused field with the page's own message:
    // Workday's "Errors Found" panel (errorHeading: a button naming the field,
    // the message in the element it is described by) and the error-summary
    // lists of other sites. A message that does not name its field is prefixed
    // with it. These lead the validation words.
    const summary = [];
    for (const box of els.filter((e) => visible(e) && (e.getAttribute("data-automation-id") === "errorHeading" || /(?:^|[-_ ])error-?summary(?:[-_ ]|$)/i.test(typeof e.className === "string" ? e.className : "")))) {
      for (const li of box.querySelectorAll("li")) {
        const btn = li.querySelector("button,a");
        const hintId = btn && btn.getAttribute("aria-describedby");
        const hint = hintId ? document.getElementById(hintId) : null;
        const field = btn ? clip((btn.innerText || btn.textContent || "").replace(/^\s*(?:error|warning)\b[\s\-\u2013\u2014:]*/i, ""), 100) : "";
        const msg = hint ? textOf(hint) : clip(li.innerText || li.textContent || "", 140);
        const line = field && msg && msg !== field && !msg.toLowerCase().includes(field.toLowerCase()) ? field + ": " + msg : msg || field;
        if (line && !summary.includes(line)) summary.push(clip(line, 140));
      }
    }
    // An aria-hidden mark is decoration beside the control it repeats. An alert
    // that announces a success ("resume.pdf successfully uploaded", Workday's
    // live announcer) refuses nothing.
    const success = (e) => e.getAttribute("role") === "alert" && /\bsuccess(?:ful(?:ly)?)?\b/i.test(textOf(e)) && !/\b(?:error|fail(?:ed|ure)?|unable|invalid|required|not|unsuccessful(?:ly)?)\b/i.test(textOf(e));
    const marks = els.filter((e) => visible(e) && e.getAttribute("aria-hidden") !== "true" && !success(e) && (e.getAttribute("role") === "alert" || e.getAttribute("aria-invalid") === "true" || /(^|[-_ ])(error|invalid|field-error|validation)([-_ ]|$)/i.test(typeof e.className === "string" ? e.className : ""))).map((e) => clip(e.getAttribute("aria-invalid") === "true" ? labelOf(e) + " invalid" : textOf(e), 140)).filter(Boolean);
    const validation = summary.concat(marks.filter((m) => !summary.includes(m))).slice(0, 12);
    // What a page says about a refusal it did not mark as one: a short leaf message
    // that reads as a validation sentence and carries a red, danger or error class.
    // It explains a stalled walk and is never read as the page's refusal, so it
    // stays out of `validation`.
    const stall = (() => {
      const sentence = /\b(?:required|please|must|invalid|cannot be (?:blank|empty)|can't be (?:blank|empty)|enter|select|choose|complete|highlighted)\b/i;
      const redClass = /(?:^|[-_ ])(?:text-)?(?:red|danger|error|invalid|warning)(?:[-_ 0-9]|$)/i;
      const messages = els.filter((e) => e.children.length === 0 && visible(e) && sentence.test(textOf(e)) && textOf(e).length >= 4 && textOf(e).length <= 140 && redClass.test(typeof e.className === "string" ? e.className : "")).map((e) => textOf(e)).slice(0, 4);
      return { messages };
    })();
    const loading = els.some((e) => visible(e) && (e.getAttribute("aria-busy") === "true" || /spinner|skeleton|loading-indicator|(?<!up)loader/i.test(typeof e.className === "string" ? e.className : "")));
    // Workday names the active step in its progress bar, and shows a "Loading"
    // skeleton or a loading page while a step renders; the word "Loading"
    // outside the apply flow means nothing.
    const auto = (e, v) => e.getAttribute("data-automation-id") === v;
    const stepBar = els.find((e) => auto(e, "progressBarActiveStep"));
    const activeStep = stepBar ? clip(stepBar.textContent, 120) || null : null;
    const flowPage = els.find((e) => auto(e, "applyFlowPage"));
    // A busy layer over the whole window (ADP's Dojo Standby: a translucent
    // underlay with the wait cursor) covers the fields of the step going away.
    const busyCover = els.some((e) => e.style && e.style.cursor === "wait" && visible(e) && rectOf(e).w >= innerWidth * 0.9 && rectOf(e).h >= innerHeight * 0.9);
    const flowLoading = busyCover || !!flowPage && (els.some((e) => auto(e, "applyFlowLoadingPage") && flowPage.contains(e)) ||
      els.some((e) => flowPage.contains(e) && (e.tagName === "DIV" || e.tagName === "SPAN") && e.children.length === 0 && (e.textContent || "").trim() === "Loading" && e.nextElementSibling && e.nextElementSibling.getAttribute("aria-hidden") === "true") ||
      // A step that drew only its progress bar (three unnamed dots below it) is still loading.
      !!stepBar && !Array.from(flowPage.querySelectorAll("input,select,textarea,button,[role=button]")).some((e) => visible(e) && !auto(e, "backToJobPosting")));
    // Workday's utility bar shows a signed-in candidate the account menu and
    // anyone else a Sign In button.
    const account = els.some((e) => auto(e, "utilityButtonAccountTasksMenu")) ? "signed-in" : els.some((e) => auto(e, "utilityButtonSignIn")) ? "signed-out" : "";
    const frames = els.filter((e) => e.tagName === "IFRAME").map((e) => ({ src: clip(e.src || "", 2000), srcdoc: !!e.getAttribute("srcdoc"), srcdocText: (e.getAttribute("srcdoc") || "").slice(0, 4000), visible: visible(e), w: rectOf(e).w, h: rectOf(e).h }));
    const links = els.filter((e) => e.tagName === "A" && /^https?:/i.test(e.href || "") && visible(e)).slice(0, 400).map((e) => ({ text: textOf(e), href: e.href }));
    // A careers page that embeds a Greenhouse board names it in its script's
    // address; the form frame the script draws can fail to appear (Datadog's
    // consent script), so the walk goes to the form by the board and gh_jid.
    const greenhouseBoard = (() => {
      for (const sc of document.querySelectorAll("script[src]")) {
        try {
          const u = new URL(sc.src);
          if (/^(boards|job-boards)\.greenhouse\.io$/.test(u.hostname) && u.pathname === "/embed/job_board/js") return u.searchParams.get("for") || "";
        } catch (_) {}
      }
      return "";
    })();

    // Oracle Recruiting Cloud's Work and Education History sections are known by
    // their timeline add buttons, not by their headings. An add button opens an
    // inline editor whose footer buttons carry the classes save-btn and
    // cancel-btn; the editor may hide the add buttons while it is open.
    const oracle = (() => {
      const find = (suffix) => els.find((e) => idOf(e).endsWith(suffix) && visible(e));
      const exp = find("timeline-add-experience-button"), edu = find("timeline-add-education-button");
      const footer = (cls) => /\.oraclecloud\.com$/i.test(location.hostname) ? els.find((e) => e.tagName === "BUTTON" && e.classList.contains(cls) && visible(e)) : null;
      const save = footer("save-btn"), cancel = footer("cancel-btn");
      if (!exp && !edu && !save) return null;
      // The timeline cards sit in the section's apply-flow-block; its text is what
      // a committed row shows.
      const items = (btn) => {
        if (!btn) return [];
        const block = up(btn, (n) => n.classList && n.classList.contains("apply-flow-block"));
        if (block) return [clip(block.innerText, 4000)];
        const sec = up(btn, (n) => n.matches && n.matches("section,[role=region],fieldset,.section")) || btn.parentNode;
        return Array.from(sec.querySelectorAll("li,[role=listitem],[class*=timeline-item]")).map((e) => clip(e.innerText, 200)).filter(Boolean);
      };
      return { addExperience: exp ? ref(exp) : -1, addEducation: edu ? ref(edu) : -1, save: save ? ref(save) : -1, cancel: cancel ? ref(cancel) : -1, experience: items(exp), education: items(edu) };
    })();

    // Dialogs and code-shaped forms, for the verification-code gates: ADP's
    // delivery choice and code entry stand in custom elements whose controls
    // are not native inputs, so the page's own field list cannot see them.
    const dialogs = (() => {
      const out = [];
      const DIALOG = "[role=dialog],[role=alertdialog],sdf-focus-pane,cx-verification-otp,[aria-modal=true],form";
      const boxOk = (b) => (b.tagName === "TEXTAREA" || (b.tagName === "INPUT" && !["password", "radio", "checkbox", "hidden", "submit", "button", "file"].includes((b.getAttribute("type") || "text").toLowerCase()))) && visible(b);
      const isOff = (b) => b.hasAttribute("disabled") || b.getAttribute("aria-disabled") === "true" || !!b.disabled;
      for (const c of els) {
        if (out.length >= 12) break;
        if (!c.matches(DIALOG) || blocksNothing(c) || !visible(c)) continue;
        // A modal opened inside another (ADP's privacy pane inside its passcode
        // pane) stands on top: the outer one is not the dialog.
        if (Array.from(c.querySelectorAll(DIALOG)).some((n) => n.tagName !== "FORM" && !blocksNothing(n) && visible(n))) continue;
        const inner = Array.from(walk(c));
        const boxes = inner.filter((e) => (e.tagName === "INPUT" || e.tagName === "TEXTAREA") && boxOk(e));
        const isForm = c.tagName === "FORM";
        if (isForm && (boxes.length < 4 || boxes.length > 8)) continue;
        const text = clip(c.innerText || c.textContent || "", 300);
        const headEl = inner.find((e) => e.matches("[role=heading],h1,h2,h3"));
        const blocked = inner.some((e) => e.tagName === "INPUT" && (e.getAttribute("type") || "").toLowerCase() === "password")
          || boxes.some((b) => /signature|initials|ssn|social security/.test(((b.getAttribute("name") || "") + " " + b.id + " " + labelOf(b)).toLowerCase()))
          || inner.some((e) => e.matches("iframe[src*=recaptcha i],iframe[src*=hcaptcha i],iframe[src*=turnstile i],iframe[src*='challenges.cloudflare' i],.g-recaptcha,.h-captcha,.cf-turnstile,[data-sitekey]"))
          || /captcha|type the characters|verify you are (?:a )?human/i.test(text);
        // The dialog's own close control (dojox's untitled closer, an "×").
        const closer = inner.find((e) => e.matches("[class*=close i],[aria-label*=close i],[title*=close i]") && /^\s*(?:[×✕x]|close)?\s*$/i.test(e.textContent || "") && visible(e));
        out.push({
          heading: clip(c.getAttribute("heading") || (headEl ? headEl.textContent : ""), 120), text, form: isForm, blocked,
          closer: closer ? ref(closer) : null,
          boxes: boxes.map((b) => ({ i: ref(b), label: labelOf(b), maxLength: b.maxLength > 0 && b.maxLength < 1000 ? b.maxLength : 0 })),
          options: inner.filter((e) => e.matches("wfn-radio-button,[role=radio],input[type=radio],[value]") && visible(e)).slice(0, 12).map((e) => ({ i: ref(e), value: clip(e.getAttribute("value") || e.value || "", 60), label: clip(e.textContent || e.getAttribute("aria-label") || "", 60) })),
          buttons: inner.filter((e) => e.matches("button,sdf-button,[role=button],a[href]") && visible(e)).slice(0, 20).map((e) => ({ i: ref(e), label: clip(e.getAttribute("aria-label") || e.textContent || "", 80), disabled: isOff(e) })),
        });
      }
      return out;
    })();

    W[FORMS] = formIndex;
    return {
      oracle, dialogs,
      url: location.href, title: document.title, text, lines,
      headings: els.filter((e) => /^H[1-2]$/.test(e.tagName) && visible(e)).slice(0, 8).map((e) => clip(e.innerText, 140)),
      forms, controls, captcha, respFields, loading, frames, links, greenhouseBoard, activeStep, flowLoading, account,
      step: step[1] ? { current: Number(step[1]), total: Number(step[2]) } : marker ? marker : (stepEl ? { current: 0, total: 0, marker: clip(stepEl.innerText, 60) } : null),
      validation, stall, modal: els.some((e) => e.matches("dialog[open],[role=dialog],[aria-modal=true]") && !blocksNothing(e) && visible(e)),
      scrollH: document.documentElement.scrollHeight,
    };
  }

  // The element a selector of the engine's inspect result names: parts joined
  // by " >>> " step into the open shadow root of the previous match; of
  // several matches the one with a box wins.
  function resolveSelector(sel) {
    let roots = [document];
    const parts = sel.split(" >>> ");
    for (let i = 0; i < parts.length; i++) {
      const found = [];
      for (const r of roots) {
        try { found.push(...Array.from(r.querySelectorAll(parts[i]))); } catch (x) { return null; }
      }
      if (i === parts.length - 1) return found.find((e) => e.getBoundingClientRect().width > 0) || found[0] || null;
      roots = found.map((f) => f.shadowRoot).filter(Boolean);
      if (!roots.length) return null;
    }
    return null;
  }
  // A formless box inside a short block that invites the visitor to join a talent
  // community or get job alerts is the site's furniture, not a question.
  const inviteRE = /talent.?(?:community|network|pool)|job.?alerts?|newsletter|stay in touch|get notified/i;
  function invited(e) {
    const box = up(e, (n) => n.tagName !== "BODY" && n.tagName !== "HTML" && n.nodeType === 1 && clip(n.innerText || "", 500).length > 0 && n.querySelectorAll("input:not([type=hidden]),select,textarea").length <= 3 && inviteRE.test(clip(n.innerText || "", 400)));
    return !!box && !box.querySelector("input[type=file]") && clip(box.innerText || "", 500).length < 400;
  }
  // Handles and page context for the controls the engine named: which form
  // holds each, whether a modal, the page's chrome or a chat widget does, and
  // the attributes the stages read. It never decides what is a field.
  function place(selectors) {
    const formIndex = W[FORMS] || new Map();
    return selectors.map((sel) => {
      const e = resolveSelector(sel);
      if (!e) return { found: false, i: -1, form: -1 };
      const i = handle(e);
      const tag = e.tagName.toLowerCase();
      const attr = (n) => (e.getAttribute && e.getAttribute(n)) || "";
      const type = tag === "input" ? (attr("type") || "text").toLowerCase() : tag === "textarea" || tag === "select" ? tag : "";
      // A contenteditable box has no .form, but it sits in its form all the same (Teamtailor's auto-join box).
      const owner = e.form || (e.closest && e.closest("form"));
      const f = owner && formIndex.has(owner) ? formIndex.get(owner) : -1;
      const max = e.maxLength > 0 && e.maxLength < 1000 ? e.maxLength : 0;
      const filled = type === "checkbox" || type === "radio" ? !!e.checked : (e.value || "").length > 0;
      return {
        found: true, i, tag, type, name: attr("name"), id: idOf(e), autocomplete: attr("autocomplete"), placeholder: clip(attr("placeholder"), 40), role: attr("role"),
        maxLength: max, form: f, modal: isModal(e), chrome: isChrome(e), chat: isChat(e), invite: type !== "file" && invited(e), filled,
        options: tag === "select" ? Array.from(e.options).slice(0, 400).map((o) => clip(o.textContent, 80)) : [],
      };
    });
  }

  // A consent gate: a cookie notice, preference center or privacy-notice dialog
  // that can stand over the page, with the visible buttons of everything that
  // reads as one. Handles are added to the list the last probe made, so a
  // button is found again by `locate`. It never presses anything.
  const consentName = /onetrust|cookiebot|cookie[-_ ]?(?:banner|consent|notice|bar|wall)|cc-window|didomi|truste|consent[-_ ]?manager/i;
  // A container whose class is the bare word (Avature's <div class="cookies">).
  const consentClass = /(?:^|\s)cookies?(?:\s|$)/i;
  const consentWords = /\b(?:privacy\s+(?:policy|notice)|cookies?|consent|personal\s+(?:data|information)|gdpr)\b/i;
  // A notice in a plain fixed overlay over most of the page (no dialog role),
  // which asks nothing and closes with a close button (Channable's scam warning,
  // TikTok's assistant promo with an icon button named "Close guide").
  const closeLabel = /^\s*(?:\u00d7|x|(?:close|dismiss)(?:\s+(?:this\s+)?(?:guide|dialog|modal|pop-?up|window|banner|notice|message|overlay|panel|tour))?)\s*$/i;
  function noticeOverlay(box) {
    if (getComputedStyle(box).position !== "fixed") return false;
    const r = box.getBoundingClientRect();
    if (r.width < innerWidth / 2 || r.height < innerHeight / 2) return false;
    const nodes = Array.from(walk(box));
    if (nodes.some((n) => n.matches("input:not([type=hidden]),select,textarea,[contenteditable=true]"))) return false;
    return nodes.some((n) => n.matches("button,[role=button]") && closeLabel.test(n.innerText || n.textContent || n.getAttribute("aria-label") || ""));
  }
  function consent() {
    const gate = { found: false, text: "", preference: false, allOff: true, asks: false, controls: [] };
    const taken = new Set();
    for (const box of all()) {
      if (!visible(box)) continue;
      const cls = typeof box.className === "string" ? box.className : "";
      const named = consentName.test(idOf(box) + " " + cls) || consentClass.test(cls);
      const dialog = box.matches(MODAL) && (isCookieBanner(box) || consentWords.test(clip(box.textContent, 600)));
      if (!named && !dialog && !noticeOverlay(box)) continue;
      const walked = Array.from(walk(box));
      if (walked.some((n) => n.matches("input[type=password]") || (n.matches("input:not([type=hidden]),textarea") && /signature|initials|ssn|social security/i.test((n.name || "") + " " + (n.id || "") + " " + (n.getAttribute("aria-label") || ""))))) {
        gate.asks = true;
        continue;
      }
      const buttons = walked.filter((n) => n.matches("button,a[href],[role=button],input[type=button],input[type=submit]") && visible(n));
      if (!buttons.length) continue;
      gate.found = true;
      const text = clip(box.textContent, 300);
      if (!gate.text.includes(text)) gate.text += text + " ";
      if (/preference|settings|manage|customi[sz]e/i.test(text)) gate.preference = true;
      for (const n of walked) {
        if (n.matches("input[type=checkbox],[role=switch]") && !n.disabled && (n.checked || n.getAttribute("aria-checked") === "true")) gate.allOff = false;
      }
      for (const b of buttons) {
        if (taken.has(b)) continue;
        taken.add(b);
        const label = clip(b.tagName === "INPUT" ? b.value : (b.innerText || b.textContent || "") || b.getAttribute("aria-label") || "", 60);
        if (!label) continue;
        gate.controls.push({ i: handle(b), label });
      }
    }
    gate.text = clip(gate.text, 300);
    return gate;
  }

  function el(i) {
    const refs = W[KEY] || [];
    const e = refs[i];
    return e && e.isConnected ? e : null;
  }
  function labelsNow(e) {
    const out = [];
    const googleIdentifier = location.protocol === "https:" && location.hostname === "accounts.google.com" ? e.getAttribute("data-identifier") || "" : "";
    for (const l of [e.tagName === "INPUT" ? e.value : ownWords(e), e.getAttribute("aria-label") || "", e.getAttribute("title") || "", googleIdentifier]) { const c = clip(l, 120); if (c && !out.includes(c)) out.push(c); }
    if (!out.length && e.matches(PAGER)) out.push("Next");
    return out;
  }
  // Workday hides its Sign In button from assistive tech (aria-hidden) and lays
  // an empty role=button proxy over it that presses it: a press on the proxy
  // is a press of the button.
  const proxyOf = (n, e) => n !== e && !!n.getAttribute && n.getAttribute("role") === "button" && !textOf(n).trim() &&
    e.getAttribute("aria-hidden") === "true" && !!n.parentElement && n.parentElement.contains(e);
  // A box the page's theme hides, drawn by the element beside it in a wrapper
  // of its own (Taleo Business Edition's icon), is pressed where it is drawn.
  const drawnBox = (e) => {
    if (e.tagName !== "INPUT" || !/^(?:checkbox|radio)$/i.test(e.type) || visible(e)) return null;
    const wrap = e.parentElement;
    if (!wrap || wrap.querySelectorAll("input,select,textarea").length !== 1 || textOf(wrap).trim()) return null;
    return Array.from(wrap.children).find((c) => c !== e && visible(c)) || null;
  };
  function locate(i, still) {
    const e = el(i);
    if (!e) return { found: false };
    const drawn = drawnBox(e);
    const at0 = drawn || e;
    // A page that scrolls smoothly (scroll-behavior: smooth) would still be moving
    // when the point is read; every scroll here is instant.
    if (!still) at0.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
    const r = at0.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    const inView = r.top >= 0 && r.left >= 0 && r.bottom <= innerHeight && r.right <= innerWidth;
    let hit = false, blocker = "";
    const top = document.elementFromPoint(x, y);
    if (top) {
      const path = top.getRootNode && top.getRootNode().host ? [top, top.getRootNode().host] : [top];
      hit = path.some((n) => n === e || e.contains(n) || (n.contains && n.contains(e) && n.tagName === "LABEL") || proxyOf(n, e) || (drawn && (n === drawn || drawn.contains(n))));
      let deep = top;
      while (!hit && deep && deep.shadowRoot) { const inner = deep.shadowRoot.elementFromPoint(x, y); if (!inner || inner === deep) break; hit = inner === e || e.contains(inner); deep = inner; }
      if (!hit) blocker = (top.tagName || "").toLowerCase() + " " + clip(textOf(top), 40);
    }
    const at = hit ? pressedAt(e, top, x, y) : { labels: [], submitter: false, link: false };
    return { found: true, labels: labelsNow(e), x, y, w: r.width, h: r.height, hit, blocker, disabled: !!e.disabled || e.getAttribute("aria-disabled") === "true", tag: e.tagName.toLowerCase(), visible: visible(e), inView,
      atLabels: at.labels, submitter: at.submitter || submits(e), link: at.link || leadsAway(e) };
  }
  // What a press at (x, y) presses besides the control itself: the deepest
  // element there and its pressable ancestors up to the control, or up to the
  // label that holds it; their labels, and whether one of them submits its form
  // or follows a link out of the page.
  const PRESSABLE = "button,a[href],input[type=submit],input[type=button],input[type=image],input[type=reset],summary,[role=button],[role=link],[role=menuitem],[role=tab]";
  const submits = (n) => !!n.form && ((n.tagName === "BUTTON" && (n.getAttribute("type") || "submit").toLowerCase() === "submit") || (n.tagName === "INPUT" && /^(?:submit|image)$/i.test(n.getAttribute("type") || "")));
  const leadsAway = (n) => n.tagName === "A" && n.hasAttribute("href") && !/^(?:#|javascript:|$)/i.test((n.getAttribute("href") || "").trim());
  function pressedAt(e, top, x, y) {
    let deep = top;
    while (deep && deep.shadowRoot) { const inner = deep.shadowRoot.elementFromPoint(x, y); if (!inner || inner === deep) break; deep = inner; }
    const out = { labels: [], submitter: false, link: false };
    for (let n = deep; n && n.nodeType === 1; n = n.parentNode && (n.parentNode.host || n.parentNode)) {
      if (n === e || n.contains(e)) break;
      if (n.matches(PRESSABLE)) for (const l of labelsNow(n)) if (!out.labels.includes(l)) out.labels.push(l);
      out.submitter = out.submitter || submits(n);
      out.link = out.link || leadsAway(n);
    }
    return out;
  }
  // A password is typed only into a password box: the handle must name one.
  function focusSecret(i) {
    const e = el(i);
    if (!e || e.tagName !== "INPUT" || (e.getAttribute("type") || "").toLowerCase() !== "password") return false;
    return focus(i);
  }
  function focus(i) {
    const e = el(i);
    if (!e) return false;
    e.scrollIntoView({ block: "center", behavior: "instant" });
    e.focus();
    if (e.select && e.tagName !== "SELECT") { try { e.select(); } catch (x) {} }
    // A box inside a shadow root is active in its own root; the document names the host.
    return document.activeElement === e || e.getRootNode().activeElement === e || (e.shadowRoot && e.shadowRoot.activeElement != null);
  }
  // The JobPosting nodes of the page's structured data: each ld+json block is a
  // node, an array of them, or a node whose @graph holds them.
  function jobPostings() {
    const out = [];
    for (const s of document.querySelectorAll('script[type="application/ld+json"]')) {
      let v;
      try { v = JSON.parse(s.textContent || ""); } catch (e) { continue; }
      for (const n of [].concat(v, (v && v["@graph"]) || [])) if (n && /JobPosting/.test(n["@type"])) out.push(n);
    }
    return out;
  }
  // A native select takes its option by index, with the input and change events
  // a person's choice raises.
  // The employer the page states: its JobPosting's hiringOrganization, else og:site_name.
  function company() {
    for (const n of jobPostings()) {
      const o = n.hiringOrganization;
      if (o) return clip(typeof o === "string" ? o : o.name || "", 80);
    }
    const m = document.querySelector('meta[property="og:site_name"]');
    return clip((m && m.content) || "", 80);
  }
  // What the page's JobPosting data names its posting: the title, the employer
  // and the place. A page that declares several different postings is a
  // listing, and names none. A page that writes its posting as microdata
  // instead (SmartRecruiters) states its place the same way.
  function posting() {
    const text = (v) => (typeof v === "string" ? clip(v, 240) : "");
    const seen = new Map();
    for (const n of jobPostings()) {
      const o = [].concat(n.hiringOrganization || [])[0];
      const p = { title: text(n.title), company: text(typeof o === "string" ? o : o && o.name), location: jobPlace(n) };
      if (p.title || p.company) seen.set(p.title + "\n" + p.company, p);
    }
    const p = seen.size === 1 ? [...seen.values()][0] : { title: "", company: "", location: "" };
    if (!p.location) p.location = microPlace();
    return p;
  }
  // "Locality, Region, Country" from an address's parts. A part an earlier one
  // already names is not repeated (Workday writes "Poland, Remote" beside the
  // country Poland).
  function placeText(fields) {
    const parts = [];
    for (const f of fields) {
      for (const s of (f || "").split(",")) {
        const t = clip(s, 120);
        if (t && !parts.some((x) => x.toLowerCase() === t.toLowerCase())) parts.push(t);
      }
    }
    return clip(parts.join(", "), 240);
  }
  // The one place every location of a posting shares: the address when they
  // are all one, else their one country. A posting open in several countries
  // is placed in none of them, so no answer about "this country" is read from
  // the first one.
  function onePlace(places) {
    const same = (k) => places.every((p) => p[k].toLowerCase() === places[0][k].toLowerCase());
    if (!places.length) return "";
    if (same("at")) return places[0].at;
    return places[0].country && same("country") ? places[0].country : "";
  }
  // Where a JobPosting puts the job: the place its jobLocations share, else,
  // for a remote posting, the one place its applicants must be in.
  function jobPlace(n) {
    const name = (v) => (typeof v === "string" ? v : (v && typeof v === "object" && typeof v.name === "string" && v.name) || "");
    const places = [];
    for (const loc of [].concat(n.jobLocation || [])) {
      const a = loc && typeof loc === "object" ? loc.address : loc;
      const fields = (a && typeof a === "object" ? [a.addressLocality, a.addressRegion, a.addressCountry] : [a]).map(name);
      const at = placeText(fields);
      if (at) places.push({ at, country: a && typeof a === "object" ? clip(fields[2], 120) : "" });
    }
    if (places.length) return onePlace(places);
    const remote = [].concat(n.jobLocationType || []).includes("TELECOMMUTE");
    const needs = remote ? [].concat(n.applicantLocationRequirements || []).map((r) => clip(name(r), 120)).filter(Boolean) : [];
    const need = onePlace(needs.map((r) => ({ at: r, country: r })));
    return need ? "Remote, " + need : "";
  }
  // The place the jobLocations of the page's one microdata JobPosting share,
  // whose properties a page may write as meta content.
  function microPlace() {
    const items = document.querySelectorAll('[itemscope][itemtype$="schema.org/JobPosting"]');
    if (items.length !== 1) return "";
    const places = [];
    for (const loc of items[0].querySelectorAll('[itemprop~="jobLocation"]')) {
      const prop = (p) => { const e = loc.querySelector('[itemprop~="' + p + '"]'); return e ? e.getAttribute("content") || e.textContent || "" : ""; };
      const at = placeText([prop("addressLocality"), prop("addressRegion"), prop("addressCountry")]);
      if (at) places.push({ at, country: clip(prop("addressCountry"), 120) });
    }
    return onePlace(places);
  }
  function choose(arg) {
    const e = el(arg.i);
    if (!e || e.tagName !== "SELECT" || arg.index < 0 || arg.index >= e.options.length) return false;
    e.selectedIndex = arg.index;
    e.dispatchEvent(new Event("input", { bubbles: true }));
    e.dispatchEvent(new Event("change", { bubbles: true }));
    return e.selectedIndex === arg.index;
  }
  // The iframe whose address is src, and where its content starts in the page, so
  // a point inside the frame can be pressed. The page is scrolled only when the
  // point (dx, dy inside the frame) is not already comfortably in view: a scroll
  // that moves the frame by a fraction of a pixel changes nothing for the
  // person and leaves the browser's input routing a frame behind the layout.
  function frameRect(arg) {
    const shown = Array.from(document.querySelectorAll("iframe")).filter((e) => /^https?:/.test(e.src || "") && visible(e));
    const origin = (u) => { try { return new URL(u).origin; } catch (x) { return ""; } };
    const f = shown.find((e) => e.src === arg.src) || shown.find((e) => origin(e.src) === origin(arg.src)) || (shown.length === 1 ? shown[0] : null);
    if (!f) return null;
    const at = () => { const r = f.getBoundingClientRect(); return { x: r.left + f.clientLeft, y: r.top + f.clientTop }; };
    const inView = () => {
      const a = at(), px = a.x + (arg.dx || 0), py = a.y + (arg.dy || 0);
      return px >= 24 && px <= innerWidth - 24 && py >= 24 && py <= innerHeight - 24;
    };
    if (!inView()) {
      f.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
      if (typeof arg.dy === "number") {
        const r0 = f.getBoundingClientRect();
        window.scrollBy({ top: r0.top + f.clientTop + arg.dy - innerHeight / 2, behavior: "instant" });
      }
    }
    return at();
  }
  // Resolves once the document has painted three frames after this call, or
  // after 600 ms when it never paints (a hidden tab), saying which. A control
  // is pressable when what the browser routes input by has caught up with the
  // layout; a frame that has been presented since the last change has.
  function settle() {
    return new Promise((resolve) => {
      let n = 0;
      const timer = setTimeout(() => resolve(false), 600);
      const tick = () => { if (++n >= 3) { clearTimeout(timer); resolve(true); } else requestAnimationFrame(tick); };
      requestAnimationFrame(tick);
    });
  }
  // Types into a probed box one key at a time (keydown, keypress, input,
  // keyup), the way a framework that listens to keys expects, and reports
  // whether the box holds the text afterwards.
  function typeKeys(i, text) {
    const b = el(i);
    if (!b) return false;
    const proto = b.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, "value").set;
    try { b.focus(); } catch (x) {}
    b.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    setter.call(b, "");
    let typed = "";
    for (const ch of text) {
      const code = /[0-9]/.test(ch) ? "Digit" + ch : "Key" + ch.toUpperCase();
      b.dispatchEvent(new KeyboardEvent("keydown", { key: ch, code, bubbles: true, cancelable: true }));
      b.dispatchEvent(new KeyboardEvent("keypress", { key: ch, code, bubbles: true, cancelable: true }));
      typed += ch;
      setter.call(b, typed);
      b.dispatchEvent(new InputEvent("input", { bubbles: true, data: ch, inputType: "insertText" }));
      b.dispatchEvent(new KeyboardEvent("keyup", { key: ch, code, bubbles: true }));
    }
    b.dispatchEvent(new Event("change", { bubbles: true }));
    return b.value === text;
  }
  // The computer-use rung (owner decision F-9) writes, by trusted input, a
  // control the engine's own writer could not. These find what it presses and
  // register handles for locate; they never press, type or change anything.
  const OPTION_ROLES = "[role=option],[role=menuitem],[role=menuitemradio],[role=menuitemcheckbox],[role=treeitem]";
  const OPTION_LOOSE = "li,[class*=option i]";
  const folded = (s) => clip(s, 300).toLowerCase();
  const register = handle;
  // The name a person reads: aria-labelledby, aria-label, the label of a native
  // control, else the text.
  function nameOf(n) {
    const root = n.getRootNode ? n.getRootNode() : document;
    const byId = (id) => (root.getElementById ? root.getElementById(id) : null) || document.getElementById(id);
    const by = (n.getAttribute("aria-labelledby") || "").split(/\s+/).filter(Boolean).map((id) => { const t = byId(id); return t ? t.textContent : ""; }).join(" ");
    for (const v of [by, n.getAttribute("aria-label") || ""]) if (clip(v, 300)) return clip(v, 300);
    if (n.labels && n.labels.length) return clip(Array.from(n.labels).map((l) => l.innerText || l.textContent).join(" "), 300);
    return clip(n.innerText || n.textContent || "", 300);
  }
  const isChecked = (n) => n.checked === true || /^(?:true|mixed)$/.test(n.getAttribute("aria-checked") || "") || n.getAttribute("aria-pressed") === "true" || n.getAttribute("aria-selected") === "true";
  // The members of a choice: the native boxes of one name in the control's form
  // or root, else the elements of one role inside the group that holds them.
  function membersOf(e) {
    const type = (e.getAttribute("type") || "").toLowerCase();
    if (e.tagName === "INPUT" && (type === "radio" || type === "checkbox") && e.name) {
      const scope = e.form || e.getRootNode();
      const named = Array.from(scope.querySelectorAll("input")).filter((n) => n.type === type && n.name === e.name);
      // A radio named alone inside a radio group (Eightfold's data-use choice) is one of the group's.
      const group = type === "radio" && named.length === 1 ? up(e.parentNode, (n) => n.matches && n.matches("[role=radiogroup]")) : null;
      return group ? Array.from(group.querySelectorAll("input[type=radio]")) : named;
    }
    const group = up(e.parentNode, (n) => n.matches && n.matches("[role=radiogroup],[role=group],fieldset"));
    const role = e.getAttribute("role");
    return group ? Array.from(group.querySelectorAll(role ? "[role=" + role + "]" : e.tagName.toLowerCase())) : [e];
  }
  // The members of the choice a selector names, each with the name a person
  // reads, whether it is chosen and the handle a press uses (a native box the
  // page hides behind its styled label is pressed on the label; a transparent
  // box laid over its option, Eightfold's, is pressed itself: it covers the label).
  function choices(sel) {
    const e = resolveSelector(sel);
    const overlay = (m) => { const r = m.getBoundingClientRect(); return r.width >= 8 && r.height >= 8; };
    const shown = (m) => (m.tagName === "INPUT" && !visible(m) && !overlay(m) && m.labels && m.labels.length ? m.labels[0] : m);
    return e ? membersOf(e).slice(0, 12).map((m) => ({ i: register(shown(m)), name: nameOf(m), checked: isChecked(m) })) : [];
  }
  // What the rung presses to write the control the selector names: the member
  // of a choice whose name is the option (a native box the page hides behind
  // its styled label is pressed on the label), else the control itself, or the
  // box around it when it is too small to press.
  function cuTarget(arg) {
    const e = resolveSelector(arg.sel);
    if (!e) return { found: false };
    const out = { found: true, i: -1, tag: e.tagName.toLowerCase(), type: (e.getAttribute("type") || "").toLowerCase(), multiple: !!e.multiple, checked: false, matched: 0, popup: [], expanded: e.getAttribute("aria-expanded") === "true",
      editable: !e.readOnly && !e.disabled && e.getAttribute("aria-disabled") !== "true" && e.getAttribute("aria-readonly") !== "true" };
    if (arg.kind === "radio-group" || arg.kind === "checkbox") {
      const want = folded(arg.option);
      const hits = arg.option ? membersOf(e).filter((m) => folded(nameOf(m)) === want || (m.tagName === "INPUT" && folded(m.value) === want)) : [e];
      out.matched = hits.length;
      if (hits.length !== 1) return out;
      const m = hits[0];
      out.checked = isChecked(m);
      out.i = register(m.tagName === "INPUT" && !visible(m) && m.labels && m.labels.length ? m.labels[0] : m);
      return out;
    }
    let press = e;
    for (let k = 0; k < 4 && press.parentElement && (rectOf(press).w < 8 || rectOf(press).h < 8); k++) press = press.parentElement;
    out.popup = ["aria-controls", "aria-owns"].flatMap((a) => (e.getAttribute(a) || "").split(/\s+/).filter(Boolean));
    out.i = register(press);
    return out;
  }
  // The visible elements of an open list that match sel and pass keep, among
  // those of the whole document (a popup portalled to the end of the body and
  // open shadow roots included) outside the page's chrome; the innermost of
  // nested ones. When the trigger names a popup this document holds, they are
  // the ones inside it, never another list's.
  function optionNodes(trigger, popup, sel, keep) {
    const pops = (popup || []).map((id) => document.getElementById(id)).filter(Boolean);
    const nodes = pops.length ? pops.flatMap((p) => Array.from(walk(p))) : all();
    const hits = nodes.filter((n) => n.matches(sel) && visible(n) && keep(n) && !(trigger && n.contains(trigger)) && !isChrome(n));
    return hits.filter((n, k) => hits.indexOf(n) === k && !hits.some((m) => m !== n && n.contains(m)));
  }
  // The option of an open list whose name is the chosen label, of an option
  // role, else option-like. Anything but exactly one is no option.
  function cuOption(arg) {
    const want = folded(arg.name);
    const trigger = typeof arg.trigger === "number" ? el(arg.trigger) : null;
    const named = (sel) => optionNodes(trigger, arg.popup, sel, (n) => folded(nameOf(n)) === want);
    let hits = named(OPTION_ROLES);
    if (!hits.length) hits = named(OPTION_LOOSE);
    if (hits.length !== 1) return { found: false, count: hits.length };
    return { found: true, i: register(hits[0]), count: 1 };
  }
  // The option names a list shows, each once and at most 100: a native
  // select's own, else those of the option-role elements of the open list,
  // inside the popup the control names now (a list names it only once open).
  // focused tells that the focus is in the control or that popup, where a key
  // reaches the list; never on a native select, which the rung does not press.
  function cuOptions(arg) {
    const e = arg.sel ? resolveSelector(arg.sel) : null;
    const once = (names) => names.filter((n, k) => n && names.indexOf(n) === k).slice(0, 100);
    if (e && e.tagName === "SELECT") return { options: once(Array.from(e.options).filter((o) => o.value !== "" && !o.disabled).map((o) => clip(o.textContent, 300))), popup: [], focused: false };
    const popup = e ? ["aria-controls", "aria-owns"].flatMap((a) => (e.getAttribute(a) || "").split(/\s+/).filter(Boolean)) : arg.popup || [];
    const active = e ? e.getRootNode().activeElement : null;
    const holds = (n) => !!n && !!active && n.contains(active);
    return { options: once(optionNodes(e, popup, OPTION_ROLES, () => true).map(nameOf)), popup, focused: holds(e) || popup.some((id) => holds(document.getElementById(id))) };
  }
  // The label of a native select's selected option.
  function cuSelected(i) {
    const e = el(i);
    return e && e.tagName === "SELECT" && e.selectedIndex >= 0 ? clip(e.options[e.selectedIndex].textContent, 300) : "";
  }
  function exists(i) { return !!el(i); }
  function valueLength(i) { const e = el(i); return e ? (e.value || "").length : -1; }

  // Token delivery: the token goes into every response field of the widget's
  // kind, a field is created in the widget's form when the page has none, and
  // a data-callback global (or a client callback the page exposes) is called
  // when one can be found. A closure-only callback is never guessed at.
  const FIELD = {
    recaptcha_v2: ["g-recaptcha-response"], recaptcha_v2_enterprise: ["g-recaptcha-response"],
    hcaptcha: ["h-captcha-response", "g-recaptcha-response"], turnstile_widget: ["cf-turnstile-response"],
  };
  const WIDGET = {
    recaptcha_v2: ".g-recaptcha", recaptcha_v2_enterprise: ".g-recaptcha", hcaptcha: ".h-captcha", turnstile_widget: ".cf-turnstile",
  };
  // A field belongs to the widget of its own form: a field whose form holds a
  // widget of another kind is that widget's (hCaptcha also writes the
  // g-recaptcha-response alias).
  function foreign(e, kind) {
    const form = up(e, (n) => n.tagName === "FORM");
    if (!form) return false;
    return Object.keys(WIDGET).some((k) => WIDGET[k] !== WIDGET[kind] && form.querySelector(WIDGET[k]));
  }
  function responseFields(kind) {
    const names = FIELD[kind] || [];
    return all().filter((e) => (e.tagName === "TEXTAREA" || e.tagName === "INPUT") && (names.includes(e.getAttribute("name")) || (kind.startsWith("recaptcha") && e.id === "g-recaptcha-response")) && !foreign(e, kind));
  }
  function setValue(e, v) {
    const proto = e.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value").set.call(e, v);
    e.dispatchEvent(new Event("input", { bubbles: true }));
    e.dispatchEvent(new Event("change", { bubbles: true }));
  }
  function deliver(kind, token) {
    const out = { fields: 0, created: false, callback: "" };
    let fields = responseFields(kind);
    const widget = all().find((e) => e.matches(WIDGET[kind] || "#none") && visible(e));
    if (!fields.length && widget) {
      const form = up(widget, (n) => n.tagName === "FORM");
      const host = form || widget.parentNode;
      const f = document.createElement("textarea");
      f.name = (FIELD[kind] || ["g-recaptcha-response"])[0];
      f.style.display = "none";
      host.appendChild(f);
      fields = [f];
      out.created = true;
    }
    for (const f of fields) { setValue(f, token); out.fields++; }
    const cb = widget && widget.getAttribute("data-callback");
    if (cb && typeof W[cb] === "function") { try { W[cb](token); out.callback = cb; } catch (x) {} }
    // A widget rendered from script names no data-callback: its callback is in
    // reCAPTCHA's own client config, found there and nowhere else.
    const cfg = !out.callback && String(kind).indexOf("recaptcha") === 0 && W.___grecaptcha_cfg;
    if (cfg && cfg.clients) {
      const seen = new Set();
      const look = (o, depth) => {
        if (!o || typeof o !== "object" || depth > 5 || seen.has(o) || (typeof Node === "function" && o instanceof Node)) return;
        seen.add(o);
        for (const k of Object.keys(o)) {
          let v;
          try { v = o[k]; } catch (x) { continue; }
          if (k === "callback") {
            const fn = typeof v === "function" ? v : typeof v === "string" && typeof W[v] === "function" ? W[v] : null;
            if (fn && !out.callback) { try { fn.call(W, token); out.callback = "___grecaptcha_cfg"; } catch (x) {} }
          } else if (v && typeof v === "object") look(v, depth + 1);
        }
      };
      look(cfg.clients, 0);
    }
    return out;
  }
  function blank(kind) {
    let n = 0;
    for (const f of responseFields(kind)) { setValue(f, ""); n++; }
    return n;
  }


  // One bounded re-validation of a FormValidation (or BootstrapValidator) form
  // whose Submit stays disabled after a complete fill. Its button state is set
  // only when a field's status is updated, so a refusal recorded while a
  // follow-up row was open keeps it disabled after the row closes. This switches
  // off the validators of a refused field no person can see, asks again about
  // every field a person can see, and reports the names still refused. "Visible"
  // is computed layout, as the library itself reads it, never markup. It never
  // calls the form-level validate (which can submit), never removes `disabled`
  // and never fills a field.
  function revalidate() {
    const $ = W.jQuery;
    if (typeof $ !== "function") return null;
    const shown = (e) => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
    const out = { hiddenReleased: [], revalidated: [], invalid: [] };
    let found = false;
    for (const form of Array.from(document.querySelectorAll("form"))) {
      let fv = null;
      try { fv = $(form).data("formValidation") || $(form).data("bootstrapValidator") || null; } catch (x) { fv = null; }
      if (!fv || !fv.options || !fv.options.fields || typeof fv.revalidateField !== "function" || typeof fv.isValidField !== "function") continue;
      found = true;
      const names = Object.keys(fv.options.fields);
      const off = (n) => fv.options.fields[n].enabled === false || fv.options.fields[n].enabled === "false";
      const elementsOf = (n) => { try { return Array.prototype.slice.call(fv.getFieldElements(n) || []); } catch (x) { return []; } };
      for (const n of names) {
        if (off(n)) continue;
        const els2 = elementsOf(n);
        if (els2.length === 0 || els2.some(shown)) continue;
        let refused = false;
        try { refused = fv.isValidField(n) === false; } catch (x) {}
        if (!refused || typeof fv.enableFieldValidators !== "function") continue;
        try { fv.enableFieldValidators(n, false); out.hiddenReleased.push(n); } catch (x) {}
      }
      for (const n of names) {
        if (off(n)) continue;
        if (!elementsOf(n).some(shown)) continue;
        try { fv.revalidateField(n); out.revalidated.push(n); } catch (x) {}
      }
      for (const n of names) {
        try { if (fv.isValidField(n) === false) out.invalid.push(n); } catch (x) {}
      }
    }
    return found ? out : null;
  }

  // The last look before the final press, two read-only scans. noResponse is
  // Workday's Review recap: a leaf that reads exactly "No Response" under a
  // required question (its "*") or the Work Experience or Education section
  // is an answer Workday does not hold. toggles are required Yes/No groups that
  // show no answer: Ashby's "_yesno_" buttons, a hidden tabindex=-1 proxy
  // input beside two option buttons (never a file input: Ashby's résumé slot
  // has the same shape), and native radio groups with nothing checked.
  function prepress() {
    const seen = (e) => { const r = e.getBoundingClientRect(); if (r.width === 0 || r.height === 0) return false; const cs = getComputedStyle(e); return cs.visibility !== "hidden" && cs.display !== "none"; };
    const noResponse = [];
    const keys = new Set();
    for (const el of document.querySelectorAll("span, div, p, li, dd")) {
      if (el.children.length > 0 || clip(el.textContent, 40) !== "No Response" || !seen(el)) continue;
      let label = "";
      const group = el.closest('[role="group"]');
      if (group) {
        const by = group.getAttribute("aria-labelledby");
        const h = by && document.getElementById(by);
        if (h) label = (h.textContent || "").trim();
        if (!label) { const hh = group.querySelector("h1, h2, h3, h4, h5"); if (hh) label = (hh.textContent || "").trim(); }
      }
      if (!label && el.previousElementSibling) label = clip(el.previousElementSibling.textContent, 200);
      if (!/\*\s*$/.test(label) && !/^(work experience|education)\b/i.test(label)) continue;
      if (keys.has(label)) continue;
      keys.add(label);
      noResponse.push(label);
    }
    const selectedButton = (b) => {
      if (b.getAttribute("aria-pressed") === "true" || b.getAttribute("aria-checked") === "true") return true;
      const cls = typeof b.className === "string" ? b.className : "";
      return /(^|[^a-zA-Z])(selected|active|chosen)/i.test(cls) || cls.split(/\s+/).some((t) => /^(?:border|bg)-accent\b/i.test(t));
    };
    const requiredNear = (el) => {
      const scope = el.closest('[data-field-path], [data-field-entry-id], fieldset, [role="group"]') || el.parentElement;
      if (!scope) return false;
      if (scope.querySelector('[class*="_required_"]')) return true;
      for (const l of scope.querySelectorAll("label, legend")) {
        if (/(^|\s)_?required(?:_[A-Za-z0-9]+)*(\s|$)/.test(typeof l.className === "string" ? l.className : "")) return true;
        if (/[*\uFF0A]\s*$/.test((l.textContent || "").trim())) return true;
      }
      return false;
    };
    const wrappers = new Set(document.querySelectorAll('[class*="_yesno_"]'));
    for (const input of document.querySelectorAll('input[tabindex="-1"]')) {
      const t = (input.type || "").toLowerCase();
      if (t === "checkbox" || t === "radio" || t === "hidden" || t === "file") continue;
      if (input.parentElement) wrappers.add(input.parentElement);
    }
    const toggles = [];
    for (const w of wrappers) {
      if (!seen(w)) continue;
      const buttons = Array.from(w.querySelectorAll("button")).filter(seen);
      if (buttons.length < 2 || !requiredNear(w)) continue;
      const box = w.querySelector('input[type="checkbox"]');
      if (buttons.some(selectedButton) || (box && box.checked)) continue;
      const entry = w.closest("[data-field-path], [data-field-entry-id]");
      const l = entry && entry.querySelector("label");
      toggles.push(clip((l && l.textContent) || (w.previousElementSibling && w.previousElementSibling.textContent), 160) || "an unlabeled required yes/no question");
    }
    const names = new Set();
    for (const radio of document.querySelectorAll('input[type="radio"][name]')) {
      if (names.has(radio.name)) continue;
      names.add(radio.name);
      const group = Array.from(document.querySelectorAll('input[type="radio"][name="' + CSS.escape(radio.name) + '"]'));
      if (!group.some(seen) || group.some((r) => r.checked)) continue;
      const scope = group[0].closest('fieldset, [role="group"], [data-field-path], [data-field-entry-id]');
      if (!scope || !requiredNear(group[0])) continue;
      const title = scope.querySelector("label, legend");
      toggles.push(clip(title && title.textContent, 160) || "an unlabeled required radio group");
    }
    return { noResponse, toggles };
  }

  switch (cmd) {
    case "prepress": return prepress();
    case "revalidate": return revalidate();
    case "settle": return settle();
    case "deliver": return deliver(arg.kind, arg.token);
    case "blank": return blank(arg);
    case "probe": return probe();
    case "consent": return consent();
    case "locate": return locate(arg, false);
    case "locateStill": return locate(arg, true);
    case "focus": return focus(arg);
    case "focusSecret": return focusSecret(arg);
    case "choose": return choose(arg);
    case "frameRect": return frameRect(arg);
    case "typekeys": return typeKeys(arg.i, arg.text);
    case "cuTarget": return cuTarget(arg);
    case "cuOption": return cuOption(arg);
    case "cuOptions": return cuOptions(arg);
    case "cuSelected": return cuSelected(arg);
    case "choices": return choices(arg);
    case "exists": return exists(arg);
    case "valueLength": return valueLength(arg);
    case "place": return place(arg);
    case "company": return company();
    case "posting": return posting();
    case "activeStep": { const b = document.querySelector('[data-automation-id="progressBarActiveStep"]'); return b ? clip(b.textContent, 120) : ""; }
    case "signature": { const p = probe(); return p.url + "|" + p.controls.map((c) => c.labels[0]).join(",") + "|" + p.text.length; }
  }
  return null;
})
