package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"applysmith/internal/onboarding"
	"applysmith/internal/vault"
)

// grammar is the command tree as the CLI itself declares it: the top-level
// command list and the usage text of every command group.
type grammar [][]string

var usageTexts = []string{backupUsage, evaluateUsage, setupUsage, cvUsage, scanUsage, inboxUsage, blockedUsage, registryUsage, livenessUsage, signalsUsage, llmUsage, trackerUsage, scheduleUsage,
	agentUsage, answerBankUsage, applyUsage, scorecardUsage, autofillUsage, browserUsage, documentsUsage, documentCommandsUsage, interviewUsage, extensionUsage,
	gmailUsage, leadsUsage, kbUsage, mcpUsage, tokenUsage, remoteUsage, resumesUsage, telegramUsage, secretsUsage, credentialsUsage, demoUsage}

// leadingWords are the words of a usage line before its first flag, bracket
// or alternative marker; a <placeholder> stands for any word.
func leadingWords(line string) []string {
	var out []string
	for _, w := range strings.Fields(line) {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "[") || strings.HasPrefix(w, "(") || w == "|" || w == "..." {
			break
		}
		out = append(out, w)
	}
	return out
}

func buildGrammar() grammar {
	var g grammar
	inCommands := false
	for _, line := range strings.Split(usage, "\n") {
		switch {
		case strings.HasPrefix(line, "commands:"):
			inCommands = true
		case inCommands && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			g = append(g, []string{strings.Fields(line)[0]})
		default:
			if strings.TrimSpace(line) == "" {
				inCommands = false
			}
		}
	}
	for _, text := range usageTexts {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimPrefix(strings.TrimSpace(line), "usage: ")
			if !strings.HasPrefix(line, "applysmith ") {
				continue
			}
			// Two spaces separate a usage line from its description.
			if i := strings.Index(line, "  "); i >= 0 {
				line = line[:i]
			}
			segments := strings.Split(joinPlaceholderAlternatives(line), " | ")
			// An alternative command replaces the last command word before
			// it; an alternative flag follows the words.
			prefix := leadingWords(segments[0])[1:]
			for len(prefix) > 1 && strings.HasPrefix(prefix[len(prefix)-1], "<") {
				prefix = prefix[:len(prefix)-1]
			}
			for i, seg := range segments {
				words := leadingWords(seg)
				if i == 0 {
					words = words[1:]
				} else if len(words) > 0 {
					words = append(append([]string(nil), prefix[:len(prefix)-1]...), words...)
				} else {
					words = prefix
				}
				g = append(g, words)
			}
		}
	}
	return g
}

// joinPlaceholderAlternatives closes up " | " inside a placeholder
// (<owner/repo | https://github.com/owner/repo>), which separates the forms of
// one argument, not two commands.
func joinPlaceholderAlternatives(line string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '<':
			depth++
		case '>':
			depth--
		}
		if depth > 0 && strings.HasPrefix(line[i:], " | ") {
			b.WriteByte('|')
			i += 2
			continue
		}
		b.WriteByte(line[i])
	}
	return b.String()
}

// accepts reports whether the words (after "applysmith") are a command of the
// grammar. Extra bare words are positional arguments only when the pattern
// ends in a placeholder. A <step> placeholder takes a catalog step id.
func (g grammar) accepts(words []string) (bool, string) {
	stepProblem := ""
	for _, p := range g {
		if len(p) == 0 || len(words) < len(p) {
			continue
		}
		ok := true
		for i, w := range p {
			if !strings.HasPrefix(w, "<") && !slices.Contains(strings.Split(w, "|"), words[i]) {
				ok = false
				break
			}
		}
		if !ok || (len(words) > len(p) && !strings.HasPrefix(p[len(p)-1], "<")) {
			continue
		}
		for i, w := range p {
			// A <step> placeholder takes a catalog step id, or a leaf of one such
			// as story.voice; a documented <placeholder> stands for any value.
			if w != "<step>" || strings.HasPrefix(words[i], "<") {
				continue
			}
			if _, found := onboarding.StepByID(strings.SplitN(words[i], ".", 2)[0]); !found {
				ok = false
				stepProblem = "step " + words[i] + " is not in the catalog"
			}
		}
		if ok {
			return true, ""
		}
	}
	if stepProblem != "" {
		return false, stepProblem
	}
	return false, "is not a command of the CLI"
}

var (
	fenceRE   = regexp.MustCompile("(?s)```[a-z]*\n(.*?)```")
	homePath  = regexp.MustCompile(`(/` + `Users/|/` + `home/|[A-Za-z]:\\` + `Users\\)[A-Za-z0-9._-]+`)
	doctorRE  = regexp.MustCompile(`applysmith doctor --only ([a-z0-9.-]+)`)
	pageNamed = regexp.MustCompile(`^(\d\d)-([a-z0-9-]+)\.md$`)
)

// commandWords are the words of a documented command before its first flag,
// quote or placeholder-free argument list.
func commandWords(line string) []string {
	var out []string
	switch strings.Fields(line)[1] {
	case "--version":
		return []string{"version"}
	case "--help", "-h":
		return []string{"help"}
	}
	for _, w := range strings.Fields(line)[1:] {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, `"`) || strings.HasPrefix(w, "'") || strings.HasPrefix(w, ">") || strings.HasPrefix(w, "|") {
			break
		}
		out = append(out, w)
	}
	return out
}

func doctorTargets() map[string]bool {
	ok := map[string]bool{"runtime": true}
	for _, s := range onboarding.Catalog() {
		ok[s.ID] = true
	}
	for _, c := range onboarding.Checks() {
		ok[c.ID], ok[c.Group()] = true, true
	}
	return ok
}

// commandProblems checks every applysmith command in the fenced blocks of one
// document against the command tree.
func commandProblems(name, text string, g grammar) []string {
	var problems []string
	for _, block := range fenceRE.FindAllStringSubmatch(text, -1) {
		for _, line := range strings.Split(block[1], "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$ "))
			if !strings.HasPrefix(line, "applysmith ") {
				continue
			}
			if ok, why := g.accepts(commandWords(line)); !ok {
				problems = append(problems, name+": `"+line+"` "+why)
			}
			if regexp.MustCompile(`\s--key\s`).MatchString(line + " ") {
				problems = append(problems, name+": `"+line+"` passes a key as an argument")
			}
		}
	}
	return problems
}

// stepHeadings are the sections every step page carries, in order.
var stepHeadings = []string{"What this step does", "Before you start", "Steps", "Where this is stored", "How to check it worked", "If something goes wrong"}

// checkSetupDocs returns the problems of a docs/setup tree: page names that
// are not the catalog's steps, commands that are not in the CLI, step ids that
// are not in the catalog, doctor targets that do not exist and keys passed as
// arguments.
func checkSetupDocs(dir string, g grammar) []string {
	var problems []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]bool{}
	for _, ent := range entries {
		name := ent.Name()
		if name == "README.md" || name == "for-agents.md" || name == "reference.md" {
			continue
		}
		m := pageNamed.FindStringSubmatch(name)
		if m == nil {
			problems = append(problems, name+": not a README, for-agents, reference or NN-<step>.md page")
			continue
		}
		step, ok := onboarding.StepByID(m[2])
		switch {
		case !ok:
			problems = append(problems, name+": step "+m[2]+" is not in the catalog")
		case m[1] != twoDigits(step.N):
			problems = append(problems, name+": step "+step.ID+" is number "+twoDigits(step.N))
		default:
			seen[step.ID] = true
		}
	}
	for _, s := range onboarding.Catalog() {
		if !seen[s.ID] {
			problems = append(problems, "no page for step "+s.ID)
		}
	}
	targets := doctorTargets()
	for _, ent := range entries {
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		text := string(b)
		if m := homePath.FindString(text); m != "" {
			problems = append(problems, ent.Name()+": contains the absolute home path "+m+"; use ~")
		}
		if pageNamed.MatchString(ent.Name()) {
			for _, h := range stepHeadings {
				if !strings.Contains("\n"+text, "\n## "+h+"\n") {
					problems = append(problems, ent.Name()+": lacks the heading \""+h+"\"")
				}
			}
		}
		problems = append(problems, commandProblems(ent.Name(), text, g)...)
		for _, m := range doctorRE.FindAllStringSubmatch(text, -1) {
			if !targets[m[1]] {
				problems = append(problems, ent.Name()+": `applysmith doctor --only "+m[1]+"` names no step or check")
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var goodHeadings = "## " + strings.Join(stepHeadings, "\n\ntext\n\n## ") + "\n"

func goodTree() map[string]string {
	files := map[string]string{
		"README.md":     "# Setup\n\n```\napplysmith --version\n```\n",
		"for-agents.md": "# Agents\n\n```\napplysmith setup next --json\n```\n",
		"reference.md":  "# Reference\n",
	}
	for _, s := range onboarding.Catalog() {
		files[twoDigits(s.N)+"-"+s.ID+".md"] = "# " + s.Title + "\n\n" + goodHeadings + "\n```\napplysmith setup apply " + s.ID + " --file -\napplysmith setup status --json\napplysmith doctor --only " + s.ID + "\n```\n"
	}
	return files
}

func TestSetupDocsChecker(t *testing.T) {
	g := buildGrammar()
	if p := checkSetupDocs(writeTree(t, goodTree()), g); len(p) != 0 {
		t.Fatalf("a correct tree has problems: %v", p)
	}
	bad := goodTree()
	delete(bad, "05-targets.md")
	bad["03-frobnicate.md"] = "# x\n"
	bad["07-cv.md"] = "# wrong number\n"
	bad["03-identity.md"] = "# Identity\n\n## What this step does\n\nSee /" + "Users/someone/Applysmith.\n"
	bad["README.md"] = "```\napplysmith setup frobnicate\napplysmith setup apply nonsense --file -\napplysmith llm configure --preset openrouter --key abc\napplysmith nosuchcommand\n```\nRun `applysmith doctor --only nosuch`.\n"
	problems := strings.Join(checkSetupDocs(writeTree(t, bad), g), "\n")
	for _, want := range []string{"no page for step targets", "step frobnicate is not in the catalog", "07-cv.md: step cv is number 02",
		"applysmith setup frobnicate` is not a command", "step nonsense is not in the catalog", "passes a key as an argument",
		"applysmith nosuchcommand` is not a command", "applysmith doctor --only nosuch` names no step or check",
		"03-identity.md: contains the absolute home path /" + "Users/someone", `03-identity.md: lacks the heading "If something goes wrong"`} {
		if !strings.Contains(problems, want) {
			t.Errorf("the checker missed %q\n%s", want, problems)
		}
	}
}

// Every command the grammar claims exists must reach its own flag parsing,
// so a usage line cannot describe a command the CLI does not have. The guard
// that refuses an unknown subcommand reads the same usage texts, so a word
// listed there but not dispatched passes it; the dispatcher then prints the
// group's usage instead of parsing the subcommand's flags, and that fails
// here. A group name alone answers with its usage.
func TestUsageGrammarNamesRealCommands(t *testing.T) {
	for _, p := range buildGrammar() {
		args := make([]string, 0, len(p)+1)
		for _, w := range p {
			if strings.HasPrefix(w, "<") {
				w = "x"
			}
			args = append(args, w)
		}
		switch args[0] {
		case "serve", "routes", "version", "help":
			continue // these do not parse a flag set first or do not run headless
		}
		te := newTestEnv(t)
		out := te.run(context.Background(), append(args, "--zz-no-such-flag")...)
		if out.code != 2 || strings.Contains(out.stderr, "unknown command") {
			t.Errorf("`applysmith %s` is in a usage text but the CLI answers exit %d: %s", strings.Join(p, " "), out.code, firstLine(out.stderr))
			continue
		}
		if len(p) > 1 && !strings.Contains(out.stderr, "flag provided but not defined: --zz-no-such-flag") {
			t.Errorf("`applysmith %s` did not reach its own flag parsing: %s", strings.Join(p, " "), firstLine(out.stderr))
		}
	}
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func TestSetupDocsOfTheRepositoryMatchTheCatalogAndTheCLI(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "setup")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if problems := checkSetupDocs(dir, buildGrammar()); len(problems) != 0 {
		t.Fatalf("docs/setup disagrees with the catalog or the CLI:\n%s", strings.Join(problems, "\n"))
	}
}

// The reference page lists every registered secret and every APPLYSMITH_
// variable the product reads, so adding either without documenting it fails.
func TestSetupReferenceListsEverySecretAndVariable(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "setup", "reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	for _, r := range vault.Registry() {
		if !strings.Contains(text, "`"+r.NamePattern+"`") {
			t.Errorf("reference.md has no row for the secret %s", r.NamePattern)
		}
	}
	envName := regexp.MustCompile(`"(APPLYSMITH_[A-Z0-9_]*[A-Z0-9])"`)
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range envName.FindAllStringSubmatch(string(src), -1) {
				if !strings.Contains(text, m[1]) {
					t.Errorf("%s reads %s, which reference.md does not list", path, m[1])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The user guide, the public architecture pages and the product skill show
// commands too; each must exist, and none may carry an owner's home path.
func TestOtherDocumentedCommandsExist(t *testing.T) {
	root := filepath.Join("..", "..")
	paths, _ := filepath.Glob(filepath.Join(root, "docs", "user-guide", "*.md"))
	if len(paths) == 0 {
		t.Error("docs/user-guide has no pages")
	}
	for _, name := range []string{"overview", "browser", "storage", "policies"} {
		paths = append(paths, filepath.Join(root, "docs", "architecture", name+".md"))
	}
	paths = append(paths, filepath.Join(root, "skills", "applysmith", "SKILL.md"), filepath.Join(root, "docs", "README.md"))
	g := buildGrammar()
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			continue
		}
		if m := homePath.FindString(string(b)); m != "" {
			t.Errorf("%s contains the absolute home path %s", path, m)
		}
		for _, problem := range commandProblems(filepath.Base(path), string(b), g) {
			t.Error(problem)
		}
	}
}

func TestVersionFlagIsTheInstallCheck(t *testing.T) {
	out := newTestEnv(t).run(context.Background(), "--version")
	if out.code != 0 || !strings.HasPrefix(out.stdout, "applysmith ") {
		t.Fatalf("out = %+v", out)
	}
}
