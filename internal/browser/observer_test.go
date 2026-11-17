package browser

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Requirement: Kept tabs are detached from automation (Quiet observer). A page
// can see a client enable the Runtime domain, so the observer's source may name
// no command that enables an event domain: the only protocol methods it may
// spell out are the ones that attach, detach, relay and evaluate.
func TestObserver_NeverEnablesAnEventDomain(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "observer.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"Runtime.evaluate": true, "Target.sendMessageToTarget": true,
		"Page.addScriptToEvaluateOnNewDocument": true, "Page.removeScriptToEvaluateOnNewDocument": true}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if strings.Contains(x.Sel.Name, "Enable") {
				t.Errorf("%s: %s", fset.Position(x.Pos()), x.Sel.Name)
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			s, _ := strconv.Unquote(x.Value)
			if (strings.HasPrefix(s, "Runtime.") || strings.HasPrefix(s, "Target.") || strings.HasPrefix(s, "Network.") ||
				strings.HasPrefix(s, "Fetch.") || strings.HasPrefix(s, "Page.") || strings.HasPrefix(s, "Log.")) && !allowed[s] {
				t.Errorf("%s: method %q", fset.Position(x.Pos()), s)
			}
		}
		return true
	})
}

func TestObserver_RefusesAnythingButItsFixedScripts(t *testing.T) {
	o := &Observer{scripts: ObserverScripts{IsArm: func(s string) bool { return s == "arm()" }, IsRead: func(e string) bool { return e == "read()" }}, dead: make(chan struct{})}
	// The arming script is installed, never evaluated in the page's world, and
	// a read is never installed.
	for _, expr := range []string{"", "arm()", "read();1", "document.title", "location.href='about:blank'"} {
		if _, err := o.Evaluate(context.Background(), expr); !isNotAllowed(err) {
			t.Errorf("%q: %v, want not-allowed", expr, err)
		}
	}
	for _, script := range []string{"", "arm() ", "read()", "document.title"} {
		if err := o.Install(context.Background(), script); !isNotAllowed(err) {
			t.Errorf("install %q: %v, want not-allowed", script, err)
		}
	}
}

func isNotAllowed(err error) bool {
	code, _ := CodeOf(err)
	return code == CodeNotAllowed
}
