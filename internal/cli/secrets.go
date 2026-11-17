package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

	"applysmith/internal/credentials"
	"applysmith/internal/llm"
	"applysmith/internal/onboarding"
	"applysmith/internal/vault"
)

const secretsUsage = `usage:
  applysmith secrets list [--json]
  applysmith secrets set <name> [--email <address>] [--label <text>]   the value is read from standard input or a hidden prompt
  applysmith secrets delete <name>
  applysmith secrets test <name>

every command takes --workspace <dir>
`

const credentialsUsage = `usage:
  applysmith credentials list [--json]
  applysmith credentials set-default [--email <address>] [--password-stdin]
  applysmith credentials add-alternate [--label <text>] --password-stdin
  applysmith credentials set-site <host> [--email <address>] --password-stdin
  applysmith credentials remove-site <host>
  applysmith credentials remove-alternate <id>
  applysmith credentials reorder <id>...

a password is read from standard input or a hidden prompt, never from an argument
every command takes --workspace <dir>
`

const valueArgRefused = "a secret value must not be passed as a command-line argument, because arguments appear in process listings and shell history; pipe it on standard input (--password-stdin where a command has the flag) or type it at the hidden prompt"

func (e *env) secrets(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, secretsUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"list": e.secretsList, "set": e.secretsSet, "delete": e.secretsDelete, "test": e.secretsTest,
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, secretsUsage)
	return 2
}

func (e *env) credentialsCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, credentialsUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"list": e.credentialsList, "set-default": e.credentialsSetDefault, "add-alternate": e.credentialsAddAlternate,
		"set-site": e.credentialsSetSite, "remove-site": e.credentialsRemoveSite, "remove-alternate": e.credentialsRemoveAlternate,
		"reorder": e.credentialsReorder,
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, credentialsUsage)
	return 2
}

// credFail maps a management failure onto the exit codes: input the user can
// fix is 2, a keychain that cannot be reached is 4, anything else 1.
func (e *env) credFail(cmd string, err error) int {
	var weak *credentials.WeakPasswordError
	var ve *vault.Error
	switch {
	case errors.As(err, &weak), errors.Is(err, credentials.ErrEmailRequired), errors.Is(err, credentials.ErrBadEmail),
		errors.Is(err, credentials.ErrNothingToSet), errors.Is(err, credentials.ErrLabelTooLong), errors.Is(err, credentials.ErrBadOrder),
		errors.Is(err, credentials.ErrBadSite), errors.Is(err, credentials.ErrUseAddAlternate):
		return e.report(cmd, &onboarding.RejectedError{Message: err.Error()})
	case errors.As(err, &ve):
		switch ve.Code {
		case vault.CodeNameInvalid:
			return e.report(cmd, &onboarding.RejectedError{Message: fmt.Sprintf("%q: %s; `applysmith secrets list` prints the registry", ve.Name, ve.Message)})
		case vault.CodeEmpty, vault.CodeTooLarge:
			return e.report(cmd, &onboarding.RejectedError{Message: ve.Message})
		case vault.CodeKeychainDenied, vault.CodeKeychainUnavailable:
			return e.report(cmd, &onboarding.EnvError{Message: ve.Message})
		}
		return e.fail(cmd, errors.New(ve.Message))
	}
	return e.report(cmd, &onboarding.EnvError{Message: "the credential index could not be read or written", Err: err})
}

// service builds the management service of an open workspace.
func (e *env) service(cmd string, oe *onboarding.Env) (*credentials.Service, bool) {
	if oe.Store == nil || oe.Vault == nil {
		e.report(cmd, &onboarding.EnvError{Message: "the workspace database does not exist yet; run `applysmith setup` or `applysmith init` first"})
		return nil, false
	}
	return credentials.New(oe.Store, oe.Vault, oe.Now), true
}

// readValue reads the one value a command takes from standard input or the
// hidden prompt. An empty value is rejected.
func (e *env) readValue(cmd, prompt string) (string, int) {
	v, err := e.readSecret(prompt)
	if err != nil {
		return "", e.report(cmd, &onboarding.EnvError{Message: "the value could not be read", Err: err})
	}
	if v == "" {
		return "", e.report(cmd, &onboarding.RejectedError{Message: "the value is empty"})
	}
	return v, 0
}

func yesNo(b bool) string {
	if b {
		return "present"
	}
	return "absent"
}

func (e *env) secretsList(ctx context.Context, args []string) int {
	fs := e.flags("secrets list")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withRead(ctx, "secrets list", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("secrets list", oe)
		if !ok {
			return 4
		}
		entries, err := svc.Entries(ctx)
		if err != nil {
			return e.credFail("secrets list", err)
		}
		if *asJSON {
			return e.printJSON(entryDocs(entries, nil))
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tKIND\tLABEL\tSTATE\tSOURCE")
		for _, en := range entries {
			state := yesNo(en.Present)
			if en.Problem != "" {
				state = en.Problem
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", en.Name, en.Kind, en.Label, state, en.Source)
		}
		tw.Flush()
		return 0
	})
}

func (e *env) secretsSet(ctx context.Context, args []string) int {
	fs := e.flags("secrets set")
	wsFlag := fs.String("workspace", "", "workspace directory")
	email := fs.String("email", "", "registration email for a sign-in entry")
	label := fs.String("label", "", "a name for the entry")
	value := fs.String("value", "", "refused: a value is never accepted as an argument")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) < 1 {
		return 2
	}
	if len(pos) > 1 || *value != "" {
		fmt.Fprintf(e.stderr, "applysmith secrets set: %s\n", valueArgRefused)
		return 2
	}
	name := pos[0]
	row, lerr := vault.Lookup(name)
	if lerr != nil {
		return e.credFail("secrets set", lerr)
	}
	return e.withWrite(ctx, "secrets set", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("secrets set", oe)
		if !ok {
			return 4
		}
		v, code := e.readValue("secrets set", name)
		if code != 0 {
			return code
		}
		if row.Kind == "api_key" {
			if err := llm.ValidateAPIKey(v); err != nil {
				return e.report("secrets set", &onboarding.RejectedError{Message: err.Error()})
			}
		}
		res, err := svc.Set(ctx, name, v, *email, *label)
		if err != nil {
			return e.credFail("secrets set", err)
		}
		fmt.Fprintf(e.stdout, "stored %s (present, not shown)\n", name)
		if res.ShadowedBy != "" {
			fmt.Fprintf(e.stdout, "note: %s is set in the environment and is used instead of the stored value\n", res.ShadowedBy)
		}
		return 0
	})
}

func (e *env) secretsDelete(ctx context.Context, args []string) int {
	fs := e.flags("secrets delete")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		return 2
	}
	return e.withWrite(ctx, "secrets delete", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("secrets delete", oe)
		if !ok {
			return 4
		}
		if err := svc.Delete(ctx, pos[0]); err != nil {
			return e.credFail("secrets delete", err)
		}
		fmt.Fprintf(e.stdout, "removed %s\n", pos[0])
		return 0
	})
}

// secretsTest says whether the secret can be read. No kind has a live provider
// check yet, so a readable secret reports `unsupported` for the live part.
func (e *env) secretsTest(ctx context.Context, args []string) int {
	fs := e.flags("secrets test")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		return 2
	}
	return e.withRead(ctx, "secrets test", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("secrets test", oe)
		if !ok {
			return 4
		}
		src, err := svc.Test(pos[0])
		switch {
		case err == nil:
			fmt.Fprintf(e.stdout, "%s: present (source %s); live check: unsupported\n", pos[0], src)
			return 0
		case vault.IsCode(err, vault.CodeNotFound):
			fmt.Fprintf(e.stdout, "%s: not_found\n", pos[0])
			return 1
		}
		return e.credFail("secrets test", err)
	})
}

// entryDoc is an entry as `credentials list --json` prints it: never a value.
type entryDoc struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Label    string `json:"label,omitempty"`
	Host     string `json:"host,omitempty"`
	System   string `json:"system,omitempty"`
	Tenant   string `json:"tenant,omitempty"`
	Email    string `json:"email,omitempty"`
	Position int    `json:"position,omitempty"`
	Present  bool   `json:"present"`
	Source   string `json:"source,omitempty"`
	Problem  string `json:"problem,omitempty"`
}

func entryDocs(entries []credentials.Entry, keep func(credentials.Entry) bool) []entryDoc {
	out := []entryDoc{}
	for _, en := range entries {
		if keep != nil && !keep(en) {
			continue
		}
		out = append(out, entryDoc{en.Name, en.Kind, en.Label, en.Host, en.System, en.Tenant, en.Email, en.Position, en.Present, string(en.Source), en.Problem})
	}
	return out
}

func signinKind(en credentials.Entry) bool {
	switch en.Kind {
	case credentials.KindDefault, credentials.KindAlternate, credentials.KindSite, credentials.KindTenant:
		return true
	}
	return false
}

func (e *env) credentialsList(ctx context.Context, args []string) int {
	fs := e.flags("credentials list")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withRead(ctx, "credentials list", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials list", oe)
		if !ok {
			return 4
		}
		entries, err := svc.Entries(ctx)
		if err != nil {
			return e.credFail("credentials list", err)
		}
		if *asJSON {
			return e.printJSON(entryDocs(entries, signinKind))
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tLABEL\tWHERE\tEMAIL\tORDER\tSECRET")
		for _, en := range entries {
			if !signinKind(en) {
				continue
			}
			where := en.Host
			if en.Tenant != "" {
				where = en.System + "/" + en.Tenant
			}
			order := ""
			if en.Position > 0 {
				order = fmt.Sprint(en.Position)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", en.Kind, en.Label, where, en.Email, order, yesNo(en.Present))
		}
		tw.Flush()
		return 0
	})
}

// passwordFlags are the flags shared by the commands that take a password.
// --password is declared only to be refused.
type passwordFlags struct {
	stdin                            *bool
	workspace, refused, email, label *string
}

func (e *env) credentialFlags(name string, withLabel bool) (*flag.FlagSet, *passwordFlags) {
	fs := e.flags("credentials " + name)
	p := &passwordFlags{
		workspace: fs.String("workspace", "", "workspace directory"),
		stdin:     fs.Bool("password-stdin", false, "read the password from standard input or a hidden prompt"),
		refused:   fs.String("password", "", "refused: a password is never accepted as an argument"),
	}
	// An alternate has a label; the default and a site entry have an email.
	if withLabel {
		p.label = fs.String("label", "", "a name for the entry")
	} else {
		p.email = fs.String("email", "", "registration email")
	}
	return fs, p
}

func (e *env) credentialsSetDefault(ctx context.Context, args []string) int {
	fs, p := e.credentialFlags("set-default", false)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) > 0 || *p.refused != "" {
		fmt.Fprintf(e.stderr, "applysmith credentials set-default: %s\n", valueArgRefused)
		return 2
	}
	return e.withWrite(ctx, "credentials set-default", *p.workspace, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials set-default", oe)
		if !ok {
			return 4
		}
		pw := ""
		if *p.stdin {
			v, code := e.readValue("credentials set-default", "shared default password")
			if code != 0 {
				return code
			}
			pw = v
		}
		if _, err := svc.SetDefault(ctx, *p.email, pw); err != nil {
			return e.credFail("credentials set-default", err)
		}
		if pw == "" {
			fmt.Fprintln(e.stdout, "recorded the shared default's email; no password was changed")
		} else {
			fmt.Fprintln(e.stdout, "stored the shared default (present, not shown)")
		}
		return 0
	})
}

func (e *env) credentialsAddAlternate(ctx context.Context, args []string) int {
	fs, p := e.credentialFlags("add-alternate", true)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) > 0 || *p.refused != "" {
		fmt.Fprintf(e.stderr, "applysmith credentials add-alternate: %s\n", valueArgRefused)
		return 2
	}
	if !*p.stdin {
		fmt.Fprint(e.stderr, "applysmith credentials add-alternate: give --password-stdin; the password is read from standard input or a hidden prompt\n")
		return 2
	}
	return e.withWrite(ctx, "credentials add-alternate", *p.workspace, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials add-alternate", oe)
		if !ok {
			return 4
		}
		v, code := e.readValue("credentials add-alternate", "alternate password")
		if code != 0 {
			return code
		}
		id, err := svc.AddAlternate(ctx, *p.label, v)
		if err != nil {
			return e.credFail("credentials add-alternate", err)
		}
		fmt.Fprintf(e.stdout, "added the alternate %s (present, not shown)\n", id)
		return 0
	})
}

func (e *env) credentialsSetSite(ctx context.Context, args []string) int {
	fs, p := e.credentialFlags("set-site", false)
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) < 1 {
		return 2
	}
	if len(pos) > 1 || *p.refused != "" {
		fmt.Fprintf(e.stderr, "applysmith credentials set-site: %s\n", valueArgRefused)
		return 2
	}
	if !*p.stdin {
		fmt.Fprint(e.stderr, "applysmith credentials set-site: give --password-stdin; the password is read from standard input or a hidden prompt\n")
		return 2
	}
	return e.withWrite(ctx, "credentials set-site", *p.workspace, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials set-site", oe)
		if !ok {
			return 4
		}
		host, err := credentials.SiteKey(pos[0])
		if err != nil {
			return e.credFail("credentials set-site", err)
		}
		v, code := e.readValue("credentials set-site", "password for "+host)
		if code != 0 {
			return code
		}
		if _, err := svc.SetSite(ctx, host, *p.email, v); err != nil {
			return e.credFail("credentials set-site", err)
		}
		fmt.Fprintf(e.stdout, "stored the password of %s (present, not shown)\n", host)
		return 0
	})
}

func (e *env) credentialsRemoveSite(ctx context.Context, args []string) int {
	fs := e.flags("credentials remove-site")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		return 2
	}
	return e.withWrite(ctx, "credentials remove-site", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials remove-site", oe)
		if !ok {
			return 4
		}
		host, err := credentials.SiteKey(pos[0])
		if err != nil {
			return e.credFail("credentials remove-site", err)
		}
		if err := svc.Delete(ctx, "signin-site-"+host); err != nil {
			return e.credFail("credentials remove-site", err)
		}
		fmt.Fprintf(e.stdout, "removed the password of %s\n", host)
		return 0
	})
}

func (e *env) credentialsRemoveAlternate(ctx context.Context, args []string) int {
	fs := e.flags("credentials remove-alternate")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		return 2
	}
	return e.withWrite(ctx, "credentials remove-alternate", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials remove-alternate", oe)
		if !ok {
			return 4
		}
		if err := svc.Delete(ctx, "signin-alternate-"+strings.TrimSpace(pos[0])); err != nil {
			return e.credFail("credentials remove-alternate", err)
		}
		fmt.Fprintf(e.stdout, "removed the alternate %s\n", pos[0])
		return 0
	})
}

func (e *env) credentialsReorder(ctx context.Context, args []string) int {
	fs := e.flags("credentials reorder")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) < 1 {
		return 2
	}
	return e.withWrite(ctx, "credentials reorder", *wsFlag, func(oe *onboarding.Env) int {
		svc, ok := e.service("credentials reorder", oe)
		if !ok {
			return 4
		}
		if err := svc.ReorderAlternates(ctx, pos); err != nil {
			return e.credFail("credentials reorder", err)
		}
		fmt.Fprintf(e.stdout, "the alternates are now tried in the order %s\n", strings.Join(pos, ", "))
		return 0
	})
}
