package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"applysmith/internal/api/wire"
)

// ConfigSnippet is the JSON an MCP client needs to start this server: the
// absolute path of the executable and the workspace only when it is not the
// default. It holds no secret.
func ConfigSnippet(exe, workspaceRoot string, nonDefault bool) ([]byte, error) {
	entry := map[string]any{"command": exe, "args": []string{"mcp"}}
	if nonDefault {
		entry["env"] = map[string]string{"APPLYSMITH_WORKSPACE": workspaceRoot}
	}
	return json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"applysmith": entry}}, "", "  ")
}

// Step is the result of one stage of the self-check.
type Step struct {
	Name   string
	Pass   bool
	Detail string
	Remedy string
}

// RunCheck walks the stages a client needs: the workspace, the server's lock
// file, the token and the health route. wsErr is the failure of resolving the
// workspace, if any; the walk then stops at its first stage.
func RunCheck(ctx context.Context, l Locator, wsErr error) []Step {
	if wsErr != nil {
		return []Step{{Name: "workspace", Detail: wsErr.Error(), Remedy: "Pass --workspace or set APPLYSMITH_WORKSPACE."}}
	}
	steps := []Step{{Name: "workspace", Pass: true, Detail: "resolved"}}
	port, found := l.Port()
	if !found {
		// No port is guessed: a fixed one may belong to another program.
		return append(steps, Step{Name: "server", Detail: "no server running for workspace " + l.Workspace.Root, Remedy: remedyStart})
	}
	steps = append(steps, Step{Name: "server", Pass: true, Detail: "the lock file names port " + strconv.Itoa(port)})
	switch _, err := l.Token(); {
	case err == nil:
		steps = append(steps, Step{Name: "token", Pass: true, Detail: "found"})
	case errors.Is(err, ErrNoToken):
		steps = append(steps, Step{Name: "token", Detail: "no API token is stored", Remedy: remedyPair})
	default:
		steps = append(steps, Step{Name: "token", Detail: err.Error(), Remedy: remedyPair})
	}
	var h wire.Health
	if err := l.Client().get(ctx, "/api/health", nil, false, &h); err != nil {
		var te *ToolError
		if errors.As(err, &te) {
			return append(steps, Step{Name: "health", Detail: te.Message, Remedy: te.Remedy})
		}
		return append(steps, Step{Name: "health", Detail: err.Error(), Remedy: remedyServer})
	}
	return append(steps, Step{Name: "health", Pass: true, Detail: string(h.Status) + ", version " + h.Version})
}

// WriteSteps prints one line per step and reports whether all passed.
func WriteSteps(w io.Writer, steps []Step) bool {
	ok := true
	for _, s := range steps {
		if s.Pass {
			fmt.Fprintf(w, "pass  %s: %s\n", s.Name, s.Detail)
			continue
		}
		ok = false
		fmt.Fprintf(w, "fail  %s: %s. %s\n", s.Name, s.Detail, s.Remedy)
	}
	return ok
}
