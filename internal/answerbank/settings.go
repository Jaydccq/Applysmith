package answerbank

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Grounding modes of free-text composition.
const (
	GroundingStrict    = "strict"
	GroundingFlex      = "flex"
	GroundingDangerous = "dangerous"
)

// GroundingModes lists the closed set.
var GroundingModes = []string{GroundingStrict, GroundingFlex, GroundingDangerous}

// settingsKey is the row of workspace_meta that holds the settings. They live
// in the state store, not in answer-bank.yml, so sharing a bank file never
// carries them.
const settingsKey = "answerbank.settings"

// Settings are the answer-bank runtime settings.
type Settings struct {
	LLMFallbackEnabled          bool   `json:"llmFallbackEnabled"`
	GroundingMode               string `json:"groundingMode"`
	AutoAcceptLegalDeclarations bool   `json:"autoAcceptLegalDeclarations"`
	AllowGoogleSignIn           bool   `json:"allowGoogleSignIn"`
	// WatchScans runs browser scans in the visible window instead of the hidden
	// scan lane (guarded boards always stay hidden). Off by default.
	WatchScans bool `json:"watchScans"`
	// KnowledgeSemanticRetrieval explicitly permits provider embeddings of the
	// query and published snippets. It is independent of model fallback.
	KnowledgeSemanticRetrieval bool `json:"knowledgeSemanticRetrieval"`
}

// DefaultSettings is what a workspace without a stored record reads.
func DefaultSettings() Settings {
	return Settings{GroundingMode: GroundingStrict, AutoAcceptLegalDeclarations: true}
}

// AutofillGroundingMode is the mode autofill composes in: dangerous is
// clamped to flex at the point of use while the stored value stays.
func (s Settings) AutofillGroundingMode() string {
	if s.GroundingMode == GroundingDangerous {
		return GroundingFlex
	}
	return s.GroundingMode
}

// SettingsPatch changes the fields that are set and leaves the rest.
type SettingsPatch struct {
	LLMFallbackEnabled          *bool   `json:"llmFallbackEnabled"`
	GroundingMode               *string `json:"groundingMode"`
	AutoAcceptLegalDeclarations *bool   `json:"autoAcceptLegalDeclarations"`
	AllowGoogleSignIn           *bool   `json:"allowGoogleSignIn"`
	WatchScans                  *bool   `json:"watchScans"`
	KnowledgeSemanticRetrieval  *bool   `json:"knowledgeSemanticRetrieval"`
}

func (p SettingsPatch) validate() error {
	if p.GroundingMode != nil && !slices.Contains(GroundingModes, *p.GroundingMode) {
		return &ValidationError{Issues: []Issue{{Message: fmt.Sprintf("groundingMode %q is not one of %v", *p.GroundingMode, GroundingModes)}}}
	}
	return nil
}

func (p SettingsPatch) apply(s Settings) Settings {
	if p.LLMFallbackEnabled != nil {
		s.LLMFallbackEnabled = *p.LLMFallbackEnabled
	}
	if p.GroundingMode != nil {
		s.GroundingMode = *p.GroundingMode
	}
	if p.AutoAcceptLegalDeclarations != nil {
		s.AutoAcceptLegalDeclarations = *p.AutoAcceptLegalDeclarations
	}
	if p.KnowledgeSemanticRetrieval != nil {
		s.KnowledgeSemanticRetrieval = *p.KnowledgeSemanticRetrieval
	}
	if p.AllowGoogleSignIn != nil {
		s.AllowGoogleSignIn = *p.AllowGoogleSignIn
	}
	if p.WatchScans != nil {
		s.WatchScans = *p.WatchScans
	}
	return s
}

// Settings reads the stored settings. A missing or unreadable record, and any
// field that is absent or malformed, reads as its default.
func (s *Service) Settings(ctx context.Context) Settings {
	if s.st == nil {
		return DefaultSettings()
	}
	var raw string
	err := s.st.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, settingsKey).Scan(&raw)
	if err != nil {
		return DefaultSettings()
	}
	return decodeSettings(raw)
}

func decodeSettings(raw string) Settings {
	out := DefaultSettings()
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return out
	}
	var b bool
	if json.Unmarshal(fields["llmFallbackEnabled"], &b) == nil {
		out.LLMFallbackEnabled = b
	}
	if json.Unmarshal(fields["autoAcceptLegalDeclarations"], &b) == nil {
		out.AutoAcceptLegalDeclarations = b
	}
	if json.Unmarshal(fields["watchScans"], &b) == nil {
		out.WatchScans = b
	}
	if json.Unmarshal(fields["knowledgeSemanticRetrieval"], &b) == nil {
		out.KnowledgeSemanticRetrieval = b
	}
	var google bool
	if json.Unmarshal(fields["allowGoogleSignIn"], &google) == nil {
		out.AllowGoogleSignIn = google
	}
	var mode string
	if json.Unmarshal(fields["groundingMode"], &mode) == nil && slices.Contains(GroundingModes, mode) {
		out.GroundingMode = mode
	}
	return out
}

// PatchSettings applies a patch in one transaction and returns the result.
func (s *Service) PatchSettings(ctx context.Context, p SettingsPatch) (Settings, error) {
	if err := p.validate(); err != nil {
		return Settings{}, err
	}
	if s.st == nil {
		return Settings{}, errors.New("answerbank: settings need the workspace store")
	}
	var out Settings
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		cur := DefaultSettings()
		var raw string
		switch err := tx.QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, settingsKey).Scan(&raw); {
		case err == nil:
			cur = decodeSettings(raw)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		out = p.apply(cur)
		enc, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingsKey, string(enc))
		return err
	})
	return out, err
}
