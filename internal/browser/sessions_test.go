package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

// secretValue stands for a cookie value; no error, event or status may repeat it.
const secretValue = "cookie-value-for-tests-5c21d8"

// memJar is a cookie store that behaves like the browser where it matters: it
// acknowledges a SameSite=None cookie without Secure and then drops it.
type memJar struct {
	cookies      map[string]Cookie
	mutations    int
	refuseDelete func(Cookie) bool
	refuseSet    func(Cookie) bool
	onDelete     func()
}

func newJar(cs ...Cookie) *memJar {
	j := &memJar{cookies: map[string]Cookie{}}
	for _, c := range cs {
		j.cookies[c.key()] = c
	}
	return j
}

func (j *memJar) Cookies(context.Context) ([]Cookie, error) {
	out := make([]Cookie, 0, len(j.cookies))
	for _, c := range j.cookies {
		out = append(out, c)
	}
	return out, nil
}

func (j *memJar) SetCookie(_ context.Context, c Cookie) error {
	j.mutations++
	if j.refuseSet != nil && j.refuseSet(c) {
		return errors.New("refused")
	}
	if c.SameSite == "None" && !c.Secure {
		return nil
	}
	j.cookies[c.key()] = c
	return nil
}

func (j *memJar) DeleteCookie(_ context.Context, c Cookie) error {
	j.mutations++
	if j.onDelete != nil {
		j.onDelete()
	}
	if j.refuseDelete != nil && j.refuseDelete(c) {
		return errors.New("refused")
	}
	delete(j.cookies, c.key())
	return nil
}

func (j *memJar) names(host string) []string {
	var out []string
	for _, c := range j.cookies {
		if scoped(c.host(), host) {
			out = append(out, c.Name)
		}
	}
	slices.Sort(out)
	return out
}

func newSessions(t *testing.T, jar CookieJar) (*Sessions, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "browser")
	s := NewSessions(SessionsConfig{Dir: dir, Open: func(context.Context) (CookieJar, error) { return jar, nil }, Running: func() CookieJar { return jar }})
	return s, dir
}

func wc(domain, name string) wire.SessionCookie {
	exp := float64(time.Now().Add(48 * time.Hour).Unix())
	return wire.SessionCookie{Name: name, Value: secretValue, Domain: domain, Path: "/", Secure: true, HTTPOnly: true, SameSite: "lax", ExpirationDate: &exp, HostOnly: !strings.HasPrefix(domain, ".")}
}

func old(domain, name string) Cookie {
	return Cookie{Name: name, Value: "old-" + name, Domain: domain, Path: "/", Secure: true, Expires: float64(time.Now().Add(24 * time.Hour).Unix())}
}

func TestImportWithReadBack(t *testing.T) {
	jar := newJar()
	s, _ := newSessions(t, jar)
	cookies := []wire.SessionCookie{wc("jobs.example.test", "a"), wc("jobs.example.test", "b"), wc(".jobs.example.test", "c"), wc("jobs.example.test", "d")}
	dropped := wc("jobs.example.test", "e")
	dropped.SameSite, dropped.Secure = "no_restriction", false
	res, err := s.Import(context.Background(), []string{"jobs.example.test"}, append(cookies, dropped))
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 4 || res.Failed != 1 || res.Replaced != 0 || res.NotInSource != 0 {
		t.Fatalf("%+v", res)
	}
	if got := jar.names("jobs.example.test"); fmt.Sprint(got) != "[a b c d]" {
		t.Fatalf("the profile holds exactly the cookies that stayed: %v", got)
	}
}

func TestImportKeepsScopeFlagsAndGivesSessionCookiesThirtyDays(t *testing.T) {
	jar := newJar()
	s, _ := newSessions(t, jar)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	host, domain, sess := wc("jobs.example.test", "host"), wc(".example.test", "domain"), wc("jobs.example.test", "sess")
	sess.Session, sess.ExpirationDate, sess.SameSite = true, nil, "strict"
	if _, err := s.Import(context.Background(), []string{"example.test"}, []wire.SessionCookie{host, domain, sess}); err != nil {
		t.Fatal(err)
	}
	byName := map[string]Cookie{}
	for _, c := range jar.cookies {
		byName[c.Name] = c
	}
	if byName["host"].Domain != "jobs.example.test" || byName["domain"].Domain != ".example.test" {
		t.Fatalf("host-only and domain scope: %q %q", byName["host"].Domain, byName["domain"].Domain)
	}
	if want := float64(now.Add(30 * 24 * time.Hour).Unix()); byName["sess"].Expires != want {
		t.Fatalf("session cookie expiry %v want %v", byName["sess"].Expires, want)
	}
	if c := byName["sess"]; c.SameSite != "Strict" || !c.Secure || !c.HTTPOnly || c.Path != "/" || c.Value != secretValue {
		t.Fatalf("attributes: %+v", c)
	}
	if byName["host"].Expires != *host.ExpirationDate {
		t.Fatal("a cookie that has an expiry keeps it")
	}
}

func TestSnapshotFirstAndNothingChangesWhenItCannotBeWritten(t *testing.T) {
	jar := newJar(old("jobs.example.test", "keep"))
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSessions(SessionsConfig{Dir: filepath.Join(blocker, "browser"), Open: func(context.Context) (CookieJar, error) { return jar, nil }})
	_, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "a")})
	if code, _ := CodeOf(err); code != CodeSnapshotFailed {
		t.Fatalf("%v", err)
	}
	if jar.mutations != 0 || fmt.Sprint(jar.names("jobs.example.test")) != "[keep]" {
		t.Fatalf("the profile was touched: %d mutations", jar.mutations)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatal("the error repeats a cookie value")
	}
}

func TestSnapshotFileIsPrivateReplacedAndHoldsTheOldCookies(t *testing.T) {
	jar := newJar(old("jobs.example.test", "old1"), old("other.example.org", "untouched"))
	s, dir := newSessions(t, jar)
	if _, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "new1")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, snapshotFile)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", d.Mode())
	}
	var snap snapshot
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &snap); err != nil || len(snap.Cookies) != 1 || snap.Cookies[0].Name != "old1" || fmt.Sprint(snap.Domains) != "[jobs.example.test]" {
		t.Fatalf("the snapshot holds the cookies the import replaced: %s", data)
	}
	if _, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "new2")}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "new1") || strings.Contains(string(data), "old1") {
		t.Fatalf("the next import replaces the snapshot: %s", data)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		// The snapshot and the imported-sites ledger are expected; a dotfile
		// would be a temp file the atomic rename failed to clean up.
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("a temp file is left behind: %v", entries)
		}
	}
}

func TestReplacementDeletesOnlyInScopeCookies(t *testing.T) {
	jar := newJar(old("jobs.example.test", "gone"), old(".jobs.example.test", "gone2"), old("example.test", "parent"), old("other.example.org", "other"), old("notjobs.example.test", "lookalike"))
	s, _ := newSessions(t, jar)
	res, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "a")})
	if err != nil || res.Replaced != 2 || res.Imported != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, host := range []string{"example.test", "other.example.org", "notjobs.example.test"} {
		if len(jar.names(host)) == 0 {
			t.Errorf("cookies of %s were deleted", host)
		}
	}
	if fmt.Sprint(jar.names("jobs.example.test")) != "[a]" {
		t.Fatalf("%v", jar.names("jobs.example.test"))
	}
}

func TestSiteNotSignedInAtTheSourceIsLeftUntouched(t *testing.T) {
	jar := newJar(old("jobs.example.test", "j"), old("careers.example.org", "kept"))
	s, dir := newSessions(t, jar)
	res, err := s.Import(context.Background(), []string{"jobs.example.test", "careers.example.org"}, []wire.SessionCookie{wc("jobs.example.test", "a")})
	if err != nil || res.NotInSource != 1 || res.Imported != 1 || res.Replaced != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if fmt.Sprint(jar.names("careers.example.org")) != "[kept]" {
		t.Fatal("the other domain's cookies are kept")
	}
	nothing, err := s.Import(context.Background(), []string{"careers.example.org"}, nil)
	if err != nil || nothing.NotInSource != 1 || nothing.Imported != 0 {
		t.Fatalf("%+v %v", nothing, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, snapshotFile))
	if !strings.Contains(string(data), `"jobs.example.test"`) {
		t.Fatal("an import that changed nothing keeps the earlier snapshot")
	}
}

func TestSiteWithSignInCookiesNeedsOneOfThem(t *testing.T) {
	jar := newJar(old("www.linkedin.com", "JSESSIONID"))
	s, _ := newSessions(t, jar)
	res, err := s.Import(context.Background(), []string{"linkedin.com"}, []wire.SessionCookie{wc(".linkedin.com", "bcookie"), wc(".linkedin.com", "lang")})
	if err != nil || res.NotInSource != 1 || res.Imported != 0 || fmt.Sprint(jar.names("linkedin.com")) != "[JSESSIONID]" {
		t.Fatalf("cookies without li_at do not show a sign-in: %+v %v", res, err)
	}
	res, err = s.Import(context.Background(), []string{"linkedin.com"}, []wire.SessionCookie{wc(".linkedin.com", "li_at"), wc(".linkedin.com", "lang")})
	if err != nil || res.Imported != 2 || res.NotInSource != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUndoRestoresAndDeletesTheSnapshot(t *testing.T) {
	before := []Cookie{old("jobs.example.test", "o1"), old(".jobs.example.test", "o2")}
	jar := newJar(append(before, old("other.example.org", "x"))...)
	s, dir := newSessions(t, jar)
	if _, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "n1"), wc("jobs.example.test", "n2")}); err != nil {
		t.Fatal(err)
	}
	if !s.UndoAvailable() {
		t.Fatal("an import leaves a snapshot")
	}
	res, err := s.Undo(context.Background())
	if err != nil || res.Restored != 2 || res.Failed != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if fmt.Sprint(jar.names("jobs.example.test")) != "[o1 o2]" || len(jar.names("other.example.org")) != 1 {
		t.Fatalf("%v", jar.names("jobs.example.test"))
	}
	for _, c := range before {
		if got := jar.cookies[c.key()]; got.Value != c.Value || got.Expires != c.Expires {
			t.Errorf("%s restored as %+v", c.Name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, snapshotFile)); !os.IsNotExist(err) || s.UndoAvailable() {
		t.Fatal("the snapshot is deleted once everything is restored")
	}
	if _, err := s.Undo(context.Background()); !errors.Is(err, &Error{Code: CodeNoSnapshot}) {
		t.Fatalf("a second undo has nothing to restore: %v", err)
	}
}

func TestUndoOfAnImportIntoAnEmptyProfileRemovesTheImportedCookies(t *testing.T) {
	jar := newJar()
	s, _ := newSessions(t, jar)
	if _, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "n1")}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Undo(context.Background())
	if err != nil || res.Restored != 0 || res.Failed != 0 || len(jar.cookies) != 0 {
		t.Fatalf("%+v %v %d", res, err, len(jar.cookies))
	}
}

func TestPartialUndoKeepsTheSnapshotAndCanBeRepeated(t *testing.T) {
	jar := newJar(old("jobs.example.test", "o1"), old("jobs.example.test", "o2"))
	s, dir := newSessions(t, jar)
	if _, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "n1")}); err != nil {
		t.Fatal(err)
	}
	jar.refuseSet = func(c Cookie) bool { return c.Name == "o2" }
	res, err := s.Undo(context.Background())
	if err != nil || res.Restored != 1 || res.Failed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, snapshotFile)); err != nil {
		t.Fatal("a partial undo keeps the snapshot")
	}
	jar.refuseSet = nil
	res, err = s.Undo(context.Background())
	if err != nil || res.Restored != 2 || res.Failed != 0 || fmt.Sprint(jar.names("jobs.example.test")) != "[o1 o2]" || s.UndoAvailable() {
		t.Fatalf("repeated: %+v %v", res, err)
	}
}

func TestRefusedDeletionRestoresTheProfile(t *testing.T) {
	jar := newJar(old("jobs.example.test", "o1"), old("jobs.example.test", "o2"), old("jobs.example.test", "o3"))
	jar.refuseDelete = func(c Cookie) bool { return c.Name == "o3" }
	s, _ := newSessions(t, jar)
	_, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "n1")})
	if code, _ := CodeOf(err); code != CodeReplaceFailed {
		t.Fatalf("%v", err)
	}
	if fmt.Sprint(jar.names("jobs.example.test")) != "[o1 o2 o3]" {
		t.Fatalf("the cookies are back: %v", jar.names("jobs.example.test"))
	}
}

func TestOutOfScopeCookieRefusesTheWholePayload(t *testing.T) {
	jar := newJar(old("jobs.example.test", "keep"))
	s, dir := newSessions(t, jar)
	for name, bad := range map[string]wire.SessionCookie{
		"another site": wc("other.example.test", "b"),
		"the parent":   wc("example.test", "b"),
		"a lookalike":  wc("notjobs.example.test", "b"),
	} {
		_, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "a"), bad})
		if code, _ := CodeOf(err); code != CodeCookieOutOfScope || strings.Contains(err.Error(), secretValue) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if jar.mutations != 0 || fmt.Sprint(jar.names("jobs.example.test")) != "[keep]" {
		t.Fatal("nothing changes")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("no snapshot is written for a refused payload")
	}
}

func TestSnapshotIsDeletedAfterSevenDays(t *testing.T) {
	jar := newJar(old("jobs.example.test", "o1"))
	s, dir := newSessions(t, jar)
	if _, err := s.Import(context.Background(), []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "n1")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, snapshotFile)
	written := time.Now().Add(-6 * 24 * time.Hour)
	_ = os.Chtimes(path, written, written)
	if !s.UndoAvailable() {
		t.Fatal("six days old is still undoable")
	}
	written = time.Now().Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(path, written, written)
	if s.UndoAvailable() {
		t.Fatal("eight days old is gone")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the file is deleted")
	}
	if _, err := s.Undo(context.Background()); !errors.Is(err, &Error{Code: CodeNoSnapshot}) {
		t.Fatalf("%v", err)
	}
}

func TestACancelledRequestStillFinishesTheReplacement(t *testing.T) {
	jar := newJar(old("jobs.example.test", "o1"), old("jobs.example.test", "o2"))
	ctx, cancel := context.WithCancel(context.Background())
	jar.onDelete = cancel
	s, _ := newSessions(t, jar)
	res, err := s.Import(ctx, []string{"jobs.example.test"}, []wire.SessionCookie{wc("jobs.example.test", "n1")})
	if err != nil || res.Imported != 1 || fmt.Sprint(jar.names("jobs.example.test")) != "[n1]" {
		t.Fatalf("%+v %v %v", res, err, jar.names("jobs.example.test"))
	}
}

func TestCookiesNeverPrintTheirValues(t *testing.T) {
	c := Cookie{Name: "sid", Value: secretValue, Domain: ".example.test"}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c), fmt.Sprint([]Cookie{c}), fmt.Sprint(&c)} {
		if strings.Contains(s, secretValue) || strings.Contains(s, "sid") {
			t.Fatalf("%s", s)
		}
	}
}

func TestSitesListProbeStoredAndProfileHostsWithoutStartingTheBrowser(t *testing.T) {
	jar := newJar(old(".linkedin.com", "li_at"), old("jobs.example.test", "a"), old("jobs.example.test", "b"), old("acme.wd5.myworkdayjobs.com", "wd"))
	opens := 0
	s := NewSessions(SessionsConfig{
		Open:    func(context.Context) (CookieJar, error) { opens++; return jar, nil },
		Running: func() CookieJar { return jar },
		Accounts: func(context.Context) ([]StoredAccount, error) {
			return []StoredAccount{{Host: "Careers.Example.org"}, {System: "workday", Tenant: "acme"}, {System: "workday", Tenant: "later"}}, nil
		},
	})
	sites, err := s.Sites(context.Background())
	if err != nil || opens != 0 {
		t.Fatalf("%v opens=%d", err, opens)
	}
	by := map[string]SessionSite{}
	var tenantsWithoutHost []string
	for _, site := range sites {
		if site.Domain == "" {
			tenantsWithoutHost = append(tenantsWithoutHost, site.Label)
		}
		by[site.Domain] = site
		if site.SignIn != "unknown" {
			t.Errorf("%+v", site)
		}
	}
	for _, d := range []string{"linkedin.com", "indeed.com", "jobright.ai", "careers.example.org", "jobs.example.test", "acme.wd5.myworkdayjobs.com"} {
		if _, ok := by[d]; !ok {
			t.Errorf("%s is missing from %v", d, sites)
		}
	}
	if by["jobs.example.test"].Cookies != 2 || by["linkedin.com"].Cookies != 1 || by["indeed.com"].Cookies != 0 {
		t.Errorf("managed cookie counts: %+v", sites)
	}
	if by["acme.wd5.myworkdayjobs.com"].Label != "Workday acme" || fmt.Sprint(tenantsWithoutHost) != "[Workday later]" {
		t.Errorf("a tenant resolves from the profile's cookie domains; an unknown one has no domain: %+v", sites)
	}
}

func TestSitesWithoutARunningBrowserOrWithAnUnreadableOne(t *testing.T) {
	s := NewSessions(SessionsConfig{Running: func() CookieJar { return nil }})
	sites, err := s.Sites(context.Background())
	if err != nil || len(sites) != len(SignInSites) {
		t.Fatalf("%v %v", sites, err)
	}
	for _, site := range sites {
		if site.Cookies != 0 {
			t.Errorf("%+v", site)
		}
	}
	boom := errors.New("stored accounts unreadable")
	if _, err := NewSessions(SessionsConfig{Accounts: func(context.Context) ([]StoredAccount, error) { return nil, boom }}).Sites(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}
