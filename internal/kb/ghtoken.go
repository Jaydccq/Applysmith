package kb

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GHCLIToken asks the user's installed `gh` command for its token. It returns
// "" when gh is not installed, is not signed in, or fails. The command line is
// fixed and no prompt can appear.
func GHCLIToken(ctx context.Context) string {
	path, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "auth", "token")
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	cmd.SysProcAttr = groupAttr()
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	tok := strings.TrimSpace(out.String())
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") || len(tok) > 512 {
		return ""
	}
	return tok
}
