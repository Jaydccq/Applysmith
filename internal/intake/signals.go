package intake

import (
	"strings"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// A signal document (course outline, repository README, project description,
// reference) names technologies it covers. That says what the material is
// about, not what the person can do, so each hit becomes a question the person
// answers. The list is deliberately short: a technology it misses is not
// suggested, which costs a prompt and never a false fact.
var knownTerms = strings.Fields(`
Angular Ansible Airflow Android Apache Bazel Bootstrap C# C++ Cassandra Django Docker Elasticsearch Elixir FastAPI Flask Flutter
Golang GraphQL Gradle Hadoop Hibernate Jenkins Jest Kafka Keras Kotlin Kubernetes Laravel MongoDB MySQL NestJS Next.js Node.js
NumPy Nuxt OpenCV Pandas PostgreSQL Prometheus PyTorch Pytest RabbitMQ Rails React Redis Redux Rust Scala Scikit-learn Selenium
Spark SQLAlchemy Svelte Swift SwiftUI Tailwind TensorFlow Terraform TypeScript Vue Vue.js Webpack Xamarin
`)

// knownSet is matched case-sensitively: words such as "go" or "spring" are
// everyday English, and a technology is written with its capitals.
var knownSet = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range knownTerms {
		m[t] = true
	}
	return m
}()

var (
	termToken   = lazyregexp.New(`[A-Za-z][A-Za-z0-9+#.\-]*[A-Za-z0-9+#]|[A-Za-z]`)
	signalHeads = lazyregexp.New(`(?i)^#{1,6}\s+.*\b(?:skills|technologies|tools|stack|frameworks|libraries|built with)\b`)
	anyHeading  = lazyregexp.New(`^(#{1,6})\s+(.*)$`)
)

// termKey folds a technology name to the key skills compare by.
func termKey(s string) string { return strings.Join(strings.Fields(identity.Fold(s)), " ") }

// signalHit is one technology a signal document names, where first.
type signalHit struct {
	Term    string
	Line    int
	Section string
}

var skillSepRE = lazyregexp.New(`[,;|]`)

// signalTerms lists the technologies a document names, once each, in the order
// they first appear. Under a heading that lists skills, tools or a stack, every
// short comma-separated item counts too.
func signalTerms(text string) []signalHit {
	var out []signalHit
	seen := map[string]bool{}
	add := func(term string, line int, section string) {
		k := termKey(term)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, signalHit{Term: term, Line: line, Section: section})
	}
	section, listing := "", false
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if m := anyHeading.FindStringSubmatch(line); m != nil {
			section, listing = strings.TrimSpace(m[2]), signalHeads.MatchString(line)
			continue
		}
		if line == "" {
			continue
		}
		for _, tok := range termToken.FindAllString(line, -1) {
			tok = strings.TrimRight(tok, ".")
			if knownSet[tok] {
				add(tok, i+1, section)
			}
		}
		if listing {
			item := strings.TrimLeft(line, "-*+ ")
			if _, rest, ok := strings.Cut(item, ":"); ok && len(item) < 80 {
				item = rest
			}
			for _, p := range skillSepRE.Split(item, -1) {
				p = strings.TrimSpace(strings.NewReplacer("*", "", "_", "").Replace(p))
				if p != "" && len(p) <= 40 && len(strings.Fields(p)) <= 3 {
					add(p, i+1, section)
				}
			}
		}
	}
	return out
}
