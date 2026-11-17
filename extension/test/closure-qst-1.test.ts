// Clean-room: docs/provenance.md#CR-ENGINE
// The employer a posting page names: the page's structured data first, then its site name, then its title; a system's own name is never the employer.
import { describe, expect, it } from "vitest";
import { postingFacts } from "../src/shared/capture";

const posting = (name: string) => ({ "@context": "https://schema.org", "@type": "JobPosting", title: "Backend Engineer", hiringOrganization: { name } });

function page(title: string, og?: string, ld?: object): void {
  document.head.innerHTML = (og ? `<meta property="og:site_name" content="${og}">` : "") + (ld ? `<script type="application/ld+json">${JSON.stringify(ld)}</script>` : "");
  document.title = title;
}

describe("job identity ladder", () => {
  it("json-ld hiring organization wins over og:site_name", () => {
    // The structured name beats the site name and the title, whatever they say.
    page("Backend Engineer at Title Corp", "Site Name Corp", posting("Example Corp"));
    expect(postingFacts(document)).toMatchObject({ company: "Example Corp", role: "Backend Engineer" });
    // Without structured data the site name comes next, then the title.
    page("Backend Engineer at Title Corp", "Site Name Corp");
    expect(postingFacts(document).company).toBe("Site Name Corp");
    page("Backend Engineer at Title Corp");
    expect(postingFacts(document).company).toBe("Title Corp");
    // A hiring system's name is not an employer at any rung: the next rung speaks.
    page("Backend Engineer at Title Corp", "Greenhouse");
    expect(postingFacts(document).company).toBe("Title Corp");
    page("Backend Engineer - Lever", "Workday");
    expect(postingFacts(document).company).toBeUndefined();
    // The organization may be a bare string.
    page("Backend Engineer", "Greenhouse", { "@type": "JobPosting", title: "Backend Engineer", hiringOrganization: "String Corp" });
    expect(postingFacts(document).company).toBe("String Corp");
  });
});
