package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/store"
)

const agentUsage = `usage:
  applysmith agent sessions list [--channel dashboard|telegram|scheduled|digest] [--limit N] [--json]
  applysmith agent sessions show <id> [--json]
  applysmith agent sessions export <id> [--out <file>]
  applysmith agent sessions delete <id>
  applysmith agent login

list prints the Ask Agent conversations, newest first
show prints a conversation's messages, tool calls and checkpoints
export writes the whole conversation as JSON to --out, or to standard output
delete removes a conversation with its messages, checkpoints and search entries; it cannot be undone

login starts pi on this terminal in the product's agent directory so you can sign in with /login (a subscription); route the agent to it with
the provider pi:<pi provider id> on the agent-kernel task: applysmith llm set agent-kernel pi:openai-codex/<model>. The login stays in pi's own files.

every command takes --workspace <dir>; list, show and export read the database without a lock, delete joins a running server
`

func (e *env) agentCmd(ctx context.Context, args []string) int {
	if len(args) >= 1 && args[0] == "login" {
		return e.agentLogin(args[1:])
	}
	if len(args) < 2 || args[0] != "sessions" {
		fmt.Fprint(e.stderr, agentUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"list": e.sessionsList, "show": e.sessionsShow, "export": e.sessionsExport, "delete": e.sessionsDelete,
	}
	if run, ok := cmds[args[1]]; ok {
		return run(ctx, args[2:])
	}
	fmt.Fprint(e.stderr, agentUsage)
	return 2
}

// withSessions opens the workspace database for one sessions command.
func (e *env) withSessions(ctx context.Context, cmd, wsFlag string, access storeAccess, fn func(*sessions.Store) int) int {
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return e.fail("agent sessions "+cmd, err)
	}
	_, st, release, err := e.openStore(ctx, root, access)
	if err != nil {
		return e.fail("agent sessions "+cmd, err)
	}
	defer release()
	return fn(sessions.New(st, nil))
}

type sessionRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Channel   string `json:"channel"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	ParentID  string `json:"parentId,omitempty"`
}

func rowOf(s sessions.Session) sessionRow {
	return sessionRow{ID: s.ID, Title: s.Title, Channel: string(s.Channel), CreatedAt: s.CreatedAt.Format(time.RFC3339), UpdatedAt: s.UpdatedAt.Format(time.RFC3339), ParentID: s.ParentID}
}

func (e *env) sessionsList(ctx context.Context, args []string) int {
	fs := e.flags("agent sessions list")
	ws, asJSON := fs.String("workspace", "", "workspace directory"), fs.Bool("json", false, "print JSON")
	channel, limit := fs.String("channel", "", "only this channel"), fs.Int("limit", 50, "most conversations to print")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	if *channel != "" && !sessions.Channel(*channel).Valid() {
		fmt.Fprintf(e.stderr, "applysmith agent sessions list: %q is not a channel\n", *channel)
		return 2
	}
	return e.withSessions(ctx, "list", *ws, accessRead, func(s *sessions.Store) int {
		list, total, err := s.List(ctx, sessions.ListQuery{Channel: sessions.Channel(*channel), Limit: *limit})
		if err != nil {
			return e.fail("agent sessions list", err)
		}
		rows := make([]sessionRow, len(list))
		for i, x := range list {
			rows[i] = rowOf(x)
		}
		if *asJSON {
			return e.printJSON(map[string]any{"total": total, "sessions": rows})
		}
		fmt.Fprintf(e.stdout, "%d conversations (showing %d)\n", total, len(rows))
		for _, r := range rows {
			fmt.Fprintf(e.stdout, "%s  %-9s  %s  %s\n", r.ID, r.Channel, r.UpdatedAt, r.Title)
		}
		return 0
	})
}

type messageRow struct {
	ID         int64  `json:"id"`
	TurnID     string `json:"turnId"`
	Role       string `json:"role"`
	Body       string `json:"body"`
	Tool       string `json:"tool,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	OK         bool   `json:"ok"`
	SystemMade bool   `json:"systemMade,omitempty"`
	At         string `json:"at"`
}

type checkpointRow struct {
	ID           int64  `json:"id"`
	CoveredTo    int64  `json:"coveredTo"`
	CoveredTurns int    `json:"coveredTurns"`
	Tainted      bool   `json:"tainted"`
	Summary      string `json:"summary"`
	At           string `json:"at"`
}

type turnRow struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
	RuleID string `json:"ruleId,omitempty"`
	Tier   string `json:"tier,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type sessionExport struct {
	sessionRow
	Turns       []turnRow       `json:"turns"`
	Messages    []messageRow    `json:"messages"`
	Checkpoints []checkpointRow `json:"checkpoints"`
}

func loadSession(ctx context.Context, s *sessions.Store, id string) (sessionExport, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return sessionExport{}, err
	}
	out := sessionExport{sessionRow: rowOf(sess), Turns: []turnRow{}, Messages: []messageRow{}, Checkpoints: []checkpointRow{}}
	turns, err := s.Turns(ctx, id)
	if err != nil {
		return out, err
	}
	for _, t := range turns {
		out.Turns = append(out.Turns, turnRow{ID: t.ID, Origin: t.Origin, RuleID: t.RuleID, Tier: t.Tier, Status: t.Status, Reason: t.Reason})
	}
	msgs, err := s.Messages(ctx, id)
	if err != nil {
		return out, err
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, messageRow{ID: m.ID, TurnID: m.TurnID, Role: string(m.Role), Body: m.Body, Tool: m.ToolName, ToolCallID: m.ToolCallID,
			OK: m.OK, SystemMade: m.SystemMade, At: m.CreatedAt.Format(time.RFC3339)})
	}
	cps, err := s.Checkpoints(ctx, id, 0)
	if err != nil {
		return out, err
	}
	for i := len(cps) - 1; i >= 0; i-- { // oldest first, as the conversation grew
		c := cps[i]
		out.Checkpoints = append(out.Checkpoints, checkpointRow{ID: c.ID, CoveredTo: c.CoveredTo, CoveredTurns: c.CoveredTurns, Tainted: c.Tainted, Summary: c.Summary, At: c.CreatedAt.Format(time.RFC3339)})
	}
	return out, nil
}

// sessionFailure reports a failed command; an unknown id is a rejected input.
func (e *env) sessionFailure(cmd string, err error) int {
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(e.stderr, "applysmith agent sessions %s: no such conversation\n", cmd)
		return 2
	}
	return e.fail("agent sessions "+cmd, err)
}

func (e *env) sessionsShow(ctx context.Context, args []string) int {
	fs := e.flags("agent sessions show")
	ws, asJSON := fs.String("workspace", "", "workspace directory"), fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, agentUsage)
		return 2
	}
	return e.withSessions(ctx, "show", *ws, accessRead, func(s *sessions.Store) int {
		x, err := loadSession(ctx, s, pos[0])
		if err != nil {
			return e.sessionFailure("show", err)
		}
		if *asJSON {
			return e.printJSON(x)
		}
		fmt.Fprintf(e.stdout, "%s  %s  %s\n%s\n", x.ID, x.Channel, x.UpdatedAt, x.Title)
		for _, t := range x.Turns {
			rule := ""
			if t.RuleID != "" {
				rule = " rule " + t.RuleID
			}
			if t.Tier != "" {
				rule += " tier " + t.Tier
			}
			fmt.Fprintf(e.stdout, "turn %s  %s%s  %s %s\n", t.ID, t.Origin, rule, t.Status, t.Reason)
		}
		for _, c := range x.Checkpoints {
			fmt.Fprintf(e.stdout, "checkpoint %d covers %d turns up to message %d%s: %s\n", c.ID, c.CoveredTurns, c.CoveredTo, map[bool]string{true: " (tainted)"}[c.Tainted], oneLineText(c.Summary))
		}
		for _, m := range x.Messages {
			who := m.Role
			if m.Tool != "" {
				who += " " + m.Tool
			}
			fmt.Fprintf(e.stdout, "[%d] %s: %s\n", m.ID, who, oneLineText(m.Body))
		}
		return 0
	})
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

func (e *env) sessionsExport(ctx context.Context, args []string) int {
	fs := e.flags("agent sessions export")
	ws, out := fs.String("workspace", "", "workspace directory"), fs.String("out", "", "file to write; standard output when empty")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, agentUsage)
		return 2
	}
	return e.withSessions(ctx, "export", *ws, accessRead, func(s *sessions.Store) int {
		x, err := loadSession(ctx, s, pos[0])
		if err != nil {
			return e.sessionFailure("export", err)
		}
		if *out == "" {
			return e.printJSON(x)
		}
		b, err := json.MarshalIndent(x, "", "  ")
		if err != nil {
			return e.fail("agent sessions export", err)
		}
		if err := os.WriteFile(*out, append(b, '\n'), 0o600); err != nil {
			return e.fail("agent sessions export", err)
		}
		fmt.Fprintf(e.stdout, "wrote %d messages to %s\n", len(x.Messages), *out)
		return 0
	})
}

func (e *env) sessionsDelete(ctx context.Context, args []string) int {
	fs := e.flags("agent sessions delete")
	ws := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, agentUsage)
		return 2
	}
	return e.withSessions(ctx, "delete", *ws, accessShared, func(s *sessions.Store) int {
		if err := s.Delete(ctx, pos[0]); err != nil {
			return e.sessionFailure("delete", err)
		}
		fmt.Fprintf(e.stdout, "deleted %s\n", pos[0])
		return 0
	})
}

// agentLogin runs pi attached to this terminal for the user's own sign-in.
func (e *env) agentLogin(args []string) int {
	fs := e.flags("agent login")
	ws := fs.String("workspace", "", "workspace directory")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail("agent login", err)
	}
	cmd, err := agent.LoginCommand(e.getenv("APPLYSMITH_PI_PATH"), filepath.Join(root, store.StateDir), os.Environ())
	if err != nil {
		return e.fail("agent login", err)
	}
	fmt.Fprintln(e.stderr, "pi starts next. Type /login, choose your provider and sign in, then /exit. Then route the agent to it, for example: applysmith llm set agent-kernel pi:openai-codex/<model>")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.stdout, e.stderr
	if err := e.runInteractive(cmd); err != nil {
		return e.fail("agent login", err)
	}
	return 0
}

func (e *env) runInteractive(cmd *exec.Cmd) error {
	if e.interactive != nil {
		return e.interactive(cmd)
	}
	return cmd.Run()
}
