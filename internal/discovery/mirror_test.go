package discovery

import (
	"net/http"
	"strings"
	"testing"
)

const mirrorReadme = "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
	"| Example Co | Software Engineer | Austin, TX | [Apply](https://mirror.example.test/job/1) | 2h |\n"

func mirrorEnv(t *testing.T, resolve func(mirrorURL, company, role string) MirrorResult) (*env, *feedServer) {
	e, fs, _ := githubEnv(t, mirrorReadme)
	e.sc.Mirror = fakeMirror{hosts: []string{"mirror.example.test"}, resolve: resolve}
	return e, fs
}

func TestMirror_ResolvedRowKeepsMirrorAsAliasAndDedupes(t *testing.T) {
	var asked []string
	e, _ := mirrorEnv(t, func(u, c, r string) MirrorResult {
		asked = append(asked, u+"|"+c+"|"+r)
		return MirrorResult{URL: "https://job-boards.greenhouse.io/example-co/jobs/4001"}
	})
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 1 || len(rows) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	row := rows[0]
	if row.URL != "https://job-boards.greenhouse.io/example-co/jobs/4001" || row.OriginalURL != "https://mirror.example.test/job/1" ||
		len(row.Aliases) != 1 || row.Aliases[0] != "https://mirror.example.test/job/1" || len(row.Flags) != 0 {
		t.Fatalf("row = %+v", row)
	}
	if len(asked) != 1 || asked[0] != "https://mirror.example.test/job/1|Example Co|Software Engineer" {
		t.Fatalf("resolver calls = %v", asked)
	}
	// The same mirror seen again resolves to the stored employer posting.
	again := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	if again.Counts.Inserted != 0 || again.Counts.Duplicates[StoreInbox] != 1 {
		t.Fatalf("second run = %+v", again.Counts)
	}
}

func TestMirror_UnresolvedKeepsReasonAndFlag(t *testing.T) {
	e, fs := mirrorEnv(t, func(string, string, string) MirrorResult {
		return MirrorResult{Reason: "aggregator-only posting; employer posting not found"}
	})
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	row := e.inbox(InboxQuery{})[0]
	if rec.Counts.Inserted != 1 || row.URL != "https://mirror.example.test/job/1" || len(row.Flags) != 1 || row.Flags[0] != FlagMirrorUnresolved ||
		!strings.Contains(strings.Join(row.Reasons, ";"), "mirror-unresolved: aggregator-only posting") {
		t.Fatalf("row = %+v", row)
	}
	// Resolution is reading only; the scan never writes to a site.
	for _, r := range fs.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("request %s %s", r.Method, r.Path)
		}
	}
}

func TestMirror_ResolverAnswerThatIsAnotherMirrorIsUnresolved(t *testing.T) {
	e, _ := mirrorEnv(t, func(string, string, string) MirrorResult {
		return MirrorResult{URL: "https://mirror.example.test/job/2"}
	})
	e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	row := e.inbox(InboxQuery{})[0]
	if row.URL != "https://mirror.example.test/job/1" || len(row.Aliases) != 0 || len(row.Flags) != 1 {
		t.Fatalf("row = %+v", row)
	}
}
