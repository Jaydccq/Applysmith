package cli

import (
	"fmt"

	"applysmith/internal/extension"
)

const extensionUsage = `usage:
  applysmith extension export <dir>   write the unpacked browser extension into an empty directory

Load the directory from the browser's extensions page (developer mode, Load unpacked).
`

// extensionCmd writes the extension embedded in this binary.
func (e *env) extensionCmd(args []string) int {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprint(e.stderr, extensionUsage)
		return 2
	}
	fs := e.flags("extension export")
	ignoredWorkspace(fs)
	pos, err := parseMixed(fs, args[1:])
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, extensionUsage)
		return 2
	}
	dir := pos[0]
	if err := extension.Export(dir); err != nil {
		return e.fail("extension", err)
	}
	st, err := extension.Stamp()
	if err != nil {
		return e.fail("extension", err)
	}
	fmt.Fprintf(e.stdout, "wrote the extension (build %s) to %s\n", st, dir)
	return 0
}
