// LinkedIn page reads for the Easy Apply entry. A function expression called
// with a command; it returns JSON-compatible data and never writes to the page.
(function (cmd) {
  const norm = (v) => String(v || "").replace(/\s+/g, " ").trim();

  // The posting's own visible text, so a phrase in a recommended-jobs card
  // cannot claim a match meant for the posting.
  function mainText() {
    const el = document.querySelector("main") || document.body;
    return (el && el.innerText ? el.innerText : "").slice(0, 4000);
  }

  // LinkedIn's "Page not found" page prints one heading per locale and shows
  // the viewer's, so the English heading is in the document whatever the
  // locale. The server-driven layout's page says "This page doesn't exist"
  // in an h2.
  function notFound() {
    return Array.from(document.querySelectorAll("h1, h2")).some((h) => /^\s*(?:page not found|this page doesn[\u2019']t exist)\s*$/i.test(h.textContent || ""));
  }

  const shown = (el) => el.getClientRects().length > 0;
  const dialogs = () => Array.from(document.querySelectorAll('div[role="dialog"], dialog[open]')).filter(shown);

  // The classic modal is a div[role=dialog]; the server-driven layout opens a
  // native <dialog> headed "Apply to <company>". Only a modal on screen counts.
  function modalOpen() {
    if (Array.from(document.querySelectorAll(".jobs-easy-apply-modal")).some(shown)) return true;
    return dialogs().some((d) => {
      const text = norm(d.textContent).toLowerCase();
      return text.startsWith("apply to ") || text.includes("easy apply") || text.includes("application powered by") || text.includes("submit application") || text.includes("review your application");
    });
  }

  // LinkedIn's "Job search safety reminder" can stand between Easy Apply and
  // its modal; its "Continue applying", a button or a link, goes on to the
  // application.
  function safetyReminder() {
    return dialogs().some((d) => /job search safety reminder/i.test(norm(d.textContent)) &&
      Array.from(d.querySelectorAll("button, a[href]")).some((b) => /^continue applying$/i.test(norm(b.innerText || b.textContent))));
  }

  // The destination of an off-site Apply is in the page's embedded
  // job-posting payload, read for THIS posting's id only: a similar-job card
  // on the same page must never supply it.
  function companyApplyUrl() {
    const id = (/\/jobs\/view\/(?:[^/?#]*-)?(\d+)/.exec(location.pathname) || [])[1];
    if (!id) return "";
    const urn = "urn:li:fsd_jobPosting:" + id;
    for (const block of document.querySelectorAll("code")) {
      const text = block.textContent || "";
      if (text.indexOf("companyApplyUrl") < 0 || text.indexOf(urn) < 0) continue;
      let payload;
      try { payload = JSON.parse(text); } catch (e) { continue; }
      for (const item of (payload && payload.included) || []) {
        if (item && item.entityUrn === urn && typeof item.companyApplyUrl === "string") return item.companyApplyUrl;
      }
    }
    return "";
  }

  function affordance() {
    const scope = document.querySelector("main") || document.body;
    if (!scope) return null;
    for (const el of scope.querySelectorAll("button, a[href]")) {
      const label = norm(el.innerText || el.textContent);
      if (!label || label.length > 60 || !/apply/i.test(label)) continue;
      const href = el.getAttribute("href") || companyApplyUrl();
      let offsite = false;
      if (href) {
        try { offsite = new URL(href, location.href).hostname !== location.hostname; } catch (e) { offsite = false; }
      }
      // The signed-out guest page tracks its Apply for an ON-LinkedIn
      // application as "public_jobs_apply-link-onsite".
      const signedOut = /^public_jobs_apply-link-onsite/.test(el.getAttribute("data-tracking-control-name") || "");
      return { label, href, offsite, signedOut };
    }
    return null;
  }

  switch (cmd) {
    case "mainText": return mainText();
    case "notFound": return notFound();
    case "modal": return modalOpen();
    case "safetyReminder": return safetyReminder();
    case "affordance": return affordance();
  }
  return null;
})
