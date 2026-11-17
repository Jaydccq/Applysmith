package kb

import (
	"reflect"
	"testing"
)

func TestTokenizer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"lowercase and split", "Reduced Latency, Cut Costs!", []string{"reduc", "latency", "cut", "cost"}},
		{"scaffolding dropped", "Describe how you explain what you did", nil},
		{"one character tokens dropped", "a b c go x", []string{"go"}},
		{"a hyphenated word is its parts and the parts joined", "open-source on-call rotation", []string{"open", "sourc", "opensourc", "call", "oncall", "rotation"}},
		{"inner apostrophe kept, possessive dropped", "the team's roadmap won't slip", []string{"team", "roadmap", "won't", "slip"}},
		{"language names with signs are one term", "C++, C# and F# on .NET; asp.net; example.network", []string{"cplusplus", "csharp", "fsharp", "dotnet", "asp", "dotnet", "exampl", "network"}},
		{"composed and decomposed accents are one term", "cafe\u0301 caf\u00e9", []string{"caf\u00e9", "caf\u00e9"}},
		{"digits are tokens", "cut p99 latency 40 percent", []string{"cut", "p99", "latency", "40", "percent"}},
		{"plural folds ies", "queries and policies", []string{"query", "policy"}},
		{"ing folds with undoubling", "running shipping", []string{"run", "ship"}},
		{"interested and interesting stay apart", "interested interesting", []string{"interest", "interesting"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokens(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Tokens(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTokenizer_SameFoldForQueryAndIndex(t *testing.T) {
	for _, pair := range [][2]string{{"index", "indexes"}, {"migrate", "migrating"}, {"deploy", "deployed"}, {"service", "services"}, {"query", "queries"},
		{"code", "coding"}, {"use", "used"}, {"agree", "agreeing"}, {"need", "needed"}, {"hire", "hiring"}} {
		a, b := Tokens(pair[0]), Tokens(pair[1])
		if len(a) != 1 || len(b) != 1 || a[0] != b[0] {
			t.Errorf("%q -> %v but %q -> %v", pair[0], a, pair[1], b)
		}
	}
}

func TestQuestionShaped(t *testing.T) {
	for q, want := range map[string]bool{
		"Describe your experience with schema design": true,
		"What is your biggest strength?":              true,
		"Walk me through it":                          false,
	} {
		if got := questionShaped(q); got != want {
			t.Errorf("questionShaped(%q) = %v, want %v", q, got, want)
		}
	}
}

// Text written without spaces (Chinese, Japanese) is read as overlapping
// two-character terms, so a phrase inside a sentence is found; its sentence
// marks end a sentence and its full-width digits are figures.
func TestTokenizer_TextWithoutSpaces(t *testing.T) {
	// "I owned the migration of the payment system" / "payment system".
	sentence, phrase := "\u6211\u8d1f\u8d23\u652f\u4ed8\u7cfb\u7edf\u7684\u8fc1\u79fb", "\u652f\u4ed8\u7cfb\u7edf"
	got := Tokens(sentence)
	if len(got) != 9 || got[3] != "\u652f\u4ed8" || got[8] != "\u8fc1\u79fb" {
		t.Fatalf("Tokens(sentence) = %q", got)
	}
	if got := Tokens("Kafka" + "\u8fc1\u79fb"); !reflect.DeepEqual(got, []string{"kafka", "\u8fc1\u79fb"}) {
		t.Fatalf("mixed = %q", got)
	}
	f := newFixture(t)
	f.write("knowledge/projects/pay.md", snippet("id: pay\ntitle: Payments", sentence+"\u3002"))
	if got, err := f.svc.Find(f.ctx, phrase); err != nil || len(got) != 1 || got[0].ID != "pay" {
		t.Fatalf("Find(phrase) = %v, %v", got, err)
	}
	if n := sentenceCount(sentence + "\u3002" + sentence + "\uff01"); n != 2 {
		t.Errorf("sentenceCount = %d, want 2", n)
	}
	if !hasDigit("\uff13\uff10%") {
		t.Error("full-width digits are not a figure")
	}
}
