package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	cliDefaultPw = "Cli-Default-Pw-1!x"
	cliAltPw     = "Cli-Alt-Canary-2"
	cliSitePw    = "Cli-Site-Canary-3"
)

func (te *testEnv) pipe(value string, args ...string) output {
	te.t.Helper()
	te.stdin = value + "\n"
	defer func() { te.stdin = "" }()
	return te.run(context.Background(), append(args, "--workspace", te.ws)...)
}

func TestCLI_Secrets_ValueFromAPipe(t *testing.T) {
	te := newInitTestEnv(t)
	out := te.pipe(testKey(), "secrets", "set", "api-key-openrouter")
	if out.code != 0 || strings.Contains(out.stdout+out.stderr, testKey()) {
		t.Fatalf("%+v", out)
	}
	accounts := te.backend.Accounts()
	found := false
	for _, a := range accounts {
		found = found || strings.HasSuffix(a, ":api-key-openrouter")
	}
	if !found {
		t.Fatalf("accounts %v", accounts)
	}
	list := te.run(context.Background(), "secrets", "list", "--workspace", te.ws)
	if list.code != 0 || !strings.Contains(list.stdout, "api-key-openrouter") || strings.Contains(list.stdout, testKey()) {
		t.Fatalf("%+v", list)
	}
	if out := te.pipe("", "secrets", "set", "cloudflared-tunnel-token"); out.code != 2 || !strings.Contains(out.stderr, "empty") {
		t.Fatalf("an empty value: %+v", out)
	}
}

func TestCLI_Secrets_ValueNeverAsArgument(t *testing.T) {
	te := newInitTestEnv(t)
	before := len(te.backend.Accounts())
	for name, args := range map[string][]string{
		"second positional":    {"secrets", "set", "api-key-openrouter", testKey()},
		"value flag":           {"secrets", "set", "api-key-openrouter", "--value", testKey()},
		"password flag":        {"credentials", "set-default", "--email", "alex@example.com", "--password", cliDefaultPw},
		"alternate positional": {"credentials", "add-alternate", cliAltPw, "--password-stdin"},
		"site positional":      {"credentials", "set-site", "careers.example.test", cliSitePw, "--password-stdin"},
	} {
		t.Run(name, func(t *testing.T) {
			out := te.run(context.Background(), append(args, "--workspace", te.ws)...)
			all := out.stdout + out.stderr
			if out.code != 2 || !strings.Contains(all, "standard input") {
				t.Fatalf("%+v", out)
			}
			for _, v := range []string{testKey(), cliDefaultPw, cliAltPw, cliSitePw} {
				if strings.Contains(all, v) {
					t.Fatalf("the refusal quotes the value: %s", all)
				}
			}
		})
	}
	if len(te.backend.Accounts()) != before {
		t.Fatalf("a refused command stored %v", te.backend.Accounts())
	}
}

func TestCLI_Credentials_ListShowsNoPassword(t *testing.T) {
	te := newInitTestEnv(t)
	if out := te.run(context.Background(), "credentials", "set-default", "--email", "alex@example.com", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("email only: %+v", out)
	}
	if out := te.pipe(cliDefaultPw, "credentials", "set-default", "--password-stdin"); out.code != 0 {
		t.Fatalf("default: %+v", out)
	}
	for _, label := range []string{"Old laptop", ""} {
		args := []string{"credentials", "add-alternate", "--password-stdin"}
		if label != "" {
			args = append(args, "--label", label)
		}
		if out := te.pipe(cliAltPw+label, args...); out.code != 0 {
			t.Fatalf("alternate: %+v", out)
		}
	}
	if out := te.pipe(cliSitePw, "credentials", "set-site", "https://Careers.Example.Test/jobs/1", "--password-stdin"); out.code != 0 {
		t.Fatalf("site: %+v", out)
	}
	out := te.run(context.Background(), "credentials", "list", "--json", "--workspace", te.ws)
	if out.code != 0 {
		t.Fatalf("%+v", out)
	}
	for _, v := range []string{cliDefaultPw, cliAltPw, cliSitePw} {
		if strings.Contains(out.stdout, v) {
			t.Fatalf("the list carries a password: %s", out.stdout)
		}
	}
	var rows []struct {
		Kind, Label, Host, Email string
		Position                 int
		Present                  bool
	}
	if err := json.Unmarshal([]byte(out.stdout), &rows); err != nil {
		t.Fatalf("%v: %s", err, out.stdout)
	}
	var alt, site, def int
	for _, r := range rows {
		switch r.Kind {
		case "signin_alternate":
			alt++
			if !r.Present || r.Position != alt || (alt == 1 && r.Label != "Old laptop") || (alt == 2 && r.Label != "Alternate 2") {
				t.Errorf("alternate = %+v", r)
			}
		case "signin_site":
			site++
			if r.Host != "careers.example.test" || !r.Present {
				t.Errorf("site = %+v", r)
			}
		case "signin_default":
			def++
			if r.Email != "alex@example.com" || !r.Present {
				t.Errorf("default = %+v", r)
			}
		}
	}
	if alt != 2 || site != 1 || def != 1 {
		t.Fatalf("rows %+v", rows)
	}
	text := te.run(context.Background(), "credentials", "list", "--workspace", te.ws)
	if text.code != 0 || !strings.Contains(text.stdout, "careers.example.test") || strings.Contains(text.stdout, cliSitePw) {
		t.Fatalf("%+v", text)
	}
}

func TestCLI_Credentials_RulesAndOrder(t *testing.T) {
	te := newInitTestEnv(t)
	if out := te.pipe(cliAltPw, "credentials", "add-alternate", "--password-stdin"); out.code != 2 || !strings.Contains(out.stderr, "email") {
		t.Fatalf("an alternate before any email: %+v", out)
	}
	if out := te.pipe("password1", "credentials", "set-default", "--email", "alex@example.com", "--password-stdin"); out.code != 2 || !strings.Contains(out.stderr, "an uppercase letter") || strings.Contains(out.stderr, "password1") {
		t.Fatalf("a weak default: %+v", out)
	}
	if out := te.run(context.Background(), "credentials", "add-alternate", "--workspace", te.ws); out.code != 2 {
		t.Fatalf("without --password-stdin: %+v", out)
	}
	te.run(context.Background(), "credentials", "set-default", "--email", "alex@example.com", "--workspace", te.ws)
	te.pipe("first-"+cliAltPw, "credentials", "add-alternate", "--label", "A", "--password-stdin")
	te.pipe("second-"+cliAltPw, "credentials", "add-alternate", "--label", "B", "--password-stdin")
	ids := func() []string {
		out := te.run(context.Background(), "credentials", "list", "--json", "--workspace", te.ws)
		var rows []struct{ Name, Label, Kind string }
		_ = json.Unmarshal([]byte(out.stdout), &rows)
		var got []string
		for _, r := range rows {
			if r.Kind == "signin_alternate" {
				got = append(got, strings.TrimPrefix(r.Name, "signin-alternate-")+"="+r.Label)
			}
		}
		return got
	}
	before := ids()
	if len(before) != 2 {
		t.Fatalf("alternates %v", before)
	}
	idA, _, _ := strings.Cut(before[0], "=")
	idB, _, _ := strings.Cut(before[1], "=")
	if out := te.run(context.Background(), "credentials", "reorder", idB, idA, "--workspace", te.ws); out.code != 0 {
		t.Fatalf("%+v", out)
	}
	if got := ids(); got[0] != idB+"=B" || got[1] != idA+"=A" {
		t.Fatalf("after reorder %v", got)
	}
	if out := te.run(context.Background(), "credentials", "reorder", idA, "--workspace", te.ws); out.code != 2 {
		t.Fatalf("a partial order: %+v", out)
	}
	if out := te.run(context.Background(), "credentials", "remove-alternate", idA, "--workspace", te.ws); out.code != 0 {
		t.Fatalf("%+v", out)
	}
	if got := ids(); len(got) != 1 {
		t.Fatalf("after remove %v", got)
	}
	te.pipe(cliSitePw, "credentials", "set-site", "careers.example.test", "--password-stdin")
	if out := te.run(context.Background(), "credentials", "remove-site", "careers.example.test", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("%+v", out)
	}
	if out := te.run(context.Background(), "credentials", "remove-site", "careers.example.test", "--workspace", te.ws); out.code != 1 {
		t.Fatalf("removing it twice: %+v", out)
	}
}

func TestCLI_Secrets_TestReportsPresenceAndNeverTheValue(t *testing.T) {
	te := newInitTestEnv(t)
	te.run(context.Background(), "credentials", "set-default", "--email", "alex@example.com", "--workspace", te.ws)
	te.pipe(cliDefaultPw, "credentials", "set-default", "--password-stdin")
	out := te.run(context.Background(), "secrets", "test", "signin-default", "--workspace", te.ws)
	if out.code != 0 || !strings.Contains(out.stdout, "present") || !strings.Contains(out.stdout, "unsupported") || strings.Contains(out.stdout, cliDefaultPw) {
		t.Fatalf("%+v", out)
	}
	if out := te.run(context.Background(), "secrets", "test", "github-token", "--workspace", te.ws); out.code != 1 || !strings.Contains(out.stdout, "not_found") {
		t.Fatalf("absent: %+v", out)
	}
	if out := te.run(context.Background(), "secrets", "delete", "signin-default", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("delete: %+v", out)
	}
	if out := te.run(context.Background(), "secrets", "test", "signin-default", "--workspace", te.ws); out.code != 1 {
		t.Fatalf("after delete: %+v", out)
	}
}
