package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"applysmith/internal/api"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/onboarding"
)

const resumesUsage = `usage:
  applysmith resumes add <file.pdf> [--label <text>] [--tags <a,b>] [--id <id>] [--default] [--workspace <dir>]
  applysmith resumes list [--json] [--workspace <dir>]
  applysmith resumes remove <id> [--workspace <dir>]
  applysmith resumes set-default <id> [--workspace <dir>]

add copies a PDF of at most 10 MiB into resumes/ and registers it in the roster. The first résumé is the default.
remove deletes the entry and its file; the default cannot be removed while others remain.
`

var nonSlug = lazyregexp.New(`[^a-z0-9]+`)

// resumeID derives a roster id from a file name: lowercase letters, digits and
// hyphens, at most 40 characters.
func resumeID(path string) string {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".pdf")
	id := strings.Trim(nonSlug.ReplaceAllString(base, "-"), "-")
	if len(id) > 40 {
		id = strings.Trim(id[:40], "-")
	}
	return id
}

func (e *env) resumesCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, resumesUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return e.resumesAdd(ctx, args[1:])
	case "list":
		return e.resumesList(ctx, args[1:])
	case "remove":
		return e.resumesEdit(ctx, "remove", args[1:])
	case "set-default":
		return e.resumesEdit(ctx, "set_default", args[1:])
	}
	fmt.Fprint(e.stderr, resumesUsage)
	return 2
}

func (e *env) resumesAdd(ctx context.Context, args []string) int {
	fs := e.flags("resumes add")
	wsFlag := fs.String("workspace", "", "workspace directory")
	label := fs.String("label", "", "label shown in pickers; defaults to the id")
	tags := fs.String("tags", "", "comma-separated role tags")
	id := fs.String("id", "", "roster id; defaults to the file name")
	def := fs.Bool("default", false, "make this the default résumé")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, resumesUsage)
		return 2
	}
	if *id == "" {
		*id = resumeID(pos[0])
	}
	var tagList []string
	for _, t := range strings.Split(*tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tagList = append(tagList, t)
		}
	}
	input, _ := json.Marshal(map[string]any{"add": map[string]any{"path": pos[0], "id": *id, "label": *label, "tags": tagList, "default": *def}})
	return e.applyResumes(ctx, "resumes add", *wsFlag, input)
}

// resumesEdit runs remove or set-default, which take one roster id.
func (e *env) resumesEdit(ctx context.Context, field string, args []string) int {
	cmd := "resumes " + strings.ReplaceAll(field, "_", "-")
	fs := e.flags(cmd)
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, resumesUsage)
		return 2
	}
	input, _ := json.Marshal(map[string]any{field: map[string]any{"id": pos[0]}})
	return e.applyResumes(ctx, cmd, *wsFlag, input)
}

func (e *env) applyResumes(ctx context.Context, cmd, wsFlag string, input []byte) int {
	return e.withWrite(ctx, cmd, wsFlag, func(oe *onboarding.Env) int {
		res, err := onboarding.Apply(ctx, oe, "resumes", input, false)
		if err != nil {
			return e.report(cmd, err)
		}
		fmt.Fprintf(e.stdout, "resumes: %s\n", res.Detection.Status)
		for _, n := range res.Notes {
			fmt.Fprintf(e.stdout, "  %s\n", n)
		}
		for _, r := range res.Detection.Reasons {
			fmt.Fprintf(e.stdout, "  - %s\n", r.Text)
		}
		return 0
	})
}

func (e *env) resumesList(ctx context.Context, args []string) int {
	fs := e.flags("resumes list")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseMixed(fs, args); err != nil {
		fmt.Fprint(e.stderr, resumesUsage)
		return 2
	}
	return e.withRead(ctx, "resumes list", *wsFlag, func(oe *onboarding.Env) int {
		if oe.Workspace == nil {
			return e.report("resumes list", &onboarding.EnvError{Message: "no workspace could be resolved"})
		}
		roster, err := oe.Workspace.Roster()
		if err != nil {
			return e.report("resumes list", err)
		}
		if *asJSON {
			return e.printJSON(api.WireResumes(roster))
		}
		if len(roster) == 0 {
			fmt.Fprintln(e.stdout, "no résumés; `applysmith resumes add <file.pdf>` adds one")
		}
		for _, r := range roster {
			mark := ""
			if r.Default {
				mark = " (default)"
			}
			fmt.Fprintf(e.stdout, "%s%s\t%s\t%s\t%d bytes\n", r.ID, mark, r.Label, strings.Join(r.Tags, ","), r.Size)
		}
		return 0
	})
}
