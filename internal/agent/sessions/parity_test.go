package sessions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/store"
)

// Tests named after the conversation and estimator rows of the ask-agent
// parity audit.

func TestAgent_Conversation_PersistsUntilDeleted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "applysmith.db")
	st, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil)
	a, _ := s.Create(ctx, Dashboard, "Kept")
	say(t, s, a.ID, RoleUser, "relocation to Exampleville")
	b, _ := s.Create(ctx, Telegram, "")
	say(t, s, b.ID, RoleUser, "a second talk")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// A new process over the same file still has both: nothing expires,
	// nothing is evicted and no setting turns the history off.
	st, err = store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = New(st, nil)
	if list, total, _ := s.List(ctx, ListQuery{}); total != 2 || len(list) != 2 {
		t.Fatalf("after a restart: %d conversations", total)
	}
	if msgs, _ := s.Messages(ctx, a.ID); len(msgs) != 1 {
		t.Fatalf("messages after a restart = %v", msgs)
	}
	set, err := s.Settings(ctx)
	if err != nil || !set.Compaction {
		t.Fatalf("settings = %+v, %v", set, err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.List(ctx, ListQuery{}); total != 1 {
		t.Fatalf("%d conversations after one delete", total)
	}
}

func TestAgent_Pack_EstimatorWeightsNonLatinScripts(t *testing.T) {
	// Scripts written without spaces are built from code points: tracked files
	// hold no non-English literals.
	han := strings.Repeat(string(rune(0x4E2D)), 40)
	kana := strings.Repeat(string(rune(0x3042)), 40)
	hangul := strings.Repeat(string(rune(0xD55C)), 40)
	full := strings.Repeat(string(rune(0xFF21)), 40)
	for name, s := range map[string]string{"Han": han, "kana": kana, "Hangul": hangul, "fullwidth": full} {
		if got := EstimateTokens(s); got != 40 {
			t.Errorf("%s: 40 characters estimate %d tokens, want 40 (one each, not a quarter)", name, got)
		}
	}
	latin := strings.Repeat("a", 40)
	if got := EstimateTokens(latin); got != 10 {
		t.Errorf("Latin: 40 characters estimate %d tokens, want 10", got)
	}
	if got := EstimateTokens(latin + han); got != 50 {
		t.Errorf("mixed text estimates %d tokens, want 50", got)
	}
	if EstimateTokens("") != 0 {
		t.Error("empty text costs tokens")
	}
	// Bytes of another alphabet still count at the byte rate.
	if got := EstimateTokens(strings.Repeat(string(rune(0x0416)), 40)); got != 20 {
		t.Errorf("Cyrillic: 40 characters estimate %d tokens, want 20", got)
	}
}
