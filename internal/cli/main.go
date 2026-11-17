// Package cli is the applysmith command line and headless server. Two binaries
// host it: cmd/applysmith, and the desktop app when it is started under the name
// applysmith.
package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/discovery"
	"applysmith/internal/launch"
	"applysmith/internal/llm"
	"applysmith/internal/onboarding"
	"applysmith/internal/remote/tunnel"
	"applysmith/internal/signals"
	"applysmith/internal/vault"
)

// version is set at link time: -ldflags "-X applysmith/internal/cli.version=...",
// or by Config.Version for a host that learns it another way.
var version = "dev"

const usage = `usage: applysmith <command> [flags]

commands:
  init            create the workspace and seed its files
  demo            create the demo workspace with the fictional example persona (demo init [--workspace <dir>])
  serve           run the local API and dashboard for a workspace
  open            print a fresh dashboard link from the running server (--browser also opens it)
  pair            print the API token for the extension or another client
  token           rotate the API token (token rotate)
  cv              import a PDF, DOCX, Markdown or plain-text CV after reviewing its text (cv import <file>)
  setup           walk through the setup steps interactively; status, next and apply for scripts (help setup lists them)
  telegram        set up the Telegram bot: set-token, clear-token, pair, unpair, status
  browser         managed browser: status, open [url], stop [--force], check (sign-ins)
  remote          turn remote access on or off, pair a phone, list and revoke devices (run without arguments for the commands)
  secrets         list, set, delete and test the stored secrets; a value is read from standard input (run without arguments for the commands)
  credentials     manage the sign-in passwords: list, set-default, add-alternate, set-site, remove-site, remove-alternate, reorder
  llm             manage the language-model provider: show, configure, keys, set, reset, test, cost, prices
  doctor          check the setup and print what to fix
  documents       list, show and delete document versions; check, set up and smoke-test the PDF toolchain (documents toolchain check|setup|smoke)
  resumes         manage the résumé roster: add, list, remove, set-default (run without arguments for the commands)
  resume          generate a tailored or baseline résumé, with its cover letter on request (resume generate)
  cover-letter    write a cover letter for an application
  interview-prep  build the prep pack for one interview stage of an application
  outreach        draft a LinkedIn message to a recruiter, hiring manager, peer or interviewer; nothing is sent
  followup-draft  draft a follow-up on an application in flight; nothing is sent
  backup          create, verify or restore a backup (create | verify | restore)
  evaluate        evaluate a posting (url, inbox row id, --file or --text); evaluate decode reads one layer by layer
  prompts         list, show, diff and check the evaluation prompts
  tracker         list, show and change application rows (run without arguments for the commands)
  report          let a report below the 4.5 floor have tailored PDFs (report override <number>)
  answer-bank     show what still needs an answer, propose candidate answers from recurring misses, approve or reject them (run without arguments for the commands)
  kb              search, list, lint, import and publish the knowledge base (run without arguments for the commands)
  gmail           connect Gmail with an app password, test, disconnect, sync, resync, reclassify and purge it (run without arguments for the commands)
  schedule        list, show, add, edit, enable, disable, delete, run and read the history of schedules; pause or resume them all; list, preview and apply the preset packs (run without arguments for the commands)
  autofill        fill the tab the apply window shows, once, without submitting
  apply           run an application or an apply list; without --submit it fills and stops before Submit
  scan            run a discovery scan, list the sources, list scan runs (run without arguments for the usage)
  inbox           list, add, dismiss, promote and export the pipeline inbox
  blocked         add, remove and list blocked companies
  leads           list, expand, evaluate and dismiss the stored hiring leads (run without arguments for the usage)
  registry        seed and maintain the ATS board registry
  liveness        check whether a posting URL is still live
  signals         run the hiring-signal pipeline: tick, backfill, rescore, replay, decompose, status (run without arguments for the commands)
  agent           list, show, export and delete the Ask Agent conversations (agent sessions; run without arguments for the commands)
  extension       write the unpacked browser extension (extension export <dir>)
  mcp             run the read-only MCP server on standard input and output (--print-config, --check)
  routes          print the API route table
  version         print build identity

to start: "applysmith init" (or "applysmith demo init" to try a fictional persona first), fill in the files it
names, then "applysmith setup", "applysmith doctor" and "applysmith serve" for the dashboard.
"applysmith help <command>" prints a command's usage and flags.
--workspace <dir> or APPLYSMITH_WORKSPACE picks the workspace; with neither, the one setup recorded, else ~/Applysmith.
exit codes: 0 done, 1 failed, 2 usage error or rejected input, 3 a person must act, 4 the environment failed
`

// env is everything a command reads from the process, so tests can run
// commands in-process.
type env struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	backend        vault.Backend
	// scanHTTP and scanEndpoints redirect discovery at local servers in tests.
	scanHTTP      *http.Client
	scanEndpoints discovery.Endpoints
	// signalEndpoints redirects the hiring-signal collectors at local servers (tests).
	signalEndpoints signals.Endpoints
	// livenessReader replaces the browser page reader (tests).
	livenessReader discovery.PageReader
	// scanWindow replaces the browser scan window (tests).
	scanWindow discovery.Window
	// llmOptions redirects model calls in tests.
	llmOptions []llm.Option
	// stdin defaults to the process input; terminal says whether it is a
	// person at a terminal. homeDir and configDir default to the user's own;
	// tests point them at temporary directories.
	stdin              io.Reader
	terminal           func() bool
	homeDir, configDir string
	// hook adjusts the setup environment (tests replace the machine probes).
	hook func(*onboarding.Env)
	// openURL starts the default browser; nil means the system opener.
	openURL func(string) error
	// gmailDial replaces the connection to Gmail's IMAP server (tests).
	gmailDial func(ctx context.Context) (net.Conn, error)
	// telegramAPI points the Telegram commands at another Bot API host (tests).
	telegramAPI string
	// interactive runs a command on the terminal; nil runs it directly (tests).
	interactive func(*exec.Cmd) error
	// remoteTunnel replaces the supervisor of the tunnel child (tests).
	remoteTunnel func(tunnel.Config) api.TunnelProcess
	// remoteEvery replaces how often a served workspace re-reads policy.yml (tests).
	remoteEvery time.Duration
	// demoNoted is set once the demo line was printed.
	demoNoted bool
	// roots are the workspaces the running command resolved; run releases the
	// managed browser of each when the command ends.
	roots []string
	// dashboard supplies the built dashboard that serve embeds; nil serves
	// the placeholder page.
	dashboard func() (fs.FS, error)
}

// Name is the command name. A binary that hosts the CLI besides its own job
// runs it when it is started under this name.
const Name = "applysmith"

// InvokedAs reports whether a process started with argv0 was asked to be the
// command line.
func InvokedAs(argv0 string) bool { return filepath.Base(argv0) == Name }

// Config is what the binary hosting the CLI supplies.
type Config struct {
	// Version replaces the link-time version when it is not empty.
	Version string
	// Dashboard returns the built dashboard for serve; nil serves a
	// placeholder page, so a build without the bundle still compiles.
	Dashboard func() (fs.FS, error)
}

func (e *env) dashboardAssets() (fs.FS, error) {
	if e.dashboard == nil {
		return nil, nil
	}
	return e.dashboard()
}

// Main runs the command line named by os.Args and exits with its code.
func Main(c Config) {
	if c.Version != "" {
		version = c.Version
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	e := &env{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, backend: vault.NewKeychain(), stdin: os.Stdin, terminal: stdinIsTerminal, dashboard: c.Dashboard}
	code := e.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

// run executes one command and returns the process exit code: 0 success,
// 1 failure, 2 usage error or rejected input, 3 a person must act, 4 the
// environment failed (doctor unable to run).
func (e *env) run(ctx context.Context, args []string) int {
	defer e.releaseBrowsers(ctx)
	if len(args) > 1 && slices.ContainsFunc(args[1:], isHelpFlag) {
		// An explicit request for help is not a usage error: the help goes to
		// standard output and the command exits 0. A word that names no
		// command is refused all the same, on standard error.
		if code, refused := e.unknownSubcommand(args); refused {
			return code
		}
		if !slices.Contains(commandNames(), args[0]) {
			return e.dispatch(ctx, args)
		}
		errOut := e.stderr
		e.stderr = e.stdout
		defer func() { e.stderr = errOut }()
		if code := e.dispatch(ctx, args); code != 2 {
			return code
		}
		return 0
	}
	return e.dispatch(ctx, args)
}

// isHelpFlag is every spelling the flag package answers with its usage.
func isHelpFlag(a string) bool {
	switch a {
	case "-h", "-help", "--h", "--help":
		return true
	}
	return false
}

// groupUsage is, for each command with subcommands, the usage text that
// lists them; unknownSubcommand refuses a word it does not list.
var groupUsage = map[string]string{
	"agent": agentUsage, "answer-bank": answerBankUsage, "backup": backupUsage, "blocked": blockedUsage, "browser": browserUsage,
	"credentials": credentialsUsage, "cv": cvUsage, "demo": demoUsage, "documents": documentsUsage + documentCommandsUsage,
	"evaluate": evaluateUsage, "extension": extensionUsage, "gmail": gmailUsage, "inbox": inboxUsage, "kb": kbUsage, "leads": leadsUsage, "llm": llmUsage,
	"prompts": evaluateUsage, "registry": registryUsage, "remote": remoteUsage, "report": reportUsage,
	"resume": documentCommandsUsage, "resumes": resumesUsage, "schedule": scheduleUsage, "secrets": secretsUsage, "setup": setupUsage,
	"signals": signalsUsage, "telegram": telegramUsage, "token": tokenUsage, "tracker": trackerUsage,
}

func (e *env) dispatch(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, usage)
		return 2
	}
	if code, refused := e.unknownSubcommand(args); refused {
		return code
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(e.stdout, buildIdentity())
		return 0
	case "init":
		return e.initWorkspace(ctx, args[1:])
	case "demo":
		return e.demoCmd(ctx, args[1:])
	case "serve":
		return e.serve(ctx, args[1:])
	case "open":
		return e.open(ctx, args[1:])
	case "pair":
		return e.pair(args[1:])
	case "token":
		return e.token(ctx, args[1:])
	case "setup":
		return e.setup(ctx, args[1:])
	case "cv":
		return e.cvImport(ctx, args[1:])
	case "llm":
		return e.llm(ctx, args[1:])
	case "doctor":
		return e.doctor(ctx, args[1:])
	case "documents":
		return e.documents(ctx, args[1:])
	case "resume":
		return e.resumeCmd(ctx, args[1:])
	case "resumes":
		return e.resumesCmd(ctx, args[1:])
	case "cover-letter":
		return e.coverCmd(ctx, args[1:])
	case "interview-prep":
		return e.interviewPrep(ctx, args[1:])
	case "outreach":
		return e.outreachCmd(ctx, args[1:])
	case "followup-draft":
		return e.followupDraftCmd(ctx, args[1:])
	case "backup":
		return e.backup(ctx, args[1:])
	case "tracker":
		return e.tracker(ctx, args[1:])
	case "report":
		return e.reportCmd(ctx, args[1:])
	case "kb":
		return e.kb(ctx, args[1:])
	case "gmail":
		return e.gmail(ctx, args[1:])
	case "telegram":
		return e.telegramCmd(ctx, args[1:])
	case "browser":
		return e.browserCmd(ctx, args[1:])
	case "remote":
		return e.remoteCmd(ctx, args[1:])
	case "secrets":
		return e.secrets(ctx, args[1:])
	case "credentials":
		return e.credentialsCmd(ctx, args[1:])
	case "answer-bank":
		return e.answerBank(ctx, args[1:])
	case "schedule":
		return e.schedule(ctx, args[1:])
	case "autofill":
		return e.autofill(ctx, args[1:])
	case "apply":
		return e.apply(ctx, args[1:])
	case "scan":
		if len(args) == 1 {
			fmt.Fprint(e.stderr, scanUsage)
			return 2
		}
		return e.scan(ctx, args[1:])
	case "inbox":
		return e.inbox(ctx, args[1:])
	case "blocked":
		return e.blocked(ctx, args[1:])
	case "leads":
		return e.leads(ctx, args[1:])
	case "registry":
		return e.registry(ctx, args[1:])
	case "liveness":
		return e.liveness(ctx, args[1:])
	case "signals":
		return e.signals(ctx, args[1:])
	case "evaluate":
		return e.evaluate(ctx, args[1:])
	case "prompts":
		return e.prompts(args[1:])
	case "agent":
		return e.agentCmd(ctx, args[1:])
	case "extension":
		return e.extensionCmd(args[1:])
	case "mcp":
		return e.mcp(ctx, args[1:])
	case "routes":
		return e.routes()
	case "help", "-h", "--help":
		if len(args) > 1 && args[0] == "help" {
			return e.run(ctx, []string{args[1], "--help"})
		}
		fmt.Fprint(e.stdout, usage)
		return 0
	}
	fmt.Fprintf(e.stderr, "applysmith: unknown command %q", args[0])
	if near, ok := nearestCommand(args[0], commandNames()); ok {
		fmt.Fprintf(e.stderr, "; did you mean %q?", near)
	}
	fmt.Fprintln(e.stderr, "\nrun `applysmith help` for the list of commands")
	return 2
}

// commandNames lists the commands the top-level usage text documents.
func commandNames() (names []string) {
	for _, line := range strings.Split(usage, "\n") {
		if strings.HasPrefix(line, "  ") {
			names = append(names, strings.Fields(line)[0])
		}
	}
	return names
}

// buildIdentity reports the link-time version, the VCS revision recorded by
// the Go toolchain and the target platform.
func buildIdentity() string {
	rev, dirty := launch.VCSRevision()
	if rev == "" {
		rev = "unknown"
	} else if dirty {
		rev += "-dirty"
	}
	return fmt.Sprintf("applysmith %s (%s) %s %s/%s", version, rev, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
