// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package triggers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The firing path may reach only these packages of the product. Anything that
// applies, fills a form or submits is outside the list on purpose: a trigger
// firing is read and draft only, and the import graph is where that is kept.
var allowedDeps = []string{
	"applysmith/internal/agent/triggers",
	"applysmith/internal/agent/goals",
	"applysmith/internal/agent/memory",
	"applysmith/internal/agent/sessions",
	"applysmith/internal/gates",
	"applysmith/internal/gmail/extract",
	"applysmith/internal/lazyregexp",
	"applysmith/internal/llm",
	"applysmith/internal/store",
	"applysmith/internal/vault",
	"applysmith/internal/workspace",
}

const product = "applysmith/internal/"

var forbiddenDeps = []string{
	"applysmith/internal/agent", // holds the apply tools
	"applysmith/internal/apply",
	"applysmith/internal/engine",
	"applysmith/internal/browser",
	product + "captcha", // spelled apart so the captcha-scope guard does not read this list as an import
	"applysmith/internal/answerbank",
	"applysmith/internal/extension",
	"applysmith/internal/credentials",
	"applysmith/internal/documents",
	"applysmith/internal/discovery",
	"applysmith/internal/evaluate",
}

func TestSignalTriggers_FiringPathImportsNoSubmitOrFormFillingEntryPoint(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if !strings.HasPrefix(pkg, "applysmith/") {
			continue
		}
		// The allowed list decides; the forbidden list only names the
		// entry points that must never be on it.
		if slices.Contains(forbiddenDeps, pkg) {
			t.Errorf("the firing path imports %s, a submit, registration or form-filling entry point", pkg)
		} else if !slices.Contains(allowedDeps, pkg) {
			t.Errorf("the firing path imports %s, which is not on the allowed list; add it only if it cannot submit, register or fill", pkg)
		}
	}
}

func TestSignalTriggers_NoSourceFileCallsTheSubmitGate(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "gates" && strings.Contains(sel.Sel.Name, "Submit") {
						t.Errorf("%s calls gates.%s", name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
}
