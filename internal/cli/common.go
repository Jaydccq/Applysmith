package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/width"

	"applysmith/internal/api"
	"applysmith/internal/demo"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/identity"
	"applysmith/internal/launch"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// flags builds a flag set whose errors go to stderr and are reported as a
// usage error by the caller. Its help spells flags --name, as usage texts do.
func (e *env) flags(name string) *flag.FlagSet { return e.flagsWithUsage(name, "") }

// flagsWithUsage is flags whose help starts with the command's usage text;
// without one, a command of the main usage starts with its line there.
func (e *env) flagsWithUsage(name, text string) *flag.FlagSet {
	return e.flagSet(name, text, func(string) bool { return true })
}

// flagSet is flagsWithUsage whose help lists only the flags shown accepts.
func (e *env) flagSet(name, text string, shown func(string) bool) *flag.FlagSet {
	fs := flag.NewFlagSet("applysmith "+name, flag.ContinueOnError)
	fs.SetOutput(dashed{e.stderr})
	for _, line := range strings.Split(usage, "\n") {
		if f := strings.Fields(line); text == "" && strings.HasPrefix(line, "  ") && len(f) > 1 && f[0] == name {
			text = "applysmith " + name + ": " + strings.Join(f[1:], " ") + "\n\n"
		}
	}
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprint(w, text)
		fmt.Fprintf(w, "flags of applysmith %s:\n", name)
		fs.VisitAll(func(f *flag.Flag) {
			if !shown(f.Name) {
				return
			}
			arg, help := flag.UnquoteUsage(f)
			if arg != "" {
				arg = " " + arg
			}
			if !slices.Contains([]string{"", "false", "0", "0s"}, f.DefValue) {
				help += " (default " + f.DefValue + ")"
			}
			fmt.Fprintf(w, "  --%s%s\n        %s\n", f.Name, arg, help)
		})
	}
	return fs
}

// dashed spells a flag --name in the flag package's error lines, which spell
// it -name ("flag provided but not defined: -x"), as the usage texts do.
type dashed struct{ io.Writer }

var singleDash = lazyregexp.New(`^(flag provided but not defined: |flag needs an argument: |invalid (?:boolean )?value ".*" for (?:flag )?)-(\w)`)

func (d dashed) Write(p []byte) (int, error) {
	if _, err := d.Writer.Write(singleDash.ReplaceAll(p, []byte("$1--$2"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// subFlags is the flag set of a subcommand in a group whose subcommands share
// one (inbox, blocked, leads, registry, gmail, telegram, remote). Its help shows the subcommand's usage
// lines and only the flags they name, and its parse refuses any other flag, so
// one that would do nothing here (inbox dismiss --limit) is a usage error
// instead of being dropped. --workspace is every command's, and so is each
// flag in always (a flag declared only to be refused with a reason).
func (e *env) subFlags(group, sub, text string, always ...string) (*flag.FlagSet, func([]string) ([]string, bool)) {
	cmd := group + " " + sub
	lines := usageLines(cmd, text)
	named := map[string]bool{"workspace": true}
	for _, f := range always {
		named[f] = true
	}
	for _, m := range flagWord.FindAllStringSubmatch(lines, -1) {
		named[m[1]] = true
	}
	fs := e.flagSet(cmd, lines+"\n", func(name string) bool { return named[name] })
	return fs, func(args []string) ([]string, bool) {
		pos, err := parseMixed(fs, args)
		if err != nil {
			return nil, false
		}
		var extra []string
		fs.Visit(func(f *flag.Flag) {
			if !named[f.Name] {
				extra = append(extra, "--"+f.Name)
			}
		})
		if len(extra) > 0 {
			fmt.Fprintf(e.stderr, "applysmith %s: %s does nothing here\n%s", cmd, strings.Join(extra, ", "), lines)
			return nil, false
		}
		return pos, true
	}
}

var flagWord = lazyregexp.New(`--([a-z][a-z0-9-]*)`)

// usageLines is the part of a group's usage text that shows one subcommand,
// such as "tracker add", or empty when the text does not show it.
func usageLines(cmd, text string) string {
	lines := ""
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "applysmith "+cmd); ok && (rest == "" || rest[0] == ' ') {
			lines += line + "\n"
		}
	}
	if lines == "" {
		return ""
	}
	return "usage:\n" + lines
}

// unknownSubcommand refuses the first subcommand word of args that the
// group's usage text does not list, naming the nearest one it does, else
// printing the usage. A level where the usage lists no word (only an
// argument such as an id), or below the group a single word (a value such as
// remote on tunnel, which its command explains), is not checked. A level that
// lists an argument beside its words (`applysmith evaluate <url|inbox-row-id>`
// and `applysmith evaluate gaps`) refuses only a word near one of them: any other
// word may be the argument, and its command answers for it.
func (e *env) unknownSubcommand(args []string) (int, bool) {
	text, ok := groupUsage[args[0]]
	for d := 1; ok && d < len(args) && isCommandWord(args[d]); d++ {
		group := strings.Join(args[:d], " ")
		names := subcommands(group, text)
		if len(names) < min(d, 2) || slices.Contains(names, args[d]) {
			continue
		}
		near, found := nearestCommand(args[d], names)
		if !found && takesArgument(group, text) {
			continue
		}
		fmt.Fprintf(e.stderr, "applysmith %s: unknown command %q", group, args[d])
		if found {
			fmt.Fprintf(e.stderr, "; did you mean %q?\nrun `applysmith %s --help` for its commands\n", near, args[0])
		} else {
			fmt.Fprint(e.stderr, "\n"+text)
		}
		return 2, true
	}
	return 0, false
}

// nearestCommand is the command a mistyped word most likely meant: the
// nearest by spelling, else the only one that has the word as a part of its
// hyphenated name (followup for followup-draft, pause for pause-all).
func nearestCommand(word string, names []string) (string, bool) {
	if near, ok := workspace.Closest(word, names); ok {
		return near, true
	}
	var parts []string
	for _, n := range names {
		if strings.Contains(n, "-") && slices.Contains(strings.Split(n, "-"), word) && !slices.Contains(parts, n) {
			parts = append(parts, n)
		}
	}
	if len(parts) == 1 {
		return parts[0], true
	}
	return "", false
}

// subcommands lists the words that follow "applysmith <group> " in a usage
// text, with the alternatives after " | " when they are at that level.
func subcommands(group, text string) (names []string) {
	for _, line := range strings.Split(text, "\n") {
		_, rest, ok := strings.Cut(line, "applysmith "+group+" ")
		alts := strings.Split(rest, " | ")
		head := strings.Fields(alts[0])
		if !ok || len(head) == 0 || !isCommandWord(head[0]) {
			continue
		}
		names = append(names, head[0])
		if len(head) > 1 && isCommandWord(head[1]) {
			continue // the alternatives belong to the next level
		}
		for _, alt := range alts[1:] {
			if f := strings.Fields(alt); len(f) > 0 && isCommandWord(f[0]) {
				names = append(names, f[0])
			}
		}
	}
	return names
}

// takesArgument reports whether the usage text lists an argument where a
// subcommand of the group would stand: `applysmith evaluate <url|inbox-row-id>`.
func takesArgument(group, text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if _, rest, ok := strings.Cut(line, "applysmith "+group+" "); ok && strings.HasPrefix(strings.TrimSpace(rest), "<") {
			return true
		}
	}
	return false
}

// isCommandWord is a lower-case word such as list or set-default.
func isCommandWord(s string) bool {
	return s != "" && s[0] >= 'a' && s[0] <= 'z' && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

// ignoredWorkspace registers --workspace on a command that reads no
// workspace, so a script may pass the flag to every command.
func ignoredWorkspace(fs *flag.FlagSet) {
	fs.String("workspace", "", "accepted and ignored: this command reads no workspace")
}

// errNoServer is a command that needs the workspace's running server.
var errNoServer = errors.New("no server runs for this workspace; start the app or `applysmith serve`")

// usageError is input the command refuses before it writes anything; it
// exits 2.
type usageError struct{ msg string }

func (u *usageError) Error() string { return u.msg }

// errNoWorkspace is a workspace folder that does not exist: a mistyped
// --workspace must not become a new, empty workspace.
var errNoWorkspace = errors.New("`applysmith init` creates it, or --workspace names another")

// workspacePath resolves the workspace of a command and refuses one whose
// folder does not exist. Only init and setup, which create it, take
// newWorkspacePath instead.
func (e *env) workspacePath(flagValue string) (string, error) {
	return e.resolveWorkspace(flagValue, false)
}

func (e *env) newWorkspacePath(flagValue string) (string, error) {
	return e.resolveWorkspace(flagValue, true)
}

func (e *env) resolveWorkspace(flagValue string, create bool) (string, error) {
	root, err := workspace.Resolve(workspace.ResolveOptions{Flag: flagValue, Getenv: e.getenv, HomeDir: e.homeDir, ConfigDir: e.configDir})
	if _, serr := os.Stat(root); err == nil && !create && errors.Is(serr, os.ErrNotExist) {
		return "", fmt.Errorf("no workspace at %s yet; %w", root, errNoWorkspace)
	}
	if err == nil {
		e.noteDemo(root)
		if !slices.Contains(e.roots, root) {
			e.roots = append(e.roots, root)
		}
	}
	return root, err
}

// releaseBrowsers ends every command: each workspace the command resolved
// gets the managed browser this process launched for it stopped when nothing
// holds it (launch.ReleaseBrowser). A command that reads a page, probes a
// sign-in or checks the setup windows may start the browser on its own; left
// running it keeps the profile with no owner and the next command fails as
// profile-in-use. A browser a server or the app owns is only adopted by this
// process, and ReleaseBrowser leaves an adopted browser alone.
func (e *env) releaseBrowsers(ctx context.Context) {
	for _, root := range e.roots {
		launch.ReleaseBrowser(context.WithoutCancel(ctx), workspace.Open(root))
	}
	e.roots = nil
}

// noteDemo prints the demo line, once, when a command opens a demo workspace.
// It goes to standard error so a command's own output stays parseable.
func (e *env) noteDemo(root string) {
	if e.demoNoted || !workspace.Open(root).IsDemo() {
		return
	}
	e.demoNoted = true
	fmt.Fprintf(e.stderr, "demo workspace %s: the fictional persona Alex Example. %s\n", root, demo.Never)
}

// localTime is how a command shows a moment to a person: the local date and
// clock time with the zone named.
func localTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04 MST") }

// defaultPort is APPLYSMITH_PORT when set, else the product default.
func (e *env) defaultPort() (int, error) { return launch.PortFromEnv(e.getenv) }

// refuseUsage names what is wrong with a command's arguments, prints its
// usage and returns the usage-error exit code.
func (e *env) refuseUsage(cmd, usage string, problems ...string) int {
	for _, p := range problems {
		fmt.Fprintf(e.stderr, "applysmith %s: %s\n", cmd, p)
	}
	fmt.Fprint(e.stderr, usage)
	return 2
}

// fail prints the failure and returns its exit code: 2 for input the tracker
// rejected or a workspace folder that does not exist, 3 when no model is set
// up for the task (a person must supply a provider and its key), else 1.
func (e *env) fail(cmd string, err error) int {
	fmt.Fprintf(e.stderr, "applysmith %s: %s\n", cmd, failureText(cmd, err))
	var usage *usageError
	switch {
	case errors.Is(err, tracker.ErrInvalid), errors.Is(err, errNoWorkspace), errors.As(err, &usage),
		workspace.IsCode(err, workspace.CodeWorkspaceInvalid):
		return 2
	case errors.Is(err, llm.ErrNoRoute):
		return 3
	}
	return 1
}

// failureText drops the package label from a message ("store: conflict: ",
// "no-route (outreach): ") and words a missing row as "no tracker row 7"
// with the command that lists them. A documented code such as
// server_already_running stays.
func failureText(cmd string, err error) string {
	msg := err.Error()
	msg = strings.Replace(msg, identity.ErrNotURL.Error(), strings.TrimPrefix(identity.ErrNotURL.Error(), "identity: "), 1)
	var model *llm.Error
	if errors.As(err, &model) && model.Kind == llm.KindNoRoute && model.Message != "" {
		msg = strings.Replace(msg, strings.TrimSuffix(model.Error(), model.Message), "", 1)
	}
	for _, l := range []error{store.ErrNotFound, discovery.ErrNotFound, documents.ErrNotFound} {
		if what, ok := strings.CutPrefix(msg, l.Error()+": "); ok && len(strings.Fields(what)) <= 3 {
			group := strings.Fields(cmd)[0]
			if slices.Contains([]string{"tracker", "inbox", "leads", "documents"}, group) {
				what += "; `applysmith " + group + " list` shows what there is"
			}
			return "no " + what
		}
	}
	for _, l := range []error{store.ErrConflict, store.ErrInvalidBackup, tracker.ErrInvalid} {
		msg = strings.TrimPrefix(msg, l.Error()+": ")
	}
	return msg
}

func (e *env) routes() int {
	fmt.Fprint(e.stdout, api.FormatRoutes(api.Routes()))
	return 0
}

// columns is text/tabwriter for the CLI's tables, measuring cells in terminal
// columns: an East Asian wide or fullwidth character (a Chinese company name)
// takes two, where tabwriter counts every character as one. Each
// tab-terminated cell is padded to its column's widest cell plus two spaces.
type columns struct {
	out io.Writer
	buf bytes.Buffer
}

func newColumns(out io.Writer) *columns        { return &columns{out: out} }
func (c *columns) Write(p []byte) (int, error) { return c.buf.Write(p) }

func (c *columns) Flush() error {
	text := strings.TrimSuffix(c.buf.String(), "\n")
	c.buf.Reset()
	if text == "" {
		return nil
	}
	var rows [][]string
	var widths []int
	for _, line := range strings.Split(text, "\n") {
		cells := strings.Split(line, "\t")
		for i, cell := range cells[:len(cells)-1] {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], cellWidth(cell))
		}
		rows = append(rows, cells)
	}
	var b strings.Builder
	for _, cells := range rows {
		for i, cell := range cells[:len(cells)-1] {
			b.WriteString(cell + strings.Repeat(" ", widths[i]-cellWidth(cell)+2))
		}
		b.WriteString(cells[len(cells)-1] + "\n")
	}
	_, err := io.WriteString(c.out, b.String())
	return err
}

func cellWidth(s string) int {
	n := 0
	for _, r := range s {
		n++
		if k := width.LookupRune(r).Kind(); k == width.EastAsianWide || k == width.EastAsianFullwidth {
			n++
		}
	}
	return n
}
