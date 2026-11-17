package kb

import (
	"errors"
	"strings"
	"testing"
)

// Credential-shaped fixtures are assembled at run time so no literal in the
// repository looks like a secret.
func fakeCreds() map[string]string {
	rnd := "q7Zr4TbX9mLw2KdVn8Ys3HcFa5JgP6Ue"
	return map[string]string{
		"private key":       "-----" + "BEGIN RSA PRIVATE" + " KEY-----\nMIIB" + rnd + "\n-----" + "END RSA PRIVATE KEY-----",
		"api key prefix":    "my key is sk" + "-" + rnd,
		"github token":      "token ghp" + "_" + rnd[:30],
		"cloud access key":  "AKIA" + strings.ToUpper(rnd[:16]),
		"bearer":            "Authorization: Bearer " + rnd + "." + rnd[:8],
		"connection string": "postgres" + "://admin:" + "hunter2pass" + "@db.example.com/prod",
		"assignment":        "api_key = \"" + rnd + "\"",
		"password assign":   "password: " + rnd[:20] + "9",
	}
}

func TestSecrets_ScanEveryWrite_NoEcho(t *testing.T) {
	for name, text := range fakeCreds() {
		t.Run(name, func(t *testing.T) {
			err := CheckSecrets("knowledge/projects/x.md", "line one\n"+text)
			if !errors.Is(err, ErrSecret) {
				t.Fatalf("not rejected: %v", err)
			}
			var se *SecretError
			if !errors.As(err, &se) || se.Findings[0].Line < 2 {
				t.Fatalf("finding = %+v", se)
			}
			for _, tok := range strings.Fields(text) {
				if len(tok) > 12 && strings.Contains(err.Error(), tok) {
					t.Fatalf("the error echoes the value: %v", err)
				}
			}
			if !strings.Contains(err.Error(), "knowledge/projects/x.md:") {
				t.Fatalf("no path and line: %v", err)
			}
		})
	}
}

func TestSecrets_ProseAboutPasswordsIsFine(t *testing.T) {
	for _, text := range []string{
		"I rotated the shared password after the incident.",
		"We stored the token in the vault and documented the secret rotation policy.",
		"password: changeme",
		"Reset the api key every quarter.",
		"token_bucket = limiter-per-tenant",
	} {
		if err := CheckSecrets("x.md", text); err != nil {
			t.Errorf("%q rejected: %v", text, err)
		}
	}
}

func TestSecrets_PastedKeyRejectedOnEveryWritePath(t *testing.T) {
	f := newFixture(t)
	bad := snippet("title: Notes", "Notes.\n"+fakeCreds()["private key"])
	if _, err := f.svc.CreateDraft(f.ctx, NewDraft{Markdown: bad, SnippetID: "notes"}); !errors.Is(err, ErrSecret) {
		t.Fatalf("draft: %v", err)
	}
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "notes.md", Data: []byte(bad)}); !errors.Is(err, ErrSecret) {
		t.Fatalf("import: %v", err)
	}
	// A file that already holds a secret is not indexed.
	f.write("knowledge/projects/leak.md", snippet("", "Harbor notes.\n"+fakeCreds()["api key prefix"]))
	rep, _ := f.svc.Sync(f.ctx)
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "knowledge/projects/leak.md:") {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	if r := f.search("harbor notes"); len(r.Facts) != 0 {
		t.Fatalf("a secret-bearing file is searchable: %v", ids(r))
	}
	// And an edit.
	f.write("knowledge/projects/ok.md", snippet("", "Plain harbor text."))
	d, _ := f.svc.Show(f.ctx, "ok")
	if _, err := f.svc.Edit(f.ctx, "ok", d.Markdown+"\n"+fakeCreds()["github token"], d.FileHash); !errors.Is(err, ErrSecret) {
		t.Fatalf("edit: %v", err)
	}
	if strings.Contains(f.read("knowledge/projects/ok.md"), "ghp") {
		t.Fatal("the edit reached the file")
	}
}

func TestSecrets_KeychainIsolation(t *testing.T) {
	f := newFixture(t)
	key := "sk" + "-" + "q7Zr4TbX9mLw2KdVn8Ys3HcFa5JgP6Ue"
	// A key pasted into a fact source is refused from the index, so querying
	// for its text finds nothing.
	f.write("narrative.md", "My provider key is "+key+" for the evaluation work.")
	f.write("cv.md", "## Skills\nProvider evaluation work.")
	r := f.search(key)
	for _, fact := range r.Facts {
		if strings.Contains(fact.Text, key) {
			t.Fatalf("the key came back: %+v", fact)
		}
	}
}
