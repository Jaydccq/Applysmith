package documents

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"applysmith/internal/kb"
	"applysmith/internal/lazyregexp"
)

// nameTok is a word as written; initial is true when it opens a sentence, a
// line or a list item.
type nameTok struct {
	text    string
	initial bool
}

// nameTokens splits text into words and keeps names with punctuation whole:
// C++, C#, .NET, Node.js.
func nameTokens(s string) []nameTok {
	rs := []rune(s)
	isAl := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	var out []nameTok
	var cur []rune
	start := true
	flush := func() {
		if len(cur) > 0 {
			out = append(out, nameTok{string(cur), start})
			start = false
			cur = cur[:0]
		}
	}
	for i, r := range rs {
		var prev, next rune
		if i > 0 {
			prev = rs[i-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch {
		case isAl(r):
			cur = append(cur, r)
		case (r == '+' || r == '#') && len(cur) > 0 && (isAl(prev) || prev == '+'):
			cur = append(cur, r)
		case r == '.' && len(cur) > 0 && isAl(prev) && isAl(next):
			cur = append(cur, r)
		case r == '.' && len(cur) == 0 && unicode.IsLetter(next) && !isAl(prev):
			cur = append(cur, r)
		case (r == '\'' || r == '’') && len(cur) > 0 && isAl(prev) && isAl(next):
			cur = append(cur, r)
		case r == '-' && isAl(next) && slices.Contains(verbPrefixes, strings.ToLower(string(cur))):
			// co-led, re-architected: the prefix and the verb are one word.
			cur = append(cur, r)
		default:
			flush()
			if r == '.' || r == '!' || r == '?' || r == ':' || r == '\n' || r == '•' {
				start = true
			}
		}
	}
	flush()
	return out
}

// aliases fold the spellings of one technology to one key.
var aliases = map[string]string{"golang": "go", "postgres": "postgresql", "k8s": "kubernetes", "js": "javascript", "ts": "typescript",
	"nodejs": "node", "reactjs": "react", "vuejs": "vue", "nextjs": "next"}

// apostrophes drops the apostrophes of a token.
var apostrophes = strings.NewReplacer("'", "", "’", "")

// key is the whole-token form two spellings of one word share. It never
// matches a part of another word: java is not javascript, go is not good.
func key(tok string) string {
	t := strings.ToLower(tok)
	for _, suf := range []string{"'s", "’s"} {
		t = strings.TrimSuffix(t, suf)
	}
	t = apostrophes.Replace(t)
	if !strings.HasPrefix(t, ".") {
		t = strings.ReplaceAll(t, ".", "")
	}
	if a, ok := aliases[t]; ok {
		return a
	}
	return singular(t)
}

// plainWordTech are the technologies whose key is also an ordinary English
// word ("before the next release", "on every node", "react faster"). They are
// watched, and count as known, only in a spelling that names the technology:
// an alias with a js ending ("Next.js", "NodeJS") or a capital in mid-sentence.
// A capital that merely opens a sentence is not that spelling.
var plainWordTech = stringSet("next", "react", "node")

func techSpelling(t nameTok) bool {
	if strings.HasSuffix(strings.ToLower(strings.ReplaceAll(t.text, ".", "")), "js") {
		return true
	}
	return !t.initial && unicode.IsUpper([]rune(t.text)[0])
}

func hasLetter(s string) bool { return strings.IndexFunc(s, unicode.IsLetter) >= 0 }

// isStop is true for function words and single characters.
func isStop(w string) bool {
	w = strings.ToLower(w)
	if i := strings.IndexAny(w, "'’"); i > 0 {
		w = w[:i]
	}
	return len(kb.Tokens(w)) == 0
}

// openerVerbs are base forms a résumé line or a review note may open with;
// past forms are caught by pastVerb.
var openerVerbs = stringSet("lead", "build", "own", "manage", "design", "drive", "create", "develop", "deliver", "run", "write", "ship", "launch",
	"improve", "reduce", "increase", "mentor", "migrate", "automate", "implement", "maintain", "optimize", "architect", "partner", "collaborate",
	"coordinate", "support", "scale", "define", "establish", "introduce", "analyze", "resolve", "cut", "act", "work", "help", "grow", "raise",
	"consider", "confirm", "mention", "replace", "add", "tighten", "keep", "remove", "move", "drop", "fold", "merge", "shorten", "trim", "name",
	"state", "cite", "say", "give", "let", "decide", "avoid", "check", "anchor", "treat", "pick", "leave", "vary")

// openers are words that start a sentence of a letter or a line of a résumé
// without being a name.
var openers = stringSet("dear", "hello", "hi", "thank", "thanks", "regards", "sincerely", "best", "kind", "warm", "respectfully", "yours", "welcome",
	"additionally", "furthermore", "moreover", "finally", "overall", "together", "today", "recently", "currently", "previously", "meanwhile",
	"alongside", "beyond", "outside", "inside", "since", "whether", "either", "neither", "none", "many", "much", "most", "several", "every", "each", "another", "such",
	"strong", "proven", "solid", "deep", "hands", "cross", "full", "high", "large", "small", "new", "early", "daily", "weekly", "senior", "junior",
	"technical", "key", "core", "major", "primary", "first", "second", "third", "last", "next", "other", "same", "more", "less", "fewer",
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "twenty", "thirty", "forty", "fifty",
	"hundred", "thousand", "million", "please", "yes", "no", "now", "here", "there", "because", "although", "though", "however", "therefore",
	"throughout", "across", "against", "among", "toward", "towards", "per", "via", "using", "like", "given", "based", "both", "all", "any", "some",
	"responsible", "accountable", "instrumental", "experience", "knowledge", "ability", "familiarity", "understanding", "proficiency", "expertise", "background", "degree", "bachelor", "master")

// builtinTech is the floor of the technology vocabulary: names a draft may
// never use unless the user's own sources hold them, even when the posting
// does not mention them and the draft spells them in lower case. Words that
// are also plain English (go, swift, spark, spring, oracle) are left out; the
// posting's own spelling catches those.
var builtinTech = stringSet("kubernetes", "k8s", "terraform", "ansible", "docker", "jenkins", "aws", "gcp", "azure", "kafka", "rabbitmq", "cassandra",
	"redis", "memcached", "mongodb", "postgresql", "postgres", "mysql", "mariadb", "sqlite", "snowflake", "bigquery", "redshift", "elasticsearch",
	"kibana", "grafana", "prometheus", "datadog", "splunk", "hadoop", "airflow", "dbt", "flink", "react", "angular", "vue", "svelte", "nextjs",
	"django", "laravel", "springboot", "dotnet", "java", "javascript", "typescript", "python", "ruby", "php", "scala", "kotlin", "rust", "golang",
	"perl", "haskell", "elixir", "erlang", "clojure", "cobol", "fortran", "graphql", "grpc", "protobuf", "nginx", "envoy", "istio", "argocd",
	"pulumi", "cloudformation", "dynamodb", "cockroachdb", "neo4j", "tensorflow", "pytorch", "pandas", "numpy", "linux", "gitlab", "github",
	"bitbucket", "jira", "salesforce", "tableau", "powerbi", "webpack", "tailwind", "nodejs", "deno", "flutter", "opencv", "kubeflow", "mlflow",
	"databricks", "selenium", "cypress", "playwright", "junit", "pytest", "c++", "c#", ".net", "node.js",
	"clickhouse", "supabase", "firebase", "vercel", "netlify", "heroku", "cloudflare", "fastly", "akamai", "twilio", "okta", "auth0", "keycloak",
	"packer", "vagrant", "kustomize", "tekton", "circleci", "teamcity", "sonarqube", "artifactory", "podman", "containerd", "kubectl", "openshift",
	"rancher", "eks", "gke", "aks", "ec2", "s3", "fargate", "cloudwatch", "cloudtrail", "timescaledb", "influxdb", "questdb", "pinot", "trino",
	"impala", "samza", "pulsar", "activemq", "kinesis", "eventbridge", "debezium", "airbyte", "fivetran", "dagster", "prefect",
	"luigi", "metaflow", "sagemaker", "huggingface", "langchain", "llamaindex", "pinecone", "weaviate", "milvus", "qdrant", "faiss", "sklearn",
	"xgboost", "lightgbm", "keras", "onnx", "cuda", "spacy", "nltk", "matplotlib", "seaborn", "plotly", "streamlit", "jupyter", "pnpm", "vite",
	"rollup", "babel", "eslint", "vitest", "storybook", "redux", "mobx", "nuxt", "gatsby", "fastify", "nestjs", "fastapi", "celery", "gunicorn",
	"uvicorn", "sinatra", "symfony", "wordpress", "drupal", "magento", "shopify", "hasura", "prisma", "sequelize", "typeorm", "hibernate",
	"mybatis", "sqlalchemy", "alembic", "flyway", "liquibase", "testng", "mockito", "jmeter", "gatling", "appium", "puppeteer",
	"openapi", "xamarin", "gradle", "bazel", "cmake", "llvm", "tokio", "avro", "parquet", "hudi", "metabase",
	"superset", "jaeger", "zipkin", "opentelemetry", "newrelic", "dynatrace", "pagerduty", "opsgenie", "rollbar", "bugsnag", "launchdarkly",
	"mixpanel", "amplitude", "zendesk", "hubspot", "marketo", "netsuite", "servicenow", "snowplow", "memcache", "couchbase", "scylla",
	"mongoose", "sybase", "teradata", "informatica", "talend", "logstash", "fluentd", "fluentbit", "vmware", "openstack", "cloudera",
	"nifi", "zookeeper", "etcd", "haproxy", "traefik", "linkerd", "calico", "cilium")

// names are what a draft may and may not name. known holds the keys of the
// user's own sources; watch holds the keys the posting and the technology
// floor name, which are checked wherever they appear, in any case; plain holds
// the words the posting writes in lower case; caps holds the watched keys the
// posting spells only in capitals ("AM", additive manufacturing), which a draft
// names only in capitals: "I am" is another word.
type names struct {
	known map[string]bool
	watch map[string]bool
	plain map[string]bool
	caps  map[string]bool
}

// userTexts are the sources the user owns: the CV and profile lines, the
// knowledge snippets the manifest kept, the dated entries and the contact
// block. The posting and company context are not among them.
func (m Manifest) userTexts() []string {
	var out []string
	for _, e := range m.Evidence {
		out = append(out, e.Text)
	}
	for _, e := range m.Entries {
		out = append(out, e.Title, e.Organization, e.Location, e.Note)
	}
	c := m.Contact
	out = append(out, c.FullName, c.Location)
	for _, l := range c.Links {
		out = append(out, l.Label)
	}
	return out
}

// names builds the vocabulary of a gate. Extra texts count as known: the
// target company and role and the company facts of a letter, the lines of a
// draft being reviewed.
func (m Manifest) names(extra ...string) names {
	n := names{known: map[string]bool{}, watch: map[string]bool{}, plain: map[string]bool{}, caps: map[string]bool{}}
	capsOnly, spelled := map[string]bool{}, map[string]bool{}
	watchSpelling := func(text string) {
		k := key(text)
		n.watch[k] = true
		if allCaps(text) {
			capsOnly[k] = true
		} else {
			spelled[k] = true
		}
	}
	for _, t := range append(m.userTexts(), extra...) {
		for _, w := range nameTokens(t) {
			if k := key(w.text); !plainWordTech[k] || techSpelling(w) {
				n.known[k] = true
			}
		}
	}
	for k := range builtinTech {
		n.watch[key(k)] = true
		spelled[key(k)] = true
	}
	// The role's own words are what the posting asks the applicant to be
	// ("Full-Stack" in "Junior Full-Stack Software Engineer"): one that no
	// user source holds is watched outside the role's name, which
	// withoutReferences blanks. The seniority and the job noun are not.
	for _, w := range nameTokens(m.roleTitle()) {
		if low := strings.ToLower(w.text); len(low) > 2 && !isStop(low) && !genericWords[low] && !roleNouns[low] && !seniority[low] {
			watchSpelling(w.text)
		}
	}
	jd := append([]string{}, m.NiceToHave...)
	for _, r := range m.Requirements {
		jd = append(jd, r.Text)
	}
	for _, t := range jd {
		for _, w := range nameTokens(t) {
			if w.text == strings.ToLower(w.text) {
				n.plain[key(w.text)] = true
			}
		}
	}
	// A keyword is one bare word ("impact", "startup"), not a list: only a
	// name-shaped keyword is watched; listedTech reads the posting's lists.
	// A word whose only mark of a name is the capital that opens a line
	// ("Production experience", "Track record") or a keyword (each is read
	// alone, so its capital may be a title's) is not watched for that: a
	// product the posting names mid-sentence, in a list or on the floor list
	// still is. A word whose only mark is a capital and that is plain English
	// (the posting also writes it in lower case, or it has an ordinary ending:
	// "New Jersey", "Computer Science, Engineering") is checked only where a
	// draft capitalizes it, like any capitalized word.
	for i, t := range append(jd, m.Keywords...) {
		for _, w := range nameTokens(t) {
			low := strings.ToLower(w.text)
			if capitalOnly(w.text) && (n.plain[key(w.text)] || englishEnding(low) || seniority[low]) {
				continue
			}
			if nameShaped(w) && !genericWords[low] && !openingCapital(w) {
				watchSpelling(w.text)
			}
		}
		if i >= len(jd) {
			continue
		}
		for _, w := range listedTech(t) {
			watchSpelling(w)
		}
	}
	for k := range capsOnly {
		if !spelled[k] {
			n.caps[k] = true
		}
	}
	return n
}

// allCaps is a token of two or more letters, every one a capital ("AM",
// "NDE").
func allCaps(s string) bool {
	letters := 0
	for _, c := range s {
		if !unicode.IsLetter(c) {
			continue
		}
		if !unicode.IsUpper(c) {
			return false
		}
		letters++
	}
	return letters >= 2
}

// summaryNames add the words of the user's target roles, so a summary may open
// "Backend engineer". A technology on the floor list stays out: a target role
// is a wish, not experience with it.
func (m Manifest) summaryNames() names {
	n := m.names()
	floor := map[string]bool{}
	for k := range builtinTech {
		floor[key(k)] = true
	}
	for _, role := range m.TargetRoles {
		for _, w := range nameTokens(role) {
			if k := key(w.text); !floor[k] {
				n.known[k] = true
			}
		}
	}
	return n
}

var (
	listSepRE = lazyregexp.New(`(?i)[,;/&]|\band\b|\bor\b`)
	introWord = stringSet("with", "in", "using", "of", "including", "like", "as", "on", "via")
	// englishEnds are endings of ordinary English words; a bare word that has
	// one is read as a skill or a quality, not a product.
	englishEnds = []string{"ing", "ion", "ions", "ment", "ness", "ity", "ship", "ous", "ive", "ful", "less", "able", "ible", "ly", "ed", "er", "or",
		"ist", "ics", "ence", "ance", "ize", "ise", "ate", "ure", "ual", "ary", "ory", "ial", "ic", "al", "s"}
)

// listedTech returns the lower-case single words a posting lists as the
// things it asks for ("experience with clickhouse, dagster and trino"). A word
// with an ordinary English ending, a function word or a verb is not a product.
func listedTech(line string) []string {
	var out []string
	for _, item := range listSepRE.Split(line, -1) {
		fields := strings.Fields(item)
		if len(fields) == 0 {
			continue
		}
		last := strings.Trim(fields[len(fields)-1], ".:()\"'")
		if len(fields) > 1 && !introWord[strings.ToLower(strings.Trim(fields[len(fields)-2], ".:()\"'"))] {
			continue
		}
		if techShaped(last) {
			out = append(out, last)
		}
	}
	return out
}

func techShaped(w string) bool {
	if len(w) < 3 || w != strings.ToLower(w) || !hasLetter(w) || isStop(w) || genericWords[w] || openers[w] || verbLike(w, true, false) {
		return false
	}
	for _, r := range w {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return !englishEnding(w)
}

func englishEnding(w string) bool {
	return slices.ContainsFunc(englishEnds, func(e string) bool { return strings.HasSuffix(w, e) })
}

var (
	// postingNounRE is the word a page title appends to a role ("... Engineer
	// Application"); the letter names the role without it.
	postingNounRE = lazyregexp.New(`(?i)\s+(?:job\s+)?(?:application|opening|posting|vacancy)$`)
	roleNouns     = stringSet("engineer", "developer", "programmer", "scientist", "analyst", "manager", "designer", "architect", "administrator",
		"specialist", "consultant", "lead", "intern", "researcher", "technician", "director", "officer", "associate", "sre")
	seniority = stringSet("junior", "senior", "staff", "principal", "mid", "entry", "level", "head", "chief", "trainee", "graduate", "new", "grad")
)

var companySuffixRE = lazyregexp.New(`(?i)[,\s]+(?:inc|llc|ltd|corp|corporation|co|gmbh|plc)\.?$`)

// referenceNames are the spellings of the role and the company that a letter
// may use to refer to them: the full text and the text without a trailing
// qualifier or company suffix.
func (m Manifest) referenceNames() []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	for _, role := range []string{m.Role, postingNounRE.ReplaceAllString(m.Role, "")} {
		add(role)
		for _, sep := range []string{"(", " - ", ",", "|", " \u2013 "} {
			parts := strings.Split(role, sep)
			if len(parts) < 2 {
				continue
			}
			add(parts[0])
			// A title after the company ("Flexi - Junior Full-Stack Software
			// Engineer") is a spelling of the role; any other segment
			// ("Platform Engineer - Kafka") could be a technology.
			for _, p := range parts[1:] {
				if f := strings.Fields(strings.TrimRight(p, ") ")); len(f) > 0 && roleNouns[strings.ToLower(f[len(f)-1])] {
					add(strings.TrimRight(p, ") "))
				}
			}
		}
	}
	add(m.Company)
	add(companySuffixRE.ReplaceAllString(m.Company, ""))
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// withoutReferences blanks the places a letter names the target role and
// company, so the words of a role ("Kafka") never ground a claim about the
// user's experience. A word boundary is asked for only at an end that is a
// word character: "Engineer (Remote - US)" ends in a bracket.
func (m Manifest) withoutReferences(text string) string {
	for _, r := range m.referenceNames() {
		re := regexp.QuoteMeta(r)
		if wordEndRE.MatchString(r[:1]) {
			re = `\b` + re
		}
		if wordEndRE.MatchString(r[len(r)-1:]) {
			re += `\b`
		}
		text = regexp.MustCompile(`(?i)`+re).ReplaceAllString(text, " ")
	}
	return text
}

var wordEndRE = lazyregexp.New(`^\w$`)

// nameShaped says whether a token reads as a name or a technology: it carries
// a symbol, a digit after its first letter or a capital after its first
// character, or it is capitalized in mid-sentence, or it opens a sentence
// without being a function word, a verb or a stock opener.
func nameShaped(t nameTok) bool {
	r := []rune(t.text)
	if !hasLetter(t.text) || unicode.IsDigit(r[0]) || unicode.IsPunct(r[0]) && r[0] != '.' {
		return false
	}
	low := strings.ToLower(t.text)
	if low == "e.g" || low == "i.e" || low == "vs" || low == "etc" {
		return false
	}
	if strings.ContainsAny(t.text, "+#") || strings.Contains(t.text, ".") {
		return true
	}
	for _, c := range r[1:] {
		if unicode.IsUpper(c) || unicode.IsDigit(c) {
			return true
		}
	}
	if !unicode.IsUpper(r[0]) {
		return false
	}
	if len(r) == 1 {
		return low != "i" && low != "a" && !t.initial
	}
	if !t.initial {
		return true
	}
	return !isStop(low) && !verbLike(low, true, false) && !openers[low] && !discourseNouns[singular(low)]
}

// openingCapital is true for a word that opens a sentence, a line or a list
// item and has no other mark of a name.
func openingCapital(t nameTok) bool { return t.initial && capitalOnly(t.text) }

// capitalOnly is a single capital, then lower-case letters only.
func capitalOnly(s string) bool {
	r := []rune(s)
	if !unicode.IsUpper(r[0]) {
		return false
	}
	for _, c := range r[1:] {
		if !unicode.IsLower(c) {
			return false
		}
	}
	return true
}

// checkNames fails a name or technology of text that no user source holds:
// a draft may not introduce a product, technology or company. Matching is by
// whole token, so Java is not found in JavaScript.
func checkNames(g *Gate, where, text string, n names) {
	seen := map[string]bool{}
	for _, t := range nameTokens(text) {
		k := key(t.text)
		if k == "" || seen[k] || n.known[k] || !hasLetter(t.text) {
			continue
		}
		if plainWordTech[k] && !techSpelling(t) {
			continue
		}
		// A word the posting names only in capitals is another word in lower
		// case ("I am" beside the posting's "AM").
		watched := n.watch[k] && !(n.caps[k] && !allCaps(t.text))
		// A sentence may open with a word the posting writes in lower case
		// ("Relational data modeling").
		if watched || nameShaped(t) && !(openingCapital(t) && n.plain[k]) {
			seen[k] = true
			g.fail("unsupported-name", "%s: %q is in no source text", where, t.text)
		}
	}
}

// phraseKeys lists the keys of the words of each text, with a break between
// texts so a phrase never runs across two lines.
func phraseKeys(texts []string) []string {
	var out []string
	for _, t := range texts {
		for _, w := range nameTokens(t) {
			out = append(out, key(w.text))
		}
		out = append(out, "")
	}
	return out
}

// hasPhrase reports whether the words of item appear in order, side by side.
func hasPhrase(hay []string, item string) bool {
	var want []string
	for _, w := range nameTokens(item) {
		want = append(want, key(w.text))
	}
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(hay); i++ {
		j := 0
		for j < len(want) && hay[i+j] == want[j] {
			j++
		}
		if j == len(want) {
			return true
		}
	}
	return false
}

// reflectionWarning names the selected evidence lines the draft hardly
// echoes: fewer of a line's distinctive words than the smaller of 40 percent
// of them and 6.
func (g *Gate) reflectionWarning(m Manifest, lines []string) {
	draft := map[string]bool{}
	for _, l := range lines {
		for _, t := range kb.Terms(l) {
			draft[t] = true
		}
	}
	var missing []string
	for _, id := range m.Selected {
		e, ok := m.evidence(id)
		if !ok {
			continue
		}
		words := kb.Terms(e.Text)
		if len(words) == 0 {
			continue
		}
		need := min((2*len(words)+4)/5, 6)
		hit := 0
		for _, w := range words {
			if draft[w] {
				hit++
			}
		}
		if hit < need {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		g.warn("evidence-not-reflected", "selected evidence not reflected in the text: %s", strings.Join(missing, ", "))
	}
}

// roleTitle is the role as a letter names it: without the word a page title
// appends ("Application") and without a segment that is the company ("Flexi -
// Junior Engineer").
func (m Manifest) roleTitle() string {
	role := strings.TrimSpace(postingNounRE.ReplaceAllString(m.Role, ""))
	company := strings.ToLower(strings.TrimSpace(m.Company))
	for _, sep := range []string{" - ", " | ", " – "} {
		head, tail, ok := strings.Cut(role, sep)
		switch {
		case !ok || company == "":
		case strings.ToLower(strings.TrimSpace(head)) == company:
			role = strings.TrimSpace(tail)
		case strings.ToLower(strings.TrimSpace(tail)) == company:
			role = strings.TrimSpace(head)
		}
	}
	return role
}

// unheldNames are the names the posting uses that the gate watches and no
// source of the user holds, as the posting spells them. The role and the
// company are left out: a letter may name them.
func (m Manifest) unheldNames() []string {
	n := m.names()
	ref := map[string]bool{}
	for _, r := range m.referenceNames() {
		for _, w := range nameTokens(r) {
			ref[key(w.text)] = true
		}
	}
	texts := append([]string{}, m.NiceToHave...)
	for _, r := range m.Requirements {
		texts = append(texts, r.Text)
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range append(texts, m.Keywords...) {
		for _, w := range nameTokens(t) {
			k := key(w.text)
			if k == "" || seen[k] || ref[k] || n.known[k] || !n.watch[k] || plainWordTech[k] && !techSpelling(w) {
				continue
			}
			seen[k] = true
			out = append(out, w.text)
		}
	}
	return out[:min(len(out), 24)]
}

// roleOnlyWords are the words of the role that no user source holds: a
// letter may use them only inside the role's name.
func (m Manifest) roleOnlyWords() []string {
	n := m.names()
	seen := map[string]bool{}
	var out []string
	for _, w := range nameTokens(m.roleTitle()) {
		if k := key(w.text); n.watch[k] && !n.known[k] && !seen[k] {
			seen[k] = true
			out = append(out, w.text)
		}
	}
	return out
}
