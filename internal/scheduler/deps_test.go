package scheduler

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenImports are the packages that drive an application: the apply
// chain, the fill engine, the browser extension and the captcha solver.
var forbiddenImports = []string{
	internal + "apply",
	internal + "engine",
	internal + "extension",
	internal + "captcha",
	internal + "answerbank",
}

const internal = "applysmith/internal/"

func TestScheduler_NoSubmit_DependencyGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "applysmith/internal/scheduler/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range forbiddenImports {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Errorf("the scheduler or a job-kind package imports %s", dep)
			}
		}
	}
}
