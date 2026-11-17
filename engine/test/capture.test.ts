// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Bindings to Go (posting context from the page), Job-site and application-form detection
// (embedded overview, wizard advance).
// Requirement: Posting context derivation (QST-86): which job a page belongs to and where its description comes from.
import { describe, expect, test } from "vitest";
import { pageDescription, withPagePosting, anchorFrom, jobPostingOf, MIN_ANCHOR_JD_CHARS, postingLocation, readJobDescription, resolveJobContext, type JobContext, type PageReading } from "../src/capture";
import { stepAdvanced } from "../src/detect";
import { revealControl } from "../src/reveal";
import type { Win } from "../src/dom";
import { fakeHost, makeEngine, mount, runFill, setUrl, tick } from "./helpers";

const win = window as unknown as Win;
const LONG = Array.from({ length: 8 }, (_, i) => `We build small reliable tools number ${i} for people who apply to jobs.`).join(" ") + " You will own the Go services.";

function ld(extra: Record<string, unknown>): string {
  return `<script type="application/ld+json">${JSON.stringify({ "@context": "https://schema.org", "@type": "JobPosting", ...extra })}</script>`;
}

describe("Page capture", () => {
  test("jdText is the longer of the stripped page text and the structured description", () => {
    const structured = "Structured description. ".repeat(40);
    mount(`${ld({ title: "Engineer", description: `<p>${structured}</p>` })}<main>Short intro.</main>`);
    expect(pageDescription(document, win)).toBe(structured.trim());
    mount(`${ld({ description: "<p>Tiny.</p>" })}<main>${LONG}</main>`);
    expect(pageDescription(document, win)).toContain("own the Go services");
  });

  test("Forms, navigation and scripts are left out of the page text and repeated sentences are kept once", () => {
    mount(`<nav>Home Pricing</nav><script>var secret = 1;</script><main>${LONG} ${LONG}</main><form><label>First name</label><input></form>`);
    const jd = pageDescription(document, win);
    expect(jd).not.toContain("Pricing");
    expect(jd).not.toContain("secret");
    expect(jd).not.toContain("First name");
    expect(jd.split("number 3").length - 1).toBe(1);
  });

  test("An escaped description is read as text and never loads anything", () => {
    const html = Array.from({ length: 8 }, (_, i) => `&lt;p&gt;Own the Go services, part ${i}.&lt;/p&gt;&lt;img src=x onerror=alert(1)&gt;`).join("");
    mount(`${ld({ description: html })}`);
    const jd = pageDescription(document, win);
    expect(jd).toContain("Own the Go services, part 3.");
    expect(jd).not.toContain("<");
  });

  test("The page's own location, company and title fill only what the caller left empty", () => {
    mount(
      ld({
        title: "Staff Engineer",
        hiringOrganization: { name: "Example Corp" },
        jobLocation: { address: { addressLocality: "Durham", addressRegion: "NC", addressCountry: "US" } },
        description: LONG,
      }),
    );
    expect(withPagePosting({}, document, win)).toMatchObject({ company: "Example Corp", title: "Staff Engineer", location: "Durham, NC, US" });
    expect(withPagePosting({ company: "Given Inc", location: "Remote" }, document, win)).toMatchObject({ company: "Given Inc", location: "Remote", title: "Staff Engineer" });
  });

  test("The match request carries the page's posting when the caller gave none", async () => {
    mount(`${ld({ title: "Engineer", description: LONG, jobLocation: { address: { addressLocality: "Durham", addressCountry: { name: "United States" } } } })}
      <label for="a">First name</label><input id="a"><label for="b">Last name</label><input id="b"><label for="y">Years of experience with Go</label><input id="y">`);
    const host = fakeHost();
    await runFill(makeEngine(host), { posting: { company: "Example Corp" } });
    const req = host.matchRequests[0];
    expect(req?.posting).toMatchObject({ company: "Example Corp", title: "Engineer", location: "Durham, United States" });
    expect(req?.posting.jdText).toContain("own the Go services");
  });
});

describe("Embedded overview", () => {
  const overview = `<div role="tablist"><a role="tab" aria-selected="true" id="t1">Overview</a><a role="tab" aria-selected="false" id="t2" href="#application">Application</a></div>
    <main id="body">Example Corp is hiring.</main>`;

  function wire(): void {
    document.getElementById("t2")!.addEventListener("click", () => {
      setTimeout(() => {
        document.getElementById("body")!.innerHTML = `<label for="fn">First name</label><input id="fn" name="fn"><label for="em">Email</label><input id="em" type="email">`;
      }, 20);
    });
  }

  test("Embedded overview: an Ashby frame on its overview tab gets the Application tab activated and the fill proceeds", async () => {
    setUrl("https://jobs.ashbyhq.com/example/abc");
    mount(overview);
    wire();
    const s = (await runFill(makeEngine(fakeHost()), { applicant: { candidate: { firstName: "Alex", email: "alex@example.com" } } })) as { summary: { rows: { value?: string; label: string }[]; site?: string } };
    expect(s.summary.site).toBe("ashby");
    expect(s.summary.rows.find((r) => r.label === "First name")?.value).toBe("Alex");
    expect((document.getElementById("em") as HTMLInputElement).value).toBe("alex@example.com");
  });

  test("A tab leaving the frame is not a reveal", () => {
    mount(`<a role="tab" aria-selected="false" href="https://other.example.org/x">Application</a>`);
    expect(revealControl(document)).toBeNull();
  });

  test("A page that is not an embedded system is not given the reveal press or the wait", async () => {
    setUrl("https://example.com/about");
    mount(`<button id="go">Apply now</button>`);
    let pressed = 0;
    document.getElementById("go")!.addEventListener("click", () => pressed++);
    const s = await runFill(makeEngine(fakeHost()));
    await tick(20);
    expect(pressed).toBe(0);
    expect(s.state).toBe("done");
  });
});

describe("Wizard advance", () => {
  test("A step counts as advanced when its controls no longer resolve, with the URL unchanged", () => {
    mount(`<div id="step1"><label for="a">First name</label><input id="a"></div>`);
    expect(stepAdvanced(document, ["#a"])).toBe(false);
    document.getElementById("step1")!.style.display = "none";
    expect(stepAdvanced(document, ["#a"])).toBe(true);
    mount(`<div id="step2"><input id="b"></div>`);
    expect(stepAdvanced(document, ["#a"])).toBe(true);
    expect(stepAdvanced(document, [])).toBe(false);
  });

  test("A changed URL with the same controls is not an advance", () => {
    mount(`<input id="a">`);
    history.pushState({}, "", "/step-2");
    expect(stepAdvanced(document, ["#a"])).toBe(false);
  });
});

const JD = "We are hiring a backend engineer to build payment services. ".repeat(6);
const FORM_TEXT = "First name Last name Email Phone Upload your resume Submit application ".repeat(4);

const posting = (over: Partial<PageReading> = {}): PageReading => ({
  url: "https://jobs.example.test/acme/123",
  company: "Acme",
  role: "Backend Engineer",
  location: "Sampleton, ES",
  jdText: JD,
  jdSource: "rendered",
  posting: true,
  form: false,
  ...over,
});
const form = (over: Partial<PageReading> = {}): PageReading => ({
  url: "https://jobs.example.test/acme/123/application",
  jdText: "",
  posting: true,
  form: true,
  ...over,
});
const anchored: JobContext = { url: "https://jobs.example.test/acme/123", jdText: JD.trim(), company: "Acme", title: "Backend Engineer", location: "Sampleton, ES", source: "anchor" };

describe("job context precedence", () => {
  test("form page uses the anchored JD, never the form text", () => {
    // Rendered form text has no description source: the anchor wins and the text is never the JD.
    const c = resolveJobContext({ page: form({ jdText: FORM_TEXT, jdSource: "rendered" }), anchor: anchored });
    expect(c).toMatchObject({ source: "anchor", jdText: JD.trim(), url: anchored.url, company: "Acme", location: "Sampleton, ES" });
    // With no anchor the rendered form text is still not a description.
    const none = resolveJobContext({ page: form({ jdText: FORM_TEXT, jdSource: "rendered" }) });
    expect(none.source).toBe("none");
    expect(none.jdText).toBeUndefined();
    // An anchor read from the board keeps saying so.
    expect(resolveJobContext({ page: form(), anchor: { ...anchored, source: "board" } }).source).toBe("board");
  });

  test("the manual pin beats the page, the anchor and the last autofill", () => {
    const pinned: JobContext = { url: "https://other.example.test/9", jdText: "Pinned description", source: "page" };
    const c = resolveJobContext({ pinned, page: posting(), anchor: anchored, lastAutofill: anchored });
    expect(c).toMatchObject({ source: "pinned", url: pinned.url, jdText: "Pinned description" });
  });

  test("a posting page uses its own live description over an older anchor", () => {
    const c = resolveJobContext({ page: posting({ url: "https://jobs.example.test/acme/456" }), anchor: anchored });
    expect(c).toMatchObject({ source: "page", url: "https://jobs.example.test/acme/456", jdText: JD.trim(), title: "Backend Engineer" });
  });

  test("a form page opened directly takes a structured or container description, never rendered text", () => {
    expect(resolveJobContext({ page: form({ jdText: JD, jdSource: "structured" }) })).toMatchObject({ source: "page", jdText: JD.trim() });
    expect(resolveJobContext({ page: form({ jdText: JD, jdSource: "container" }) })).toMatchObject({ source: "page", jdText: JD.trim() });
    expect(resolveJobContext({ page: form({ jdText: JD, jdSource: "rendered" }) }).jdText).toBeUndefined();
  });

  test("the anchor beats a structured description on the form page", () => {
    const c = resolveJobContext({ page: form({ jdText: "A short structured blurb", jdSource: "structured" }), anchor: anchored });
    expect(c.source).toBe("anchor");
  });

  test("the last autofill context answers when the page and the anchor give nothing", () => {
    const last: JobContext = { url: "https://jobs.example.test/acme/123", jdText: JD.trim(), company: "Acme", source: "page" };
    expect(resolveJobContext({ page: form(), lastAutofill: last })).toMatchObject({ source: "autofill", company: "Acme" });
    expect(resolveJobContext({ page: form() })).toMatchObject({ source: "none", url: form().url });
  });
});

describe("anchor creation", () => {
  test("only a posting page with at least 200 characters of description makes an anchor", () => {
    expect(anchorFrom(posting())).toMatchObject({ url: posting().url, jdText: JD.trim(), company: "Acme", title: "Backend Engineer", location: "Sampleton, ES" });
    expect(anchorFrom(posting({ jdText: "x".repeat(MIN_ANCHOR_JD_CHARS - 1) }))).toBeNull();
    expect(anchorFrom(posting({ posting: false }))).toBeNull();
  });

  test("an application form never makes an anchor, even on a job site", () => {
    expect(anchorFrom(form({ jdText: FORM_TEXT, jdSource: "rendered" }))).toBeNull();
    expect(anchorFrom(form({ jdText: JD, jdSource: "structured" }))).toBeNull();
  });
});

describe("reading the page", () => {
  const ld = (o: unknown) => `<script type="application/ld+json">${JSON.stringify(o)}</script>`;

  test("the posting's own location, Remote for a telecommute posting", () => {
    mount(ld({ "@type": "JobPosting", title: "X", jobLocation: { address: { addressLocality: "Sampleton", addressRegion: "ES", addressCountry: { name: "United States" } } } }));
    expect(postingLocation(jobPostingOf(document))).toBe("Sampleton, ES, United States");
    mount(ld({ "@graph": [{ "@type": "JobPosting", jobLocationType: "TELECOMMUTE", jobLocation: { address: { addressLocality: "Sampleton" } } }] }));
    expect(postingLocation(jobPostingOf(document))).toBe("Remote");
  });

  test("The fill's posting context and the page reading name one posting and one location", () => {
    const remote = { "@type": "jobposting", title: "Remote one", description: LONG, jobLocationType: "TELECOMMUTE", applicantLocationRequirements: { "@type": "Country", name: "United States" } };
    mount(ld([{ "@type": "BreadcrumbList" }, { "@type": "JobPosting", title: "Short", description: "x", jobLocation: { address: { addressLocality: "Sampleton" } } }, remote]));
    const read = jobPostingOf(document);
    expect(read?.title).toBe("Remote one");
    expect(postingLocation(read)).toBe("Remote, United States");
    expect(withPagePosting({}, document, win)).toMatchObject({ title: "Remote one", location: "Remote, United States" });
  });

  test("structured data, then a known container; rendered text only off a form page", () => {
    mount(`<main><p>Rendered page text</p></main>${ld({ "@type": "JobPosting", description: "<p>Build <b>payments</b></p>" })}`);
    expect(readJobDescription(document, true)).toEqual({ text: "Build payments", source: "structured" });
    mount(`<main><div class="job__description">Container description</div><form>x</form></main>`);
    expect(readJobDescription(document, true)).toEqual({ text: "Container description", source: "container" });
    mount(`<main><label for="e">Email</label><input id="e" type="email"></main>`);
    expect(readJobDescription(document, true)).toEqual({ text: "" });
    expect(readJobDescription(document, false).source).toBe("rendered");
  });
});
