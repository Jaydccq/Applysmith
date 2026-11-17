package workspace

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func intp(n int) *int { return &n }

func TestApplyOps(t *testing.T) {
	tests := []struct {
		name string
		src  string
		ops  []Op
		want string
	}{
		{
			name: "set scalar keeps the comment above",
			src:  "version: 1\ntargets:\n  compensation:\n    # floor for offers\n    minimum: 50000   # annual\n    target: 120000\n",
			ops:  []Op{{Kind: OpSet, Path: "targets.compensation.minimum", Value: "90000"}},
			want: "version: 1\ntargets:\n  compensation:\n    # floor for offers\n    minimum: 90000   # annual\n    target: 120000\n",
		},
		{
			name: "set fills a blank value in place",
			src:  "candidate:\n  full_name:          # e.g. Alex Example\n  email:\n",
			ops:  []Op{{Kind: OpSet, Path: "candidate.full_name", Value: "Alex Example"}},
			want: "candidate:\n  full_name: Alex Example          # e.g. Alex Example\n  email:\n",
		},
		{
			name: "set keeps quoting style",
			src:  "candidate:\n  email: \"old@example.com\"\n",
			ops:  []Op{{Kind: OpSet, Path: "candidate.email", Value: "ada@example.com"}},
			want: "candidate:\n  email: \"ada@example.com\"\n",
		},
		{
			name: "set single-quoted",
			src:  "a:\n  b: 'it''s'\n  c: x\n",
			ops:  []Op{{Kind: OpSet, Path: "a.b", Value: "plain"}},
			want: "a:\n  b: 'plain'\n  c: x\n",
		},
		{
			name: "set blank to null",
			src:  "a:\n  b: old # why\n",
			ops:  []Op{{Kind: OpSet, Path: "a.b", Value: ""}},
			want: "a:\n  b: # why\n",
		},
		{
			name: "set adds a missing key at the end of its mapping",
			src:  "version: 1\ntargets:\n  compensation:\n    minimum: 1\n\n# next group\nlinks:\n  github:\n",
			ops:  []Op{{Kind: OpSet, Path: "targets.compensation.currency", Value: "GBP"}},
			want: "version: 1\ntargets:\n  compensation:\n    minimum: 1\n    currency: GBP\n\n# next group\nlinks:\n  github:\n",
		},
		{
			name: "set creates nested keys under a blank value",
			src:  "version: 1\ntargets:\n  compensation:\nlinks:\n  github:\n",
			ops:  []Op{{Kind: OpSet, Path: "targets.compensation.minimum", Value: "90000"}},
			want: "version: 1\ntargets:\n  compensation:\n    minimum: 90000\nlinks:\n  github:\n",
		},
		{
			name: "set creates nested keys under an empty flow mapping",
			src:  "scan:\n  defaults: {}\nnotifications:\n  x:\n",
			ops:  []Op{{Kind: OpSet, Path: "scan.defaults.linkedin.evaluate_limit", Value: "5"}},
			want: "scan:\n  defaults:\n    linkedin:\n      evaluate_limit: 5\nnotifications:\n  x:\n",
		},
		{
			name: "set a list replaces an empty flow list",
			src:  "skills: []\nother: 1\n",
			ops:  []Op{{Kind: OpSet, Path: "skills", Value: "[{name: Go, level: expert}]"}},
			want: "skills:\n  - name: Go\n    level: expert\nother: 1\n",
		},
		{
			name: "set a block mapping value over a scalar",
			src:  "a:\n  b: 1\n  c: 2\n",
			ops:  []Op{{Kind: OpSet, Path: "a.b", Value: "{x: 1, y: [1, 2]}"}},
			want: "a:\n  b:\n    x: 1\n    y:\n      - 1\n      - 2\n  c: 2\n",
		},
		{
			name: "set replaces a whole block with a scalar",
			src:  "a:\n  b:\n    x: 1\n    y: 2\n  c: 2\n",
			ops:  []Op{{Kind: OpSet, Path: "a.b", Value: "5"}},
			want: "a:\n  b: 5\n  c: 2\n",
		},
		{
			name: "set a list item field",
			src:  "roles:\n  - title: A\n    priority: primary\n  - title: B\n    priority: primary\n",
			ops:  []Op{{Kind: OpSet, Path: "roles[1].priority", Value: "secondary"}},
			want: "roles:\n  - title: A\n    priority: primary\n  - title: B\n    priority: secondary\n",
		},
		{
			name: "set first key of a list item keeps the dash",
			src:  "roles:\n  - title: A\n    priority: primary\n",
			ops:  []Op{{Kind: OpSet, Path: "roles[0].title", Value: "{x: 1}"}},
			want: "roles:\n  - title:\n      x: 1\n    priority: primary\n",
		},
		{
			name: "set a whole list item",
			src:  "roles:\n  - title: A\n    priority: primary\n  - title: B\n",
			ops:  []Op{{Kind: OpSet, Path: "roles[0]", Value: "{title: Z, priority: secondary, seniority: lead}"}},
			want: "roles:\n  - title: Z\n    priority: secondary\n    seniority: lead\n  - title: B\n",
		},
		{
			name: "unset removes the key and its comment but nothing else",
			src:  "a: 1\n# the b key\nb:\n  c: 1\n  d: 2\n\n# group\ne: 3\n",
			ops:  []Op{{Kind: OpUnset, Path: "b"}},
			want: "a: 1\n\n# group\ne: 3\n",
		},
		{
			name: "unset missing key is a no-op",
			src:  "a: 1\n",
			ops:  []Op{{Kind: OpUnset, Path: "z.y"}},
			want: "a: 1\n",
		},
		{
			name: "unset a list item by index",
			src:  "l:\n  - a\n  - b\n  - c\n",
			ops:  []Op{{Kind: OpUnset, Path: "l[1]"}},
			want: "l:\n  - a\n  - c\n",
		},
		{
			name: "append to a block list leaves other lines identical",
			src:  "# skip\nbatch_skip_companies:\n  - Apple   # keep\n  - Tesla\n\n# next\nscan: {}\n",
			ops:  []Op{{Kind: OpAppend, Path: "batch_skip_companies", Value: "Example Corp"}},
			want: "# skip\nbatch_skip_companies:\n  - Apple   # keep\n  - Tesla\n  - Example Corp\n\n# next\nscan: {}\n",
		},
		{
			name: "append to an empty flow list",
			src:  "batch_skip_companies: []   # none\nx: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "batch_skip_companies", Value: "Tesla"}},
			want: "batch_skip_companies: [Tesla]   # none\nx: 1\n",
		},
		{
			name: "append a mapping to an empty flow list writes a block list",
			src:  "companies: []   # none\nx: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "companies", Value: "{name: A, slug: a}"}},
			want: "companies: # none\n  - name: A\n    slug: a\nx: 1\n",
		},
		{
			name: "append to a flow list",
			src:  "l: [a, b]\n",
			ops:  []Op{{Kind: OpAppend, Path: "l", Value: "c"}},
			want: "l: [a, b, c]\n",
		},
		{
			name: "append to a blank value starts a list",
			src:  "a:\n  l:\n  m: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "a.l", Value: "x"}},
			want: "a:\n  l:\n    - x\n  m: 1\n",
		},
		{
			name: "append to a missing list creates it",
			src:  "a: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "b.l", Value: "x"}},
			want: "a: 1\nb:\n  l:\n    - x\n",
		},
		{
			name: "append a mapping item",
			src:  "roles:\n  - title: A\n    priority: primary\nnext: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "roles", Value: "{title: B, priority: secondary}"}},
			want: "roles:\n  - title: A\n    priority: primary\n  - title: B\n    priority: secondary\nnext: 1\n",
		},
		{
			name: "remove by matching scalar",
			src:  "l:\n  - a\n  - b\n  - c\n",
			ops:  []Op{{Kind: OpRemove, Path: "l", Match: "b"}},
			want: "l:\n  - a\n  - c\n",
		},
		{
			name: "remove by index",
			src:  "l:\n  - a\n  - title: B\n    x: 1\n  - c\n",
			ops:  []Op{{Kind: OpRemove, Path: "l", Index: intp(1)}},
			want: "l:\n  - a\n  - c\n",
		},
		{
			name: "append to a flow list whose plain item holds an apostrophe",
			src:  "l: [O'Reilly, Acme]\nm: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "l", Value: "B"}, {Kind: OpRemove, Path: "l", Match: "Acme"}},
			want: "l: [O'Reilly, B]\nm: 1\n",
		},
		{
			name: "append to a flow list over two lines with quoted items",
			src:  "l: ['it''s ]', \"a \\\" ]\",\n  x]   # mine\nm: 1\n",
			ops:  []Op{{Kind: OpAppend, Path: "l", Value: "y"}},
			want: "l: ['it''s ]', \"a \\\" ]\", x, y]   # mine\nm: 1\n",
		},
		{
			name: "remove from a flow list",
			src:  "l: [a, b, c]\n",
			ops:  []Op{{Kind: OpRemove, Path: "l", Match: "a"}},
			want: "l: [b, c]\n",
		},
		{
			name: "remove the last item leaves a blank value",
			src:  "l:\n  - a\nm: 1\n",
			ops:  []Op{{Kind: OpRemove, Path: "l", Index: intp(0)}},
			want: "l:\nm: 1\n",
		},
		{
			name: "CRLF is preserved on inserted lines",
			src:  "l:\r\n  - a\r\nm: 1\r\n",
			ops:  []Op{{Kind: OpAppend, Path: "l", Value: "b"}},
			want: "l:\r\n  - a\r\n  - b\r\nm: 1\r\n",
		},
		{
			name: "no final newline is preserved",
			src:  "a: 1\nb: 2",
			ops:  []Op{{Kind: OpSet, Path: "c", Value: "3"}},
			want: "a: 1\nb: 2\nc: 3",
		},
		{
			name: "unicode before the value",
			src:  "name: \"Zoë\" # é\nx: 1\n",
			ops:  []Op{{Kind: OpSet, Path: "x", Value: "2"}},
			want: "name: \"Zoë\" # é\nx: 2\n",
		},
		{
			name: "several operations in order",
			src:  "a: 1\nl: []\n",
			ops:  []Op{{Kind: OpSet, Path: "a", Value: "2"}, {Kind: OpAppend, Path: "l", Value: "x"}, {Kind: OpAppend, Path: "l", Value: "y"}},
			want: "a: 2\nl: [x, y]\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyOps([]byte(tc.src), tc.ops)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// A literal or folded block scalar ends at its last content line: a comment
// below it, less indented, belongs to what follows and survives every edit.
func TestBlockScalarEditsKeepTheNextComment(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
		op              Op
	}{
		{"unset", "summary: |\n  text\n# about next\nnext: 1\n", "# about next\nnext: 1\n", Op{Kind: OpUnset, Path: "summary"}},
		{"set", "summary: |\n  text\n# about next\nnext: 1\n", "summary: short\n# about next\nnext: 1\n", Op{Kind: OpSet, Path: "summary", Value: "short"}},
		{"set folded in a mapping", "a:\n  s: >\n    one\n    two\n  # about t\n  t: 1\n", "a:\n  s: x\n  # about t\n  t: 1\n", Op{Kind: OpSet, Path: "a.s", Value: "x"}},
		{"create", "a:\n  s: |\n    text\n# about b\nb: 1\n", "a:\n  s: |\n    text\n  t: 2\n# about b\nb: 1\n", Op{Kind: OpSet, Path: "a.t", Value: "2"}},
		{"remove item", "entries:\n  - q: one\n    answer: |\n      line\n  # the second\n  - q: two\n", "entries:\n  # the second\n  - q: two\n", Op{Kind: OpRemove, Path: "entries", Index: intp(0)}},
		{"append", "l:\n  - |\n    text\n# after\nm: 1\n", "l:\n  - |\n    text\n  - x\n# after\nm: 1\n", Op{Kind: OpAppend, Path: "l", Value: "x"}},
		{"unset after a block whose last line starts with #", "note: |\n  step one\n  # not a comment\n# about next\nnext: 1\n", "note: |\n  step one\n  # not a comment\n", Op{Kind: OpUnset, Path: "next"}},
	} {
		got, err := ApplyOps([]byte(tc.src), []Op{tc.op})
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// A comment after a value on its key's line stays on that line when set
// rewrites the value in block layout, as it does for append and remove.
func TestSetKeepsTheKeyLineComment(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
		op              Op
	}{
		{"empty flow list", "work_modes: []      # any of remote, hybrid, onsite\nx: 1\n", "work_modes: # any of remote, hybrid, onsite\n  - remote\n  - hybrid\nx: 1\n", Op{Kind: OpSet, Path: "work_modes", Value: "[remote, hybrid]"}},
		{"null", "f:\n  negative:           # dropped titles\n  y: 2\n", "f:\n  negative: # dropped titles\n    - intern\n  y: 2\n", Op{Kind: OpSet, Path: "f.negative", Value: "[intern]"}},
		{"scalar to mapping", "b: 1 # why\nx: 1\n", "b: # why\n  k: 2\nx: 1\n", Op{Kind: OpSet, Path: "b", Value: "{k: 2}"}},
		{"block over block", "b: # mine\n  - a\nx: 1\n", "b: # mine\n  - c\n  - d\nx: 1\n", Op{Kind: OpSet, Path: "b", Value: "[c, d]"}},
	} {
		got, err := ApplyOps([]byte(tc.src), []Op{tc.op})
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestApplyOpsErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		op   Op
		code string
	}{
		{"empty path", "a: 1\n", Op{Kind: OpSet, Path: "", Value: "1"}, CodeEditPath},
		{"bad index", "a: 1\n", Op{Kind: OpSet, Path: "a[x]", Value: "1"}, CodeEditPath},
		{"key under scalar", "a: 1\n", Op{Kind: OpSet, Path: "a.b", Value: "1"}, CodeEditPath},
		{"index into mapping", "a: {b: 1}\n", Op{Kind: OpSet, Path: "a[0]", Value: "1"}, CodeEditPath},
		{"index out of range", "l: [a]\n", Op{Kind: OpUnset, Path: "l[3]"}, CodeEditPath},
		{"append to scalar", "a: 1\n", Op{Kind: OpAppend, Path: "a", Value: "1"}, CodeEditPath},
		{"remove no match", "l: [a]\n", Op{Kind: OpRemove, Path: "l", Match: "zz"}, CodeEditPath},
		{"remove from missing", "a: 1\n", Op{Kind: OpRemove, Path: "l", Match: "a"}, CodeEditPath},
		{"invalid value", "a: 1\n", Op{Kind: OpSet, Path: "a", Value: "[unclosed"}, CodeEditPath},
		{"value with a nested anchor", "a: 1\n", Op{Kind: OpSet, Path: "a", Value: "[&x v, *x]"}, CodeEditPath},
		{"create list item", "a: 1\n", Op{Kind: OpSet, Path: "b[0]", Value: "1"}, CodeEditPath},
		{"unknown op", "a: 1\n", Op{Kind: "frob", Path: "a"}, CodeEditPath},
		{"shared value with an alias as a key", "a: &x k\n*x : 1\n", Op{Kind: OpSet, Path: "a", Value: "2"}, CodeEditUnsupported},
		{"invalid source", "a: [\n", Op{Kind: OpSet, Path: "a", Value: "2"}, CodeInvalidYAML},
		{"empty document", "", Op{Kind: OpSet, Path: "a", Value: "2"}, CodeEditUnsupported},
		{"mixed CRLF and LF", "a: 1\r\nb: 2\nc: 3\n", Op{Kind: OpSet, Path: "c", Value: "4"}, CodeEditUnsupported},
		{"CR line breaks", "a: 1\rb: 2\r", Op{Kind: OpSet, Path: "b", Value: "4"}, CodeEditUnsupported},
		{"a line separator in a value", "a: \"x\u2028y\"\nb: 1\nc: 2\n", Op{Kind: OpSet, Path: "b", Value: "4"}, CodeEditUnsupported},
		{"an insert that would change a kept block's trailing lines", "a: |+\n  x\n\n", Op{Kind: OpSet, Path: "c", Value: "1"}, CodeEditUnsupported},
		{"a next-line character in a value", "a: \"x\u0085y\"\nb: 1\nc: 2\n", Op{Kind: OpSet, Path: "b", Value: "4"}, CodeEditUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ApplyOps([]byte(tc.src), []Op{tc.op})
			if !IsCode(err, tc.code) {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

// A document that shares values through anchors elsewhere (a hand-written or
// imported profile) still takes keyed edits of its other keys, byte for byte.
func TestEditBesideAnchors(t *testing.T) {
	src := "# mine\nbase: &base\n  city: Durham\nhome:\n  <<: *base\nwork:\n  modes: []\n"
	got, err := ApplyOps([]byte(src), []Op{
		{Kind: OpSet, Path: "work.modes", Value: "[remote, hybrid]"},
		{Kind: OpSet, Path: "work.clearance", Value: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "# mine\nbase: &base\n  city: Durham\nhome:\n  <<: *base\nwork:\n  modes:\n    - remote\n    - hybrid\n  clearance: none\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// An edit of a value shared through an anchor (PyYAML writes &id001 for a
// list that appears twice) first writes every alias out as a copy: the data
// is unchanged apart from the edit, comments and order stay, and the other
// copy keeps the old value.
func TestEditOfASharedValueWritesTheAliasesOut(t *testing.T) {
	src := "# profile\n" +
		"targets:\n  locations: &id001\n  - Leeds\n  - York\n  level: &lvl senior # mine\n" +
		"relocation:\n  # where I would move\n  places: *id001\n  levels:\n  - *lvl\n  - junior\n  flow: [*lvl, x]\n" +
		"base: &b\n  k: v\nmerged:\n  <<: *b\n  own: 1\n"
	got, err := ApplyOps([]byte(src), []Op{{Kind: OpAppend, Path: "targets.locations", Value: "Hull"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "# profile\n" +
		"targets:\n  locations:\n  - Leeds\n  - York\n  - Hull\n  level: senior # mine\n" +
		"relocation:\n  # where I would move\n  places:\n    - Leeds\n    - York\n  levels:\n  - senior\n  - junior\n  flow: [senior, x]\n" +
		"base:\n  k: v\nmerged:\n  <<:\n    k: v\n  own: 1\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	var before, after map[string]any
	if yaml.Unmarshal([]byte(src), &before) != nil || yaml.Unmarshal(got, &after) != nil {
		t.Fatal("unparseable")
	}
	targets := after["targets"].(map[string]any)
	targets["locations"] = targets["locations"].([]any)[:2]
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("data changed beyond the edit:\n%v\n%v", before, after)
	}
	for _, tc := range []struct {
		name, src, want string
		op              Op
	}{
		{"alias value", "a: &x 1\nb: *x\n", "a: 1\nb: 2\n", Op{Kind: OpSet, Path: "b", Value: "2"}},
		{"key under an anchor", "a: &x\n  k: 1\nb: *x\n", "a:\n  k: 2\nb:\n  k: 1\n", Op{Kind: OpSet, Path: "a.k", Value: "2"}},
		{"a key under an alias list item", "lvl: &l\n  name: a\nlevels:\n  - *l\n  - name: b\n", "lvl:\n  name: a\nlevels:\n  - name: c\n  - name: b\n", Op{Kind: OpSet, Path: "levels[0].name", Value: "c"}},
		{"dropping an anchor in use", "a:\n  k: &x 1\nb: *x\n", "b: 1\n", Op{Kind: OpUnset, Path: "a"}},
	} {
		got, err := ApplyOps([]byte(tc.src), []Op{tc.op})
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	} // A refusal names the shared key.
	if _, err := ApplyOps([]byte("top:\n  a: &x k\n  *x : 1\n"), []Op{{Kind: OpSet, Path: "top.a", Value: "2"}}); err == nil || !strings.Contains(err.Error(), "top.a is shared through a YAML anchor") {
		t.Fatalf("refusal: %v", err)
	}
}

func TestEditedProfileStaysValid(t *testing.T) {
	src := string(templateBytes(FileProfile))
	got, err := ApplyOps([]byte(src), []Op{
		{Kind: OpSet, Path: "candidate.full_name", Value: "Alex Example"},
		{Kind: OpSet, Path: "targets.compensation.minimum", Value: "90000"},
		{Kind: OpSet, Path: "targets.compensation.target", Value: "120000"},
		{Kind: OpAppend, Path: "targets.roles", Value: "{title: Backend Engineer, seniority: senior}"},
		{Kind: OpAppend, Path: "targets.locations", Value: "London"},
		{Kind: OpSet, Path: "work_authorization.requires_sponsorship_now", Value: "false"},
		{Kind: OpAppend, Path: "batch_skip_companies", Value: "Example Corp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, rep := ParseProfile(got)
	if !rep.OK() {
		t.Fatalf("%s\n%s", rep, got)
	}
	if p.Candidate.FullName != "Alex Example" || *p.Targets.Compensation.Minimum != 90000 ||
		p.Targets.Roles[0].Title != "Backend Engineer" || p.Targets.Locations[0] != "London" ||
		*p.WorkAuthorization.RequiresSponsorshipNow || p.EffectiveSkipCompanies()[0] != "Example Corp" {
		t.Fatalf("decoded profile wrong: %+v", p)
	}
	// Everything the edit did not touch keeps its exact line.
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") && !strings.Contains(string(got), line) {
			t.Errorf("comment line lost: %q", line)
		}
	}
}

func TestEditedPortalsAndPolicyStayValid(t *testing.T) {
	got, err := ApplyOps(templateBytes(FilePolicy), []Op{
		{Kind: OpSet, Path: "captcha_solver", Value: "off"},
		{Kind: OpSet, Path: "cooldowns.captcha", Value: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, rep := ParsePolicy(got)
	if !rep.OK() || p.CaptchaSolver != "off" || p.Cooldowns.Captcha != 2 {
		t.Fatalf("%s\n%+v\n%s", rep, p, got)
	}
	if !strings.Contains(string(got), "# Hours to back off a site after each risk signal") {
		t.Fatal("comment lost")
	}
	got, err = ApplyOps(templateBytes(FilePortals), []Op{
		{Kind: OpAppend, Path: "companies", Value: "{name: Example Corp, ats: greenhouse, slug: example-corp}"},
		{Kind: OpSet, Path: "sources.signals-edgar.enabled", Value: "true"},
		{Kind: OpAppend, Path: "filters.title.negative", Value: "contract"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, rep := ParsePortals(got); !rep.OK() {
		t.Fatalf("%s\n%s", rep, got)
	}
}

// A portals.yml written with flow lists under block parents, as a hand-edited
// or tool-written file can be: per-entry edits (the Portals forms) and whole
// list sets (the filters form) must work on it, leave each list they enter in
// block layout, and keep every untouched line.
func TestPerEntryEditsReachIntoFlowLists(t *testing.T) {
	src := "# Where to look.\nversion: 1\nfilters:\n  title:\n    positive: [engineer, developer]   # roles\n    negative:\n      - intern\n" +
		"    seniority_boost: [{term: senior, weight: 2}]\n  locations:\n    include: [Remote, Berlin]\n    exclude: []\n  skip_companies: [Acme Staffing]\n" +
		"# Boards to read.\ncompanies: [{name: Alpha, careers_url: \"https://alpha.example/jobs\", enabled: true},\n  {name: Beta, ats: lever, slug: beta}]   # mine\nconcurrency:\n  list: 4   # keep\n"
	alpha := map[string]any{"name": "Alpha", "careers_url": "https://alpha.example/jobs", "enabled": true}
	beta := map[string]any{"name": "Beta", "ats": "lever", "slug": "beta"}
	gamma := map[string]any{"name": "Gamma", "ats": "greenhouse", "slug": "gamma"}
	off := map[string]any{"name": "Beta", "ats": "lever", "slug": "beta", "enabled": false}
	for _, tc := range []struct {
		op   Op
		path string
		want any
	}{
		{Op{Kind: OpSet, Path: "companies[1].enabled", Value: "false"}, "companies", []any{alpha, off}},
		{Op{Kind: OpRemove, Path: "companies", Index: intp(0)}, "companies", []any{beta}},
		{Op{Kind: OpAppend, Path: "companies", Value: "{name: Gamma, ats: greenhouse, slug: gamma}"}, "companies", []any{alpha, beta, gamma}},
		{Op{Kind: OpSet, Path: "filters.title.seniority_boost[0].weight", Value: "3"}, "filters.title.seniority_boost", []any{map[string]any{"term": "senior", "weight": 3}}},
		{Op{Kind: OpSet, Path: "filters.title.positive", Value: "[engineer]"}, "filters.title.positive", []any{"engineer"}},
		{Op{Kind: OpSet, Path: "filters.skip_companies", Value: "[Acme Staffing, Other]"}, "filters.skip_companies", []any{"Acme Staffing", "Other"}},
	} {
		got, err := ApplyOps([]byte(src), []Op{tc.op})
		if err != nil {
			t.Fatalf("%s %s: %v", tc.op.Kind, tc.op.Path, err)
		}
		var doc yaml.Node
		var data map[string]any
		if yaml.Unmarshal(got, &doc) != nil || yaml.Unmarshal(got, &data) != nil {
			t.Fatalf("%s: not YAML:\n%s", tc.op.Path, got)
		}
		node, val := doc.Content[0], any(data)
		for _, k := range strings.Split(tc.path, ".") {
			node, val = valueNode(node, k), val.(map[string]any)[k]
		}
		if !reflect.DeepEqual(val, tc.want) || node.Style&yaml.FlowStyle != 0 {
			t.Errorf("%s %s: %v (flow %v)\n%s", tc.op.Kind, tc.op.Path, val, node.Style&yaml.FlowStyle != 0, got)
		}
		if tc.path == "companies" && !strings.Contains(string(got), "companies: # mine\n") {
			t.Errorf("the comment after the list is lost:\n%s", got)
		}
		for _, line := range []string{"# Where to look.", "    negative:", "      - intern", "    include: [Remote, Berlin]", "# Boards to read.", "  list: 4   # keep"} {
			if !strings.Contains(string(got), line+"\n") {
				t.Errorf("%s: lost the line %q\n%s", tc.op.Path, line, got)
			}
		}
	}
}

func TestApplyOpsUnknownErrorIsTyped(t *testing.T) {
	_, err := ApplyOps([]byte("a: 1\n"), []Op{{Kind: OpSet, Path: "a.b", Value: "1"}})
	var te *Error
	if !errors.As(err, &te) || te.Path != "a.b" {
		t.Fatalf("err = %#v", err)
	}
}
