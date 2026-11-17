// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Job-site and application-form detection (the site table and the job-page heuristic).
import { describe, expect, test } from "vitest";
import { looksLikeJobPage, SITES, siteOf } from "../src/sites";

/** One matching and one look-alike URL per site id. */
const TABLE: Record<string, { hit: string; miss: string }> = {
  linkedin: { hit: "https://www.linkedin.com/jobs/view/123", miss: "https://www.linkedin.com.example.test/jobs/view/123" },
  builtin: { hit: "https://builtin.com/job/engineer/1", miss: "https://builtin.com.example.test/job/engineer/1" },
  indeed: { hit: "https://www.indeed.com/viewjob?jk=1", miss: "https://www.indeed.com.example.test/viewjob" },
  "indeed-apply": { hit: "https://smartapply.indeed.com/beta/indeedapply/form", miss: "https://smartapply.indeed.com.example.test/form" },
  jobright: { hit: "https://jobright.ai/jobs/info/1", miss: "https://jobright.ai.example.test/jobs/info/1" },
  newgrad: { hit: "https://www.newgrad-jobs.com/", miss: "https://newgrad-jobs.com.example.test/" },
  wellfound: { hit: "https://wellfound.com/jobs", miss: "https://wellfound.com.example.test/jobs" },
  greenhouse: { hit: "https://boards.greenhouse.io/example/jobs/4001", miss: "https://boards.greenhouse.io/example" },
  "greenhouse-eu": { hit: "https://job-boards.eu.greenhouse.io/example/jobs/4001", miss: "https://job-boards.eu.greenhouse.io.example.test/example/jobs/4001" },
  "greenhouse-embed": { hit: "https://boards.greenhouse.io/embed/job_app?for=example&token=4001", miss: "https://boards.greenhouse.io/embed/job_board?for=example" },
  lever: { hit: "https://jobs.lever.co/example/abc", miss: "https://jobs.lever.co.example.test/example/abc" },
  ashby: { hit: "https://jobs.ashbyhq.com/example/abc", miss: "https://jobs.ashbyhq.com.example.test/example" },
  workday: { hit: "https://example.wd5.myworkdayjobs.com/en-US/site/job/x_R1", miss: "https://myworkdayjobs.com.example.test/site" },
  "workday-site": { hit: "https://wd3.myworkdaysite.com/recruiting/example/site", miss: "https://myworkdaysite.com.example.test/x" },
  workable: { hit: "https://apply.workable.com/example/j/ABC/", miss: "https://apply.workable.com.example.test/example" },
  rippling: { hit: "https://ats.rippling.com/example/jobs/abc", miss: "https://ats.rippling.com.example.test/example/jobs/abc" },
  hirebridge: { hit: "https://example.hirebridge.com/v3/Jobs/JobDetails.aspx?cid=1", miss: "https://hirebridge.com.example.test/x" },
  successfactors: { hit: "https://career5.successfactors.com/career?company=example", miss: "https://successfactors.com.example.test/career" },
  icims: { hit: "https://careers-example.icims.com/jobs/1/engineer/job", miss: "https://careers-example.icims.com.example.test/jobs/1" },
  smartrecruiters: { hit: "https://jobs.smartrecruiters.com/Example/743999-engineer", miss: "https://smartrecruiters.com.example.test/x" },
  taleo: { hit: "https://example.taleo.net/careersection/ex/jobdetail.ftl", miss: "https://taleo.net.example.test/careersection" },
  bamboohr: { hit: "https://example.bamboohr.com/careers/12", miss: "https://bamboohr.com.example.test/careers/12" },
  jobvite: { hit: "https://jobs.jobvite.com/example/job/o1", miss: "https://jobs.jobvite.com.example.test/example" },
  personio: { hit: "https://example.jobs.personio.de/job/1", miss: "https://example.jobs.personio.de.example.test/job/1" },
  teamtailor: { hit: "https://example.teamtailor.com/jobs/1-engineer", miss: "https://teamtailor.com.example.test/jobs/1" },
  recruitee: { hit: "https://example.recruitee.com/o/engineer", miss: "https://example.recruitee.com/" },
  pinpoint: { hit: "https://example.pinpointhq.com/en/postings/abc", miss: "https://example.pinpointhq.com/en/about" },
  eightfold: { hit: "https://example.eightfold.ai/careers/apply?pid=1", miss: "https://example.eightfold.ai/careers?query=go" },
  jazzhr: { hit: "https://example.applytojob.com/apply/abc/Engineer", miss: "https://example.applytojob.com.example.test/apply/abc" },
  breezy: { hit: "https://example.breezy.hr/p/abc-engineer", miss: "https://example.breezy.hr/" },
  oracle: { hit: "https://example.fa.us2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX/job/1", miss: "https://example.fa.us2.oraclecloud.com/other" },
  "adp-workforcenow": { hit: "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html?cid=1", miss: "https://workforcenow.adp.com.example.test/mascsr/default/mdf/recruitment/recruitment.html" },
  "adp-recruiting": { hit: "https://recruiting.adp.com/srccar/public/RTI.home?c=1", miss: "https://recruiting.adp.com.example.test/srccar" },
  dayforce: { hit: "https://jobs.dayforcehcm.com/en-US/example/CANDIDATEPORTAL/jobs/1", miss: "https://jobs.dayforcehcm.com.example.test/x" },
  ukg: { hit: "https://recruiting.ultipro.com/EXA1000/JobBoard/abc/OpportunityDetail?opportunityId=1", miss: "https://recruiting.ultipro.com.example.test/EXA1000" },
  paylocity: { hit: "https://recruiting.paylocity.com/recruiting/jobs/Details/1/abc/Engineer", miss: "https://recruiting.paylocity.com.example.test/recruiting/jobs" },
  paycom: { hit: "https://www.paycomonline.net/v4/ats/web.php/jobs/ViewJobDetails?job=1", miss: "https://paycomonline.net.example.test/v4/ats/web.php/jobs" },
};

describe("Site table", () => {
  test("Pattern table: at least 35 patterns, each accepts its example URL and rejects its look-alike", () => {
    expect(SITES.length).toBeGreaterThanOrEqual(35);
    expect(new Set(SITES.map((s) => s.id))).toEqual(new Set(Object.keys(TABLE)));
    for (const [id, { hit, miss }] of Object.entries(TABLE)) {
      expect(siteOf(hit), `${id} accepts ${hit}`).toBe(id);
      expect(siteOf(miss), `${id} rejects ${miss}`).not.toBe(id);
    }
  });

  test("A vendor's own marketing site is no application system: the bare domain and its www host of a tenant-hosted system", () => {
    for (const domain of ["icims.com", "taleo.net", "bamboohr.com", "teamtailor.com", "ukg.com"]) {
      expect(siteOf(`https://www.${domain}/demo`), domain).toBeNull();
      expect(siteOf(`https://${domain}/contact`), domain).toBeNull();
    }
    expect(siteOf("https://example.rec.pro.ukg.com/EXA/JobBoard/abc")).toBe("ukg");
  });

  test("Only http and https match", () => {
    expect(siteOf("ftp://jobs.lever.co/example/abc")).toBeNull();
    expect(siteOf("javascript:void(0)//jobs.lever.co")).toBeNull();
    expect(siteOf("http://jobs.lever.co/example/abc")).toBe("lever");
    expect(siteOf("not a url")).toBeNull();
  });

  test("The SAP aliases are one family", () => {
    for (const host of ["career8.successfactors.com", "career.successfactors.eu", "example.sapsf.com", "example.ns2cloud.com"]) {
      expect(siteOf(`https://${host}/career`), host).toBe("successfactors");
    }
  });
});

describe("Job-page heuristic", () => {
  test("A path or query that reads like a job or application page passes for a host outside the table", () => {
    for (const url of [
      "https://example.com/careers/engineering",
      "https://example.com/jobs/123",
      "https://example.com/en/apply",
      "https://example.com/page?gh_jid=4001",
      "https://example.com/open-positions",
      "https://example.com/recruiting/start",
      "https://example.com/join-us",
    ]) {
      expect(looksLikeJobPage(url), url).toBe(true);
    }
  });

  test("The host label alone proves nothing, and ordinary pages fail", () => {
    for (const url of ["https://jobs.example.com/", "https://example.com/pricing", "https://example.com/blog/jobsite-safety-tips", "ftp://example.com/jobs/1", "bad"]) {
      expect(looksLikeJobPage(url), url).toBe(false);
    }
  });
});
