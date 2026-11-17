package discovery

import (
	"testing"
	"time"
)

func TestAdmission_NonPostingURLsRefused(t *testing.T) {
	refused := []string{
		"https://jobs.example.test/jobs/recommend", "https://www.example.test/jobs/search-results?keywords=engineer", "https://www.example.test/careers/",
		"https://www.linkedin.com/jobs/collections/recommended/", "https://www.example.test/careers?location=Austin",
	}
	for _, u := range refused {
		canon, err := postingURL(u)
		if err != nil || !NonPostingURL(canon) {
			t.Errorf("%s was admitted (canon %q, err %v)", u, canon, err)
		}
	}
	admitted := []string{
		"https://www.linkedin.com/jobs/view/3912345678/", "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001",
		"https://www.linkedin.com/jobs/collections/recommended/?currentJobId=3912345678", "https://www.example.test/",
		// An employer's own careers page can name the posting in the query.
		"https://ngc.eightfold.ai/careers?pid=1340074810317", "https://www.example.test/careers?gh_jid=4001&utm_source=x", "https://www.example.test/jobs?jobId=77",
	}
	for _, u := range admitted {
		canon, err := postingURL(u)
		if err != nil || NonPostingURL(canon) {
			t.Errorf("%s was refused (canon %q, err %v)", u, canon, err)
		}
	}
	for _, u := range []string{"", "ftp://example.test/jobs/1", "javascript:alert(1)", "not a url"} {
		if _, err := postingURL(u); err == nil {
			t.Errorf("%q was admitted", u)
		}
	}
}

func TestAdmission_RecommendationPageCountedBeforeScoring(t *testing.T) {
	e, fs, _ := githubEnv(t, "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n"+
		"| Example Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/jobs/recommend) | 2h |\n"+
		"| Sample Inc | Data Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/2) | 2h |\n")
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	if rec.Counts.Filtered[FilterNonPostingURL] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	for _, r := range fs.requests() {
		if r.Path == "/jobs/recommend" {
			t.Fatal("a detail fetch was made for a refused row")
		}
	}
}

func TestParseWindow(t *testing.T) {
	for in, want := range map[string]time.Duration{"6h": 6 * time.Hour, "24h": 24 * time.Hour, "3d": 72 * time.Hour, "2w": 14 * 24 * time.Hour} {
		if got, err := ParseWindow(in); err != nil || got != want {
			t.Errorf("ParseWindow(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "h", "0h", "5x", "abc"} {
		if _, err := ParseWindow(in); err == nil {
			t.Errorf("ParseWindow(%q) succeeded", in)
		}
	}
}
