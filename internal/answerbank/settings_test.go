package answerbank

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSettings_Defaults(t *testing.T) {
	s, _ := newService(t)
	got := s.Settings(context.Background())
	if got.LLMFallbackEnabled || got.GroundingMode != GroundingStrict || !got.AutoAcceptLegalDeclarations || got.WatchScans {
		t.Fatalf("%+v", got)
	}
}

func sptr(v string) *string { return &v }

func TestSettings_PartialPatchKeepsOthers(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	if _, err := s.PatchSettings(ctx, SettingsPatch{AutoAcceptLegalDeclarations: boolPtr(true), LLMFallbackEnabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PatchSettings(ctx, SettingsPatch{GroundingMode: sptr(GroundingFlex)})
	if err != nil || !got.AutoAcceptLegalDeclarations || !got.LLMFallbackEnabled || got.GroundingMode != GroundingFlex {
		t.Fatalf("%+v %v", got, err)
	}
	if again := s.Settings(ctx); again != got {
		t.Fatalf("stored %+v, patched %+v", again, got)
	}
}

func TestSettings_WatchScansDefaultsOffAndPersists(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	if s.Settings(ctx).WatchScans {
		t.Fatal("scans must stay hidden until the user asks to watch them")
	}
	got, err := s.PatchSettings(ctx, SettingsPatch{WatchScans: boolPtr(true)})
	if err != nil || !got.WatchScans || !s.Settings(ctx).WatchScans {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ = s.PatchSettings(ctx, SettingsPatch{LLMFallbackEnabled: boolPtr(true)}); !got.WatchScans {
		t.Fatal("a patch of another field cleared the setting")
	}
	if decodeSettings(`{"watchScans":"yes"}`).WatchScans {
		t.Fatal("a malformed value must read as off")
	}
}

func TestSettings_InvalidModeRejected(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	before, _ := s.PatchSettings(ctx, SettingsPatch{LLMFallbackEnabled: boolPtr(true)})
	var ve *ValidationError
	if _, err := s.PatchSettings(ctx, SettingsPatch{GroundingMode: sptr("reckless"), LLMFallbackEnabled: boolPtr(false)}); !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	if s.Settings(ctx) != before {
		t.Fatal("a rejected patch changed the stored settings")
	}
}

func TestSettings_DangerousClampedToFlexAtUse(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	got, _ := s.PatchSettings(ctx, SettingsPatch{GroundingMode: sptr(GroundingDangerous)})
	if got.AutofillGroundingMode() != GroundingFlex || s.Settings(ctx).GroundingMode != GroundingDangerous {
		t.Fatalf("clamped = %q, stored = %q", got.AutofillGroundingMode(), s.Settings(ctx).GroundingMode)
	}
	if (Settings{GroundingMode: GroundingStrict}).AutofillGroundingMode() != GroundingStrict {
		t.Fatal("strict must stay strict")
	}
}

func TestSettings_UnreadableRecordReadsAsDefaults(t *testing.T) {
	tests := map[string]Settings{
		`not json`: DefaultSettings(),
		`{"llmFallbackEnabled":"yes","groundingMode":"reckless","autoAcceptLegalDeclarations":true}`: {GroundingMode: GroundingStrict, AutoAcceptLegalDeclarations: true},
		`{"groundingMode":"flex"}`:              {GroundingMode: GroundingFlex, AutoAcceptLegalDeclarations: true},
		`{"autoAcceptLegalDeclarations":false}`: {GroundingMode: GroundingStrict},
	}
	for raw, want := range tests {
		if got := decodeSettings(raw); got != want {
			t.Errorf("%s: %+v, want %+v", raw, got, want)
		}
	}
	s, _ := newService(t)
	ctx := context.Background()
	if _, err := s.st.Read().ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, 'garbage')`, settingsKey)
		return err
	})
	if err != nil || s.Settings(ctx) != DefaultSettings() {
		t.Fatalf("%v %+v", err, s.Settings(ctx))
	}
	if got, err := s.PatchSettings(ctx, SettingsPatch{LLMFallbackEnabled: boolPtr(true)}); err != nil || !got.LLMFallbackEnabled {
		t.Fatalf("a patch over a garbage record: %+v %v", got, err)
	}
}

func TestSettings_GoogleSignInDefaultsOffAndPersists(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	if s.Settings(ctx).AllowGoogleSignIn || decodeSettings(`{"allowGoogleSignIn":"yes"}`).AllowGoogleSignIn || decodeSettings(`{"knowledgeSemanticRetrieval":true,"allowGoogleSignIn":null}`).AllowGoogleSignIn {
		t.Fatal("Google sign-in must default off")
	}
	got, err := s.PatchSettings(ctx, SettingsPatch{AllowGoogleSignIn: boolPtr(true)})
	if err != nil || !got.AllowGoogleSignIn || !s.Settings(ctx).AllowGoogleSignIn {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = s.PatchSettings(ctx, SettingsPatch{WatchScans: boolPtr(true)})
	if err != nil || !got.AllowGoogleSignIn {
		t.Fatal("unrelated patch cleared Google sign-in")
	}
	got, err = s.PatchSettings(ctx, SettingsPatch{AllowGoogleSignIn: boolPtr(false)})
	if err != nil || got.AllowGoogleSignIn || s.Settings(ctx).AllowGoogleSignIn {
		t.Fatal("Google sign-in did not turn off")
	}
}
