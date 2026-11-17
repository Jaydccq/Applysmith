//go:build !windows

package kb

import "os/exec"

func exec_pgrep(pattern string) (string, error) {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	return string(out), err
}
