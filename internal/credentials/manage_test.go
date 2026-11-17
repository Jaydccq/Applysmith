package credentials

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const strongPw = "Correct-Horse-9!"

func changedAt(t *testing.T, st *store.Store) string {
	t.Helper()
	var v string
	err := st.Read().QueryRow(`SELECT value FROM credential_meta WHERE key = 'credentials_changed_at'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// storeHolds reports whether any column of any table holds the needle.
func storeHolds(t *testing.T, st *store.Store, needle string) bool {
	t.Helper()
	var tables []string
	rows, err := st.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables to scan")
	}
	for _, n := range tables {
		rows, err := st.Read().Query("SELECT * FROM " + n)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				found = found || strings.Contains(fmt.Sprint(v), needle)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		if found {
			return true
		}
	}
	return false
}

// clock returns a service whose time moves one minute per call, so a stamp
// that moved is visible and one that did not is too.
func ticking(t *testing.T) (*Service, *vault.Vault, *store.Store, *vault.Memory) {
	t.Helper()
	mem := vault.NewMemory()
	c, v, st := setup(t, func(vault.Backend) vault.Backend { return mem })
	at := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { at = at.Add(time.Minute); return at }
	return c, v, st, mem
}

func TestSetDefault_EmailGoesToTheIndexAndTheKeychainValueIsThePasswordOnly(t *testing.T) {
	c, v, st, mem := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", strongPw); err != nil {
		t.Fatal(err)
	}
	account := v.Account("signin-default")
	if got, _ := mem.Get(account); got != strongPw {
		t.Fatal("the keychain value is not the password alone")
	}
	var email string
	if err := st.Read().QueryRow(`SELECT email FROM credential_index WHERE name = 'signin-default'`).Scan(&email); err != nil || email != "alex@example.com" {
		t.Fatalf("index email %q %v", email, err)
	}
	if storeHolds(t, st, strongPw) {
		t.Fatal("a password reached the database")
	}
}

func TestSetDefault_WeakPasswordNamesTheUnmetRulesAndStoresNothing(t *testing.T) {
	c, _, st, mem := ticking(t)
	_, err := c.SetDefault(context.Background(), "alex@example.com", "password1")
	var weak *WeakPasswordError
	if !errors.As(err, &weak) {
		t.Fatalf("err = %v", err)
	}
	for _, rule := range []string{"at least 12 characters", "an uppercase letter", "a symbol"} {
		if !strings.Contains(err.Error(), rule) {
			t.Errorf("%q does not name %q", err, rule)
		}
	}
	if strings.Contains(err.Error(), "password1") {
		t.Fatal("the weak value was echoed")
	}
	if len(mem.Accounts()) != 0 {
		t.Fatal("a weak password was stored")
	}
	var rows int
	if err := st.Read().QueryRow(`SELECT count(*) FROM credential_index`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a refused default left an index row: %d %v", rows, err)
	}
	if storeHolds(t, st, "password1") {
		t.Fatal("the weak value reached the database")
	}
}

func TestUnmetRules_EachRuleIsIndependent(t *testing.T) {
	tests := map[string][]string{
		strongPw:           nil,
		"Short-1!":         {"at least 12 characters"},
		"alllowercase-1!!": {"an uppercase letter"},
		"ALLUPPERCASE-1!!": {"a lowercase letter"},
		"NoDigitsHere-!!!": {"a number"},
		"NoSymbolsHere123": {"a symbol"},
		"":                 {"at least 12 characters", "an uppercase letter", "a lowercase letter", "a number", "a symbol"},
	}
	for pw, want := range tests {
		got := UnmetRules(pw)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("UnmetRules(%q) = %v, want %v", pw, got, want)
		}
	}
}

func TestSetSite_OverrideAcceptsAWeakPasswordButNeedsAnEmail(t *testing.T) {
	c, v, _, _ := ticking(t)
	ctx := context.Background()
	if _, err := c.SetSite(ctx, "https://careers.example.test/jobs/1", "", "short1"); !errors.Is(err, ErrEmailRequired) {
		t.Fatalf("no email anywhere: %v", err)
	}
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSite(ctx, "https://careers.example.test/jobs/1", "", "short1"); err != nil {
		t.Fatalf("a weak override was refused: %v", err)
	}
	if got, _ := v.Get("signin-site-careers.example.test"); got != "short1" {
		t.Fatalf("stored %q", got)
	}
}

func TestSiteKey_IsTheLowercaseHostnameAndNothingCleverer(t *testing.T) {
	good := map[string]string{
		"https://Careers.Example.TEST:8443/a/b?x=1":  "careers.example.test",
		"http://jobs.example.test/":                  "jobs.example.test",
		"Careers.Example.Test":                       "careers.example.test",
		"https://acme.wd5.myworkdayjobs.com/en-US/x": "acme.wd5.myworkdayjobs.com",
	}
	for in, want := range good {
		if got, err := SiteKey(in); err != nil || got != want {
			t.Errorf("SiteKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://example.test/x", "file:///etc/hosts", "javascript:alert(1)", "https://user:pw@example.test/", "https:///nohost", "mailto:a@example.test"} {
		if got, err := SiteKey(bad); err == nil {
			t.Errorf("SiteKey(%q) = %q, want a refusal", bad, got)
		}
	}
}

func TestSetDefault_EmailOnlyUpdateTouchesNoKeychainEntryAndNoStamp(t *testing.T) {
	c, _, st, mem := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if len(mem.Accounts()) != 0 || changedAt(t, st) != "" {
		t.Fatalf("email only: accounts %v stamp %q", mem.Accounts(), changedAt(t, st))
	}
	if _, err := c.SetDefault(ctx, "", ""); !errors.Is(err, ErrNothingToSet) {
		t.Fatalf("nothing: %v", err)
	}
	if _, err := c.SetDefault(ctx, "not an email", ""); !errors.Is(err, ErrBadEmail) {
		t.Fatalf("bad email: %v", err)
	}
}

func TestChangedAtStamp_MovesForSharedPasswordsOnly(t *testing.T) {
	c, _, st, _ := ticking(t)
	ctx := context.Background()
	var alt string
	type step struct {
		name string
		do   func() error
		move bool
	}
	steps := []step{
		{"default email alone", func() error { _, err := c.SetDefault(ctx, "alex@example.com", ""); return err }, false},
		{"default password set", func() error { _, err := c.SetDefault(ctx, "", strongPw); return err }, true},
		{"default email changed", func() error { _, err := c.SetDefault(ctx, "alex.example@example.org", ""); return err }, false},
		{"site override set", func() error { _, err := c.SetSite(ctx, "careers.example.test", "", "pw"); return err }, false},
		{"alternate added", func() (err error) { alt, err = c.AddAlternate(ctx, "", "Alt-Pw-1!x"); return }, true},
		{"alternates reordered", func() error { return c.ReorderAlternates(ctx, []string{alt}) }, true},
		{"site override deleted", func() error { return c.Delete(ctx, "signin-site-careers.example.test") }, false},
		{"alternate removed", func() error { return c.Delete(ctx, "signin-alternate-"+alt) }, true},
		{"default deleted", func() error { return c.Delete(ctx, "signin-default") }, true},
	}
	last := changedAt(t, st)
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		now := changedAt(t, st)
		if moved := now != last; moved != s.move {
			t.Errorf("%s: stamp moved = %v, want %v", s.name, moved, s.move)
		}
		last = now
	}
}

func TestDelete_SiteOverrideKeepsItsAccountHistoryOnlyWhenAnAccountWasRecorded(t *testing.T) {
	c, v, st, _ := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"proven.example.test", "fresh.example.test"} {
		if _, err := c.SetSite(ctx, host, "", "pw-"+host); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Created(ctx, "proven.example.test", "generic", "proven.example.test", "alex@example.com", "signin-site-proven.example.test"); err != nil {
		t.Fatal(err)
	}
	type hist struct{ created, signin, outcome sql.NullString }
	read := func(name string) (hist, bool, int) {
		var h hist
		var has int
		err := st.Read().QueryRow(`SELECT account_created_at, last_signin_at, last_outcome, has_secret FROM credential_index WHERE name = ?`, name).Scan(&h.created, &h.signin, &h.outcome, &has)
		return h, !errors.Is(err, sql.ErrNoRows), has
	}
	before, _, _ := read("signin-site-proven.example.test")
	for _, host := range []string{"proven.example.test", "fresh.example.test"} {
		if err := c.Delete(ctx, "signin-site-"+host); err != nil {
			t.Fatal(err)
		}
		if _, err := v.Get("signin-site-" + host); !vault.IsCode(err, vault.CodeNotFound) {
			t.Fatalf("%s: the secret survived: %v", host, err)
		}
	}
	after, kept, has := read("signin-site-proven.example.test")
	if !kept || has != 0 || after != before {
		t.Fatalf("a recorded account lost its history: kept=%v has_secret=%d %+v -> %+v", kept, has, before, after)
	}
	if _, kept, _ := read("signin-site-fresh.example.test"); kept {
		t.Fatal("a row without account history survived its delete")
	}
	if err := c.Delete(ctx, "signin-site-never.example.test"); !vault.IsCode(err, vault.CodeNotFound) {
		t.Fatalf("deleting nothing: %v", err)
	}
}

func TestRecordAccount_CreatedTimeIsSetOnceAndEverySignInAdvancesTheLastOne(t *testing.T) {
	c, _, st, _ := ticking(t)
	ctx := context.Background()
	name := "signin-site-careers.example.test"
	read := func() (created, signin, outcome string) {
		if err := st.Read().QueryRow(`SELECT account_created_at, last_signin_at, last_outcome FROM credential_index WHERE name = ?`, name).Scan(&created, &signin, &outcome); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := c.Created(ctx, "careers.example.test", "generic", "careers.example.test", "alex@example.com", name); err != nil {
		t.Fatal(err)
	}
	created1, signin1, outcome1 := read()
	if outcome1 != "created" || signin1 == "" {
		t.Fatalf("after creating: %q %q %q", created1, signin1, outcome1)
	}
	cand := Candidate{Name: name, Kind: KindSite, Email: "alex@example.com", Password: "x"}
	if err := c.Keep(ctx, "careers.example.test", "generic", "careers.example.test", cand); err != nil {
		t.Fatal(err)
	}
	created2, signin2, outcome2 := read()
	if created2 != created1 || signin2 == signin1 || outcome2 != "signed-in" {
		t.Fatalf("created %q -> %q, last sign-in %q -> %q, outcome %q", created1, created2, signin1, signin2, outcome2)
	}
	if err := c.Created(ctx, "careers.example.test", "generic", "careers.example.test", "alex@example.com", name); err != nil {
		t.Fatal(err)
	}
	if created3, _, _ := read(); created3 != created1 {
		t.Fatalf("a second record moved the created time: %q -> %q", created1, created3)
	}
}

func TestAddAlternate_IdIsRandomHexTheLabelDefaultsAndTheOrderAppends(t *testing.T) {
	c, v, st, mem := ticking(t)
	ctx := context.Background()
	if _, err := c.AddAlternate(ctx, "", "Alt-Pw-1!x"); !errors.Is(err, ErrEmailRequired) {
		t.Fatalf("before any email: %v", err)
	}
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddAlternate(ctx, strings.Repeat("l", 41), "x"); !errors.Is(err, ErrLabelTooLong) {
		t.Fatalf("long label: %v", err)
	}
	first, err := c.AddAlternate(ctx, "", "Alt-Pw-1!x")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.AddAlternate(ctx, "  Old laptop  ", "any non-empty value")
	if err != nil {
		t.Fatal(err)
	}
	hex12 := regexp.MustCompile(`^[0-9a-f]{12}$`)
	if !hex12.MatchString(first) || !hex12.MatchString(second) || first == second {
		t.Fatalf("ids %q %q", first, second)
	}
	for _, acc := range mem.Accounts() {
		if strings.Contains(acc, "Alt-Pw") || strings.Contains(acc, "laptop") {
			t.Fatalf("an account name says something about the password or label: %s", acc)
		}
	}
	if got, _ := v.Get("signin-alternate-" + first); got != "Alt-Pw-1!x" {
		t.Fatal("alternate value not stored")
	}
	entries, err := c.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, e := range entries {
		if e.Kind == KindAlternate {
			labels = append(labels, fmt.Sprintf("%d:%s", e.Position, e.Label))
		}
	}
	if strings.Join(labels, ",") != "1:Alternate 1,2:Old laptop" {
		t.Fatalf("alternates = %v", labels)
	}
	var email sql.NullString
	if err := st.Read().QueryRow(`SELECT email FROM credential_index WHERE name = ?`, "signin-alternate-"+first).Scan(&email); err != nil || email.Valid {
		t.Fatalf("an alternate stores no email of its own: %v %v", email, err)
	}
}

func TestReorderAlternates_RewritesPositionsAndTheNextSignInTriesTheMovedOneFirst(t *testing.T) {
	c, _, _, _ := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	a, _ := c.AddAlternate(ctx, "A", "pw-a")
	b, _ := c.AddAlternate(ctx, "B", "pw-b")
	for _, bad := range [][]string{{a}, {a, a}, {a, "ffffffffffff"}, {a, b, b}, nil} {
		if err := c.ReorderAlternates(ctx, bad); !errors.Is(err, ErrBadOrder) {
			t.Errorf("order %v: %v", bad, err)
		}
	}
	if err := c.ReorderAlternates(ctx, []string{b, a}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Candidates(ctx, "jobs.example.test", "generic", "jobs.example.test")
	if err != nil || len(got) != 2 || got[0].Password != "pw-b" || got[1].Password != "pw-a" {
		t.Fatalf("candidates %+v %v", got, err)
	}
}

func TestAlternateUsesTheCurrentDefaultEmail(t *testing.T) {
	c, _, _, _ := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddAlternate(ctx, "", "pw-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetDefault(ctx, "alex.example@example.org", ""); err != nil {
		t.Fatal(err)
	}
	got, err := c.Candidates(ctx, "jobs.example.test", "generic", "jobs.example.test")
	if err != nil || len(got) != 1 || got[0].Email != "alex.example@example.org" {
		t.Fatalf("candidates %+v %v", got, err)
	}
}

func TestEntries_NeverCarriesAValueAndSeparatesKeychainTroubleFromAbsence(t *testing.T) {
	c, _, _, mem := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", strongPw); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSite(ctx, "careers.example.test", "", "Site-Secret-Canary-1"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out := fmt.Sprintf("%+v", got); strings.Contains(out, strongPw) || strings.Contains(out, "Canary") {
		t.Fatalf("an entry carries a value: %s", out)
	}
	byName := map[string]Entry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if e := byName["signin-default"]; !e.Present || e.Source != vault.SourceKeychain || e.Email != "alex@example.com" {
		t.Errorf("default = %+v", e)
	}
	if e, ok := byName["cloudflared-tunnel-token"]; !ok || e.Present {
		t.Errorf("a registered single-name secret is listed as absent: %+v %v", e, ok)
	}
	mem.FailWith(vault.ErrDenied)
	got, err = c.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Name == "signin-default" && (e.Present || e.Problem != vault.CodeKeychainDenied) {
			t.Errorf("a refusing keychain reads as %+v", e)
		}
	}
}

func TestStoredAccounts_AreReadFromTheIndexWithoutTouchingTheKeychain(t *testing.T) {
	c, _, _, mem := ticking(t)
	ctx := context.Background()
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSite(ctx, "careers.example.test", "", "pw"); err != nil {
		t.Fatal(err)
	}
	mem.FailWith(vault.ErrUnavailable)
	got, err := c.StoredAccounts(ctx)
	if err != nil || len(got) != 1 || got[0].Host != "careers.example.test" {
		t.Fatalf("accounts %+v %v", got, err)
	}
}

// An account whose password the user deleted keeps its row without a secret;
// when the application creates the account again the row holds one again.
func TestCreated_AfterTheUserDeletedThePasswordTheRowHoldsASecretAgain(t *testing.T) {
	c, _, st, _ := ticking(t)
	ctx := context.Background()
	const host, name = "careers.example.test", "signin-site-careers.example.test"
	create := func() {
		t.Helper()
		if _, _, err := c.GenerateFor(name, 0); err != nil {
			t.Fatal(err)
		}
		if err := c.Created(ctx, host, "generic", host, "alex@example.com", name); err != nil {
			t.Fatal(err)
		}
	}
	create()
	if err := c.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	var has int
	if err := st.Read().QueryRow(`SELECT has_secret FROM credential_index WHERE name = ?`, name).Scan(&has); err != nil || has != 0 {
		t.Fatalf("after the delete: has_secret %d err %v, want the row kept without a secret", has, err)
	}
	create()
	got, err := c.StoredAccounts(ctx)
	if err != nil || len(got) != 1 || got[0].Host != host {
		t.Fatalf("accounts %+v %v", got, err)
	}
}

// Only a shared password, the host's own override or a later sign-in lifts a
// hold; a tenant key edited behind the application's back does not.
func TestHeld_OnlyTheListedChangesLiftAHold(t *testing.T) {
	c, v, st, _ := ticking(t)
	ctx := context.Background()
	host := "example.wd5.myworkdayjobs.com"
	if _, err := c.SetDefault(ctx, "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error { return c.RecordExhausted(ctx, tx, host, false, "r1") }); err != nil {
		t.Fatal(err)
	}
	held := func(why string, want bool) {
		t.Helper()
		if h, err := c.Held(ctx, host); err != nil || h.Held != want {
			t.Fatalf("%s: held = %v (%v), want %v", why, h.Held, err, want)
		}
	}
	held("just recorded", true)
	if _, err := v.Set("tenant-workday-example", "Tenant-Pw-1!x"); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, system, tenant, has_secret, created_at, updated_at) VALUES ('tenant-workday-example', 'tenant', 'workday', 'example', 1, ?1, ?1)`, c.stampNow())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	held("a tenant key changed outside Settings", true)
	if _, err := c.SetSite(ctx, "jobs.example.test", "", "pw"); err != nil {
		t.Fatal(err)
	}
	held("another host's override", true)
	if _, err := c.SetSite(ctx, host, "", "Site-Pw-2!y"); err != nil {
		t.Fatal(err)
	}
	held("this host's own override", false)
}
