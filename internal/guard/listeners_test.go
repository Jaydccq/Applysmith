package guard

import (
	"slices"
	"testing"
)

func TestLoopbackListeners(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"loopback literals are clean", map[string]string{
			"internal/a/a.go":      "l, err := net.Listen(\"tcp\", \"127.0.0.1:0\")\n",
			"internal/a/a_test.go": "l, err := net.Listen(\"tcp6\", \"[::1]:0\")\n",
		}, nil},
		{"the bind helper may listen on a validated host", map[string]string{
			listenHelperPath: "ln, err := net.Listen(\"tcp\", net.JoinHostPort(host, port))\n",
		}, nil},
		{"a Unix-domain socket only in a test", map[string]string{
			"internal/a/a_test.go": "ln, err := net.Listen(\"unix\", sock)\n",
			"internal/b/b.go":      "ln, err := net.Listen(\"unix\", sock)\n",
		}, []string{"internal/b/b.go"}},
		{"wildcard and unspecified addresses", map[string]string{
			"internal/a/a.go": "net.Listen(\"tcp\", \":8080\")\n",
			"internal/b/b.go": "net.Listen(\"tcp\", \"0.0.0.0:0\")\n",
		}, []string{"internal/a/a.go", "internal/b/b.go"}},
		{"a computed address outside the helper", map[string]string{
			"internal/a/a.go": "net.Listen(\"tcp\", addr)\n",
		}, []string{"internal/a/a.go"}},
		{"a LAN-style listener in a test file", map[string]string{
			"cmd/applysmith/x_test.go": "net.Listen(\"tcp\", \"192.168.1.5:47320\")\n",
		}, []string{"cmd/applysmith/x_test.go"}},
		{"other ways to open a server socket", map[string]string{
			"internal/a/a.go": "http.ListenAndServe(addr, nil)\n",
			"internal/b/b.go": "var lc net.ListenConfig\n",
			"internal/c/c.go": "tls.Listen(\"tcp\", a, cfg)\n",
			"internal/d/d.go": "srv.ServeTLS(ln, \"\", \"\")\n",
			"internal/e/e.go": "http.Serve(ln, h)\n",
		}, []string{"internal/a/a.go", "internal/b/b.go", "internal/c/c.go", "internal/d/d.go", "internal/e/e.go"}},
		{"comments, the guard package and non-Go files are not read", map[string]string{
			"internal/a/a.go":          "// never call net.Listen(\"tcp\", \":80\") here\n",
			"internal/guard/g.go":      "net.Listen(\"tcp\", \":80\")\n",
			"docs/architecture/net.md": "net.Listen(\"tcp\", \":80\")\n",
		}, nil},
		{"a call named inside a string is text, not a call", map[string]string{
			"internal/a/a_test.go": "banned := []string{\"net.Listen\", \"ListenAndServe\", `http.Serve(`}\n",
		}, nil},
		{"a real call beside a string literal is still found", map[string]string{
			"internal/a/a.go": "log.Print(\"start\"); http.ListenAndServe(addr, nil)\n",
		}, []string{"internal/a/a.go"}},
		{"serve on a loopback listener is fine", map[string]string{
			"internal/a/a.go": "go func() { _ = srv.Serve(ln) }()\n",
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found, err := checkLoopbackListeners(dirTree(t, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range found {
				if f.Rule != ruleLoopbackListener || f.Line != 1 {
					t.Fatalf("finding = %v", f)
				}
				got = append(got, f.Path)
			}
			if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(tc.want))) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}
