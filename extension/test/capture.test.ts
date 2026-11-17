import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { capturePage, readPosting } from "../src/shared/capture";
import { isApplicationPage } from "../src/shared/site";

const POSTING = `Backend Engineer. Responsibilities: build and run services. Qualifications: five years of Go. Salary range and benefits apply. Full-time, remote. ${"Details. ".repeat(60)}`;

function page(url: string, title: string, bodyHtml: string, head = "") {
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL(url);
  document.head.innerHTML = head;
  document.title = title;
  document.body.innerHTML = bodyHtml;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("capturing the page", () => {
  it("collects the address, title and main text and labels a posting", async () => {
    page("https://example.com/careers/backend", "Backend Engineer - Example Corp", `<main>${POSTING} Apply now to join.</main>`);
    const c = await capturePage();
    expect(c.url).toBe("https://example.com/careers/backend");
    expect(c.title).toBe("Backend Engineer - Example Corp");
    expect(c.text).toContain("Responsibilities");
    expect(c.detection.label).toBe("job_posting");
    expect(c.detection.signals).toContain("responsibilities");
    expect(c.company).toBe("Example Corp");
    expect(c.role).toBe("Backend Engineer");
  });

  it("reads structured job-posting data, including inside a graph", async () => {
    const ld = JSON.stringify({ "@graph": [{ "@type": "WebSite" }, { "@type": "JobPosting", title: "Staff Engineer", hiringOrganization: { name: "Example Corp" }, jobLocation: { address: { addressLocality: "Springfield", addressRegion: "IL" } }, description: "<p>Build things.</p>" }] });
    page("https://example.com/jobs/1", "Jobs", `<main>${POSTING}</main>`, `<script type="application/ld+json">${ld}</script>`);
    const c = await capturePage();
    expect(c).toMatchObject({ structured: true, role: "Staff Engineer", company: "Example Corp", location: "Springfield, IL" });
    expect(c.detection.signals).toContain("structured job posting data");
  });

  it("turns a structured description into text in an inert document: paragraphs stay apart and no image is loaded", async () => {
    const ld = JSON.stringify({ "@type": "JobPosting", title: "Staff Engineer", description: `<p>Build things.</p><p>Run them.</p><img src="https://tracker.example.com/pixel.gif">${"<p>More detail.</p>".repeat(30)}` });
    page("https://example.com/jobs/1", "Jobs", "<main>Loading</main>", `<script type="application/ld+json">${ld}</script>`);
    const made = vi.spyOn(document, "createElement");
    const c = capturePage();
    await vi.advanceTimersByTimeAsync(6000);
    expect((await c).text).toMatch(/^Build things\. Run them\. More detail\./);
    expect(made.mock.calls.map((a) => a[0])).not.toContain("div");
    made.mockRestore();
  });

  it("calls a page an application form by the same rule as the submission recorder, shadow roots included", () => {
    page("https://example.com/careers/1", "Apply", "<main><div id='host'></div></main>");
    document.getElementById("host")!.attachShadow({ mode: "open" }).innerHTML = `<form><label for="e">Email</label><input id="e" type="email"><label for="n">Full name</label><input id="n"><label for="p">Phone</label><input id="p" type="tel"></form>`;
    expect(isApplicationPage(document, window)).toBe(true);
    expect(readPosting(document).form).toBe(true);
  });

  it("reads the role and the employer from the title shape of a known application system", async () => {
    const cases: [string, string, string, string][] = [
      ["https://job-boards.greenhouse.io/example/jobs/1", "Job Application for Software Engineer - Platform at Example Corp", "Software Engineer - Platform", "Example Corp"],
      ["https://jobs.lever.co/example/0f3c/apply", "Example Corp - Backend Engineer - Data Platform", "Backend Engineer - Data Platform", "Example Corp"],
      ["https://jobs.ashbyhq.com/example/0f3c/application", "Software Engineer, Frontend @ Example Corp", "Software Engineer, Frontend", "Example Corp"],
      ["https://example.com/careers/backend", "Backend Engineer - Example Corp", "Backend Engineer", "Example Corp"],
    ];
    for (const [url, title, role, company] of cases) {
      page(url, title, `<main>${POSTING}</main>`);
      expect(await capturePage(), title).toMatchObject({ role, company });
    }
  });
});
