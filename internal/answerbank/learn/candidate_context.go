package learn

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"applysmith/internal/answerbank/match"
)

// PageContext is where a candidate's evidence was seen: the form sections, the
// option labels of its controls and the number of distinct postings
// (postingSQL). Multiple marks a group of checkboxes, which takes several of
// its options.
type PageContext struct {
	Sections []string
	Options  []string
	Postings int
	Multiple bool
}

const (
	maxContextSections = 5
	maxContextOptions  = 30
)

// CandidateContexts reads the sections, options and postings of the evidence
// rows each candidate rests on. A value-withheld control is left out, so its
// options never leave the database even if a row were to hold them.
func (s *Service) CandidateContexts(ctx context.Context, ids []int64) (map[int64]PageContext, error) {
	out := make(map[int64]PageContext, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, 2*len(ids))
	for range 2 {
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := s.st.Read().QueryContext(ctx, `
		SELECT ce.candidate_id, ev.section_path, ev.control_kind, ev.options_json, `+postingSQL("ev")+` FROM answer_candidate_evidence ce
			JOIN answer_events ev ON ev.id = ce.event_id WHERE ev.withheld = '' AND ce.candidate_id IN (`+marks+`)
		UNION ALL
		SELECT ce.candidate_id, mf.section_path, mf.control_kind, mf.options_json, `+postingSQL("mf")+` FROM answer_candidate_evidence ce
			JOIN manual_fills mf ON mf.id = ce.fill_id WHERE mf.withheld = '' AND ce.candidate_id IN (`+marks+`)
		ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int64]map[string]bool{}
	for rows.Next() {
		var id int64
		var section, kind, opts, posting string
		if err := rows.Scan(&id, &section, &kind, &opts, &posting); err != nil {
			return nil, err
		}
		pc := out[id]
		if seen[id] == nil {
			seen[id] = map[string]bool{}
		}
		if !seen[id][posting] {
			seen[id][posting] = true
			pc.Postings++
		}
		if section != "" && !slices.Contains(pc.Sections, section) && len(pc.Sections) < maxContextSections {
			pc.Sections = append(pc.Sections, section)
		}
		var labels []string
		_ = json.Unmarshal([]byte(opts), &labels)
		pc.Multiple = pc.Multiple || match.ControlKind(kind) == match.KindCheckbox && len(labels) > 1
		for _, l := range labels {
			if l = strings.TrimSpace(l); l != "" && !slices.Contains(pc.Options, l) && len(pc.Options) < maxContextOptions {
				pc.Options = append(pc.Options, l)
			}
		}
		out[id] = pc
	}
	return out, rows.Err()
}
