package kb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ghFixture is a local repository served the way the GitHub REST API serves
// it. The commit, the tree and the blobs all come from a real git repository,
// so the API path and the clone path describe the same commit.
type ghFixture struct {
	t     *testing.T
	work  string
	bare  string
	srv   *httptest.Server
	owner string
	name  string

	mu        sync.Mutex
	hits      map[string]int
	sha       string // the commit the branch tip names
	truncated bool
	auth      []string
	inflight  int
	maxFlight int
	delay     time.Duration
	blobFail  int    // blob requests answered 403 with a rate-limit signal before success
	blobGone  string // a blob SHA answered 404 after the others were served
	served    int    // blobs answered with their content
	goneAfter int    // how many blobs are served before blobGone is answered
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Alex Example", "GIT_AUTHOR_EMAIL=alex@example.com",
		"GIT_COMMITTER_NAME=Alex Example", "GIT_COMMITTER_EMAIL=alex@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newGHFixture(t *testing.T, files map[string]string) *ghFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := &ghFixture{t: t, owner: "alex-example", name: "reef-tool", hits: map[string]int{}, work: t.TempDir()}
	gitOut(t, f.work, "init", "--quiet", "-b", "main")
	f.commit(files, nil)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// commit writes files, removes the named paths and commits.
func (f *ghFixture) commit(add map[string]string, remove []string) {
	for p, c := range add {
		full := filepath.Join(f.work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	for _, p := range remove {
		os.Remove(filepath.Join(f.work, filepath.FromSlash(p)))
	}
	gitOut(f.t, f.work, "add", "-A")
	gitOut(f.t, f.work, "commit", "--quiet", "--allow-empty", "-m", fmt.Sprintf("commit %d", time.Now().UnixNano()))
	f.mu.Lock()
	f.sha = gitOut(f.t, f.work, "rev-parse", "HEAD")
	f.mu.Unlock()
	f.refreshBare()
}

func (f *ghFixture) refreshBare() {
	if f.bare != "" {
		os.RemoveAll(f.bare)
	}
	f.bare = filepath.Join(f.t.TempDir(), "bare.git")
	gitOut(f.t, f.work, "clone", "--quiet", "--bare", f.work, f.bare)
	gitOut(f.t, f.bare, "config", "uploadpack.allowFilter", "true")
	gitOut(f.t, f.bare, "config", "uploadpack.allowAnySHA1InWant", "true")
}

func (f *ghFixture) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, v := range f.hits {
		if strings.HasPrefix(k, prefix) {
			n += v
		}
	}
	return n
}

func (f *ghFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	base := "/repos/" + f.owner + "/" + f.name
	write := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == base:
		write(map[string]any{"default_branch": "main"})
	case r.URL.Path == base+"/commits/main":
		f.mu.Lock()
		sha := f.sha
		f.mu.Unlock()
		write(map[string]any{"sha": sha})
	case strings.HasPrefix(r.URL.Path, base+"/git/trees/"):
		sha := strings.TrimPrefix(r.URL.Path, base+"/git/trees/")
		out := gitOut(f.t, f.work, "ls-tree", "-r", "-l", sha)
		var tree []map[string]any
		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			meta, p, _ := strings.Cut(line, "\t")
			fs := strings.Fields(meta)
			var size int64
			fmt.Sscan(fs[3], &size)
			tree = append(tree, map[string]any{"path": p, "type": fs[1], "sha": fs[2], "size": size})
		}
		f.mu.Lock()
		trunc := f.truncated
		f.mu.Unlock()
		if trunc && len(tree) > 1 {
			tree = tree[:1]
		}
		write(map[string]any{"tree": tree, "truncated": trunc})
	case strings.HasPrefix(r.URL.Path, base+"/git/blobs/"):
		f.blob(w, strings.TrimPrefix(r.URL.Path, base+"/git/blobs/"))
	default:
		http.NotFound(w, r)
	}
}

func (f *ghFixture) blob(w http.ResponseWriter, sha string) {
	f.mu.Lock()
	f.inflight++
	f.maxFlight = max(f.maxFlight, f.inflight)
	fail := f.blobFail > 0
	if fail {
		f.blobFail--
	}
	delay := f.delay
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inflight--; f.mu.Unlock() }()
	if delay > 0 {
		time.Sleep(delay)
	}
	if sha == f.blobGone {
		// Answer after the other blobs were served, however slow the machine.
		for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			f.mu.Lock()
			served := f.served
			f.mu.Unlock()
			if served >= f.goneAfter {
				break
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if fail {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	cmd := exec.Command("git", "cat-file", "blob", sha)
	cmd.Dir = f.work
	data, err := cmd.Output()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)})
	f.mu.Lock()
	f.served++
	f.mu.Unlock()
}

// service builds a Service over the fixture. model may be nil.
func (f *ghFixture) service(t *testing.T, mut ...func(*Options)) *fixture {
	fx := newFixture(t, mut...)
	fx.svc.ghBase, fx.svc.ghInsecure = f.srv.URL, true
	fx.svc.ghBackoff = time.Millisecond
	fx.svc.cloneSeam = &cloner{url: func(string, string) string { return "file://" + f.bare }, protocols: "file"}
	return fx
}

type fakeModel struct {
	mu      sync.Mutex
	reply   string
	replies []string
	prompts []string
	systems []string
	err     error
}

func (m *fakeModel) Generate(_ ctxT, system, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt)
	m.systems = append(m.systems, system)
	if m.err != nil {
		return "", m.err
	}
	if len(m.replies) > 0 {
		r := m.replies[0]
		if len(m.replies) > 1 {
			m.replies = m.replies[1:]
		}
		return r, nil
	}
	return m.reply, nil
}
