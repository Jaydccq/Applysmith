package answerbank

import (
	"os"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

// A frozen corpus of reworded or furnished questions keeps routing to the
// entry that answers them. The curated self match (every example fills from
// its own entry) is TestDefaultBank_EveryExampleFillsFromItsOwnEntry.
func TestAnswerBank_Replay_SyntheticMissCorpusAndCuratedSelfMatch(t *testing.T) {
	b := defaultBank(t)
	t.Run("frozen miss corpus", func(t *testing.T) {
		raw, err := os.ReadFile("testdata/miss-corpus.txt")
		if err != nil {
			t.Fatal(err)
		}
		lines := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) != 3 {
				t.Fatalf("malformed corpus line %q", line)
			}
			e, ok := b.Find(f[0])
			if !ok {
				t.Fatalf("the corpus names %q, which is not in the shipped bank", f[0])
			}
			lines++
			c := seCase{entry: e, example: f[2], input: match.InputType(f[1]), required: true}
			t.Run(f[0]+"/"+f[2], func(t *testing.T) {
				rec, r := seRun(t, c)
				if why := seFill(c, rec, r); why != "" {
					t.Fatalf("%s; outcome %s entry %q reason %q", why, r.Outcome, r.EntryID, r.Reason)
				}
			})
		}
		if lines < 15 {
			t.Fatalf("the corpus holds %d questions", lines)
		}
	})
}
