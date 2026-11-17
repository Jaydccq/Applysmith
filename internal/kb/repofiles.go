package kb

import (
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// Repository file bounds.
const (
	maxRepoFiles      = 80
	maxRepoFileBytes  = 128 << 10
	repoFileChars     = 4000
	repoTopFileChars  = 8000
	repoTopFiles      = 12
	repoTotalChars    = 60000
	storedBlobChars   = repoTopFileChars
	maxStoriesPerRepo = 6
)

var deniedDirs = setOf(
	"node_modules", "bower_components", "vendor", "third_party", "dist", "build", "out", "target", "coverage", ".git", ".next", ".nuxt",
	".cache", "__pycache__", ".venv", "venv", "virtualenv", ".tox", ".gradle", ".idea", ".terraform", "pods", "site-packages", ".pytest_cache",
	".mypy_cache", ".svelte-kit", ".parcel-cache", "bin", "obj",
)

var deniedFileNames = setOf(
	"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "cargo.lock", "go.sum", "poetry.lock", "gemfile.lock",
	"composer.lock", "pipfile.lock", "flake.lock", "mix.lock", "pubspec.lock", "gradle.lockfile", ".ds_store",
)

var deniedExts = setOf(
	// binaries
	".exe", ".dll", ".so", ".dylib", ".bin", ".class", ".jar", ".war", ".o", ".a", ".lib", ".pyc", ".pyo", ".wasm", ".node", ".dat", ".db", ".sqlite", ".pdf",
	// images, audio, video
	".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".bmp", ".tiff", ".psd", ".mp3", ".wav", ".flac", ".ogg", ".mp4", ".mov", ".avi", ".mkv", ".webm",
	// archives and fonts
	".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar", ".woff", ".woff2", ".ttf", ".otf", ".eot",
	// logs, source maps, generated lock-like files
	".log", ".map", ".lock", ".pem", ".key", ".p12", ".pfx",
)

var manifestNames = setOf(
	"package.json", "go.mod", "pyproject.toml", "cargo.toml", "makefile", "requirements.txt", "pom.xml", "build.gradle", "build.gradle.kts",
	"gemfile", "setup.py", "setup.cfg", "cmakelists.txt", "tsconfig.json", "pubspec.yaml", "composer.json", "mix.exs", "deno.json", "justfile",
	"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml",
)

var sourceExts = setOf(
	".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".py", ".rb", ".rs", ".java", ".kt", ".kts", ".swift", ".c", ".h", ".cc", ".cpp", ".hpp",
	".cs", ".php", ".scala", ".sh", ".bash", ".sql", ".proto", ".lua", ".ex", ".exs", ".erl", ".hs", ".ml", ".dart", ".r", ".vue", ".svelte", ".tf",
	".graphql", ".gql",
)

var docExts = setOf(".md", ".mdx", ".rst", ".txt", ".adoc")

// skipReason names why a path is not read. An empty reason means the path is
// allowed. Deny rules come first and anything not explicitly allowed is out.
func skipReason(p string, size int64) string {
	if !safeRepoPath(p) {
		return "denied unsafe path"
	}
	parts := strings.Split(p, "/")
	base := strings.ToLower(parts[len(parts)-1])
	for _, d := range parts[:len(parts)-1] {
		if deniedDirs[strings.ToLower(d)] {
			return "denied directory " + d
		}
	}
	ext := path.Ext(base)
	switch {
	case deniedFileNames[base]:
		return "lockfile"
	case strings.Contains(base, ".min.") || strings.HasSuffix(base, ".min"):
		return "minified file"
	case isPrivateEnv(base):
		return "private environment file"
	case deniedExts[ext]:
		return "denied file type " + ext
	}
	switch {
	case isReadme(base), isProjectDoc(base), manifestNames[base], strings.HasPrefix(base, "dockerfile"), base == ".env.example",
		strings.HasPrefix(base, ".env.") && (strings.HasSuffix(base, ".example") || strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".template")):
	case docExts[ext] && (len(parts) == 1 || strings.EqualFold(parts[0], "docs") || strings.EqualFold(parts[0], "doc")):
	case sourceExts[ext]:
	case (ext == ".yml" || ext == ".yaml") && strings.HasPrefix(p, ".github/workflows/"):
	default:
		return "not an allowed file"
	}
	if size > maxRepoFileBytes {
		return "larger than 128 KiB"
	}
	return ""
}

// safeRepoPath refuses a path that is absolute, climbs out with "..", has an
// empty segment or holds a NUL or control character.
func safeRepoPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func isPrivateEnv(base string) bool {
	if strings.HasSuffix(base, ".env") {
		return true
	}
	if base != ".env" && !strings.HasPrefix(base, ".env.") {
		return false
	}
	for _, ok := range []string{".example", ".sample", ".template"} {
		if strings.HasSuffix(base, ok) {
			return false
		}
	}
	return true
}

// docName reports whether base is one of the named documents: the bare name or
// the name followed by a dotted part (readme.zh.md), with no extension or a
// documentation one. A name that merely starts with it (readme-secrets.env)
// does not count.
func docName(base string, names ...string) bool {
	ext := path.Ext(base)
	if ext != "" && !docExts[ext] {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	for _, n := range names {
		if stem == n || strings.HasPrefix(stem, n+".") {
			return true
		}
	}
	return false
}

func isReadme(base string) bool { return docName(base, "readme") }
func isProjectDoc(base string) bool {
	return docName(base, "contributing", "changelog", "changes", "history", "architecture", "design")
}

// rankFile is the deterministic order in which allowed files are read when
// there are more than the cap: the README, manifests and documentation first,
// then source files nearest the root, larger ones before tiny ones.
func rankFile(p string, size int64) int {
	parts := strings.Split(p, "/")
	base := strings.ToLower(parts[len(parts)-1])
	depth := len(parts) - 1
	switch {
	case isReadme(base) && depth == 0:
		return 1000
	case manifestNames[base] || strings.HasPrefix(base, "dockerfile"):
		return 900 - depth*10
	case isProjectDoc(base):
		return 800 - depth*10
	case docExts[path.Ext(base)]:
		return 700 - depth*10
	}
	score := 500 - depth*40
	if strings.Contains(base, "_test.") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		score -= 150
	}
	switch {
	case size >= 2000 && size <= 40000:
		score += 20
	case size < 300:
		score -= 30
	}
	return score
}

// repoEntry is one file of a commit's tree.
type repoEntry struct {
	Path string
	SHA  string
	Size int64
}

type rankedEntry struct {
	repoEntry
	rank int
}

// selectFiles applies the deny and allow rules, ranks what is allowed and
// keeps the first maxRepoFiles. Skipped paths are reported with a reason.
func selectFiles(entries []repoEntry) (chosen []repoEntry, skipped []SkippedFile) {
	var allowed []rankedEntry
	for _, e := range entries {
		if reason := skipReason(e.Path, e.Size); reason != "" {
			if reason != "not an allowed file" && !strings.HasPrefix(reason, "denied") {
				skipped = append(skipped, SkippedFile{Path: e.Path, Reason: reason})
			}
			continue
		}
		allowed = append(allowed, rankedEntry{e, rankFile(e.Path, e.Size)})
	}
	sort.SliceStable(allowed, func(i, j int) bool {
		if allowed[i].rank != allowed[j].rank {
			return allowed[i].rank > allowed[j].rank
		}
		return allowed[i].Path < allowed[j].Path
	})
	for i, a := range allowed {
		if i < maxRepoFiles {
			chosen = append(chosen, a.repoEntry)
		} else {
			skipped = append(skipped, SkippedFile{Path: a.Path, Reason: "file cap"})
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	return chosen, skipped
}

// looksBinary reports content that is not text: a NUL byte, invalid UTF-8 or a
// high share of control characters.
func looksBinary(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	control := 0
	for _, c := range head {
		if c == 0 {
			return true
		}
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' {
			control++
		}
	}
	if control*100 > len(head)*30 {
		return true
	}
	return !utf8.Valid(b)
}
