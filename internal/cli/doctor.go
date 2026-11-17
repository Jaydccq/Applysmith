package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"applysmith/internal/onboarding"
)

// doctor runs the checks and exits 1 when a required one failed, 4 when it
// could not run at all, 2 on a usage error.
func (e *env) doctor(ctx context.Context, args []string) int {
	port, err := e.defaultPort()
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith doctor: %v\n", err)
		return onboarding.ExitUnable
	}
	fs := e.flags("doctor")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	only := fs.String("only", "", "run one step or one check, for example workspace or workspace.files")
	live := fs.Bool("live", false, "allow checks that use the network")
	offline := fs.Bool("offline", false, "never use the network, as without --live; refuses --live")
	fs.IntVar(&port, "port", port, "port the server is expected on")
	if pos, err := parseMixed(fs, args); err != nil || len(pos) > 0 {
		if err == nil {
			fmt.Fprintf(e.stderr, "applysmith doctor: unexpected argument %q\n", pos[0])
		}
		return 2
	}
	if *live && *offline {
		fmt.Fprintln(e.stderr, "applysmith doctor: --live and --offline exclude each other")
		return 2
	}

	denv := e.onboardingEnv(*wsFlag)
	denv.Port = port
	if closeFn, err := denv.Open(ctx, false); err == nil {
		defer closeFn()
	} else {
		fmt.Fprintf(e.stderr, "applysmith doctor: the workspace database could not be opened read-only: %v\n", err)
	}
	rep, err := onboarding.Run(ctx, onboarding.Checks(), denv, onboarding.Options{Only: *only, Live: *live && !*offline})
	switch {
	case errors.Is(err, onboarding.ErrUnknownCheck):
		fmt.Fprintf(e.stderr, "applysmith doctor: %v\n", err)
		return 2
	case err != nil:
		fmt.Fprintf(e.stderr, "applysmith doctor: %v\n", err)
		return onboarding.ExitUnable
	}
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return onboarding.ExitUnable
		}
	} else {
		onboarding.WriteText(e.stdout, rep)
	}
	return rep.ExitCode()
}
