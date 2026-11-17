package credentials

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func seedPasswords(t *testing.T) *Service {
	t.Helper()
	c, v, st := setup(t, nil)
	for _, p := range []struct{ name, kind, host, pw string }{
		{"signin-default", "signin_default", "", "Default-Pw-1!x"},
		{"signin-site-careers.example.test", "signin_site", "careers.example.test", "Site-Pw-2!y"},
		{"tenant-workday-example", "tenant", "", "Tenant-Pw-3!z"},
		{"signin-alternate-0123456789ab", "signin_alternate", "", "Alt-Pw-4!a"},
		{"signin-alternate-ba9876543210", "signin_alternate", "", "Default-Pw-1!x"},
	} {
		if _, err := v.Set(p.name, p.pw); err != nil {
			t.Fatal(err)
		}
		index(t, st, p.name, p.kind, p.host, "alex@example.com")
	}
	return c
}

func labels(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Label)
	}
	return out
}

func TestCandidates_TryOrderNamedSystemAndGenericAndDedup(t *testing.T) {
	c := seedPasswords(t)
	ctx := context.Background()
	got, err := c.Candidates(ctx, "example.wd5.myworkdayjobs.com", "workday", "example")
	if err != nil {
		t.Fatal(err)
	}
	// Tenant key, (no site override for this host), shared default, alternate;
	// the second alternate repeats the default's pair and is tried once.
	want := []string{"tenant-key", "settings-default", "alternate-1"}
	if g := labels(got); len(g) != len(want) || g[0] != want[0] || g[1] != want[1] || g[2] != want[2] {
		t.Fatalf("workday order %v, want %v", g, want)
	}
	got, err = c.Candidates(ctx, "careers.example.test", "generic", "careers.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if g := labels(got); len(g) != 3 || g[0] != "settings-site" || g[1] != "settings-default" || g[2] != "alternate-1" {
		t.Fatalf("generic order %v", g)
	}
}

func TestCandidates_KeepNeverCopiesTheDefaultIntoASiteOverride(t *testing.T) {
	c := seedPasswords(t)
	ctx := context.Background()
	cands, _ := c.Candidates(ctx, "jobs.example.test", "generic", "jobs.example.test")
	var alt, def Candidate
	for _, x := range cands {
		switch x.Kind {
		case KindAlternate:
			alt = x
		case KindDefault:
			def = x
		}
	}
	if err := c.Keep(ctx, "jobs.example.test", "generic", "jobs.example.test", def); err != nil {
		t.Fatal(err)
	}
	after, _ := c.Candidates(ctx, "jobs.example.test", "generic", "jobs.example.test")
	if after[0].Kind == KindSite {
		t.Fatal("the shared default was copied into a site override")
	}
	if err := c.Keep(ctx, "jobs.example.test", "generic", "jobs.example.test", alt); err != nil {
		t.Fatal(err)
	}
	after, _ = c.Candidates(ctx, "jobs.example.test", "generic", "jobs.example.test")
	if after[0].Kind != KindSite || after[0].Password != alt.Password {
		t.Fatalf("an alternate that signed in did not become the site override: %+v", after[0])
	}
	// On a named system the winner becomes the tenant key.
	named, _ := c.Candidates(ctx, "acme.wd1.myworkdayjobs.com", "workday", "acme")
	if err := c.Keep(ctx, "acme.wd1.myworkdayjobs.com", "workday", "acme", named[0]); err != nil {
		t.Fatal(err)
	}
	named, _ = c.Candidates(ctx, "acme.wd1.myworkdayjobs.com", "workday", "acme")
	if named[0].Kind != KindTenant || named[0].Name != "tenant-workday-acme" || named[0].Label != "tenant-key" {
		t.Fatalf("tenant key not kept: %+v", named[0])
	}
}

func TestCandidates_RemoveDropsASavedPasswordAndItsRow(t *testing.T) {
	c := seedPasswords(t)
	ctx := context.Background()
	if err := c.Remove(ctx, "signin-site-careers.example.test"); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Candidates(ctx, "careers.example.test", "generic", "careers.example.test")
	for _, x := range got {
		if x.Kind == KindSite {
			t.Fatal("the removed override is still offered")
		}
	}
}

func TestGenerate_RulesAndNoRepeatsAcrossTenThousandPasswords(t *testing.T) {
	has := func(s, set string) bool {
		for _, r := range s {
			for _, c := range set {
				if r == c {
					return true
				}
			}
		}
		return false
	}
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		max := []int{0, 20, 8}[i%3]
		pw, err := Generate(max)
		if err != nil {
			t.Fatal(err)
		}
		if seen[pw] {
			t.Fatalf("password %d repeats an earlier one", i)
		}
		seen[pw] = true
		want := 24
		switch max {
		case 20:
			want = 20
		case 8:
			want = 12 // the minimum of 12 wins over a maximum below 12
		}
		if len(pw) != want || !has(pw, lower) || !has(pw, upper) || !has(pw, digits) || !has(pw, symbols) || tripled([]byte(pw)) {
			t.Fatalf("password of length %d breaks the rules", len(pw))
		}
	}
}

func TestGenerateFor_ReturnsTheExistingPasswordAndNeverOverwrites(t *testing.T) {
	c, v, _ := setup(t, nil)
	first, created, err := c.GenerateFor("signin-site-careers.example.test", 0)
	if err != nil || !created {
		t.Fatal(err)
	}
	second, created, err := c.GenerateFor("signin-site-careers.example.test", 0)
	if err != nil || created || second != first {
		t.Fatalf("created=%v same=%v", created, second == first)
	}
	if got, _ := v.Get("signin-site-careers.example.test"); got != first {
		t.Fatal("stored password changed")
	}
}

func TestStoredAccounts_ListsSiteOverridesAndTenantKeysNeverAValue(t *testing.T) {
	c := seedPasswords(t)
	if err := c.s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE credential_index SET system = 'workday', tenant = 'example' WHERE name = 'tenant-workday-example'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := c.StoredAccounts(context.Background())
	want := []Account{{Host: "careers.example.test"}, {System: "workday", Tenant: "example"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, pw := range []string{"Default-Pw-1!x", "Site-Pw-2!y", "Tenant-Pw-3!z", "Alt-Pw-4!a"} {
		if strings.Contains(fmt.Sprintf("%+v", got), pw) {
			t.Fatal("an account carries a stored value")
		}
	}
}
