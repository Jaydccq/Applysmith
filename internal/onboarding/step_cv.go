package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"applysmith/internal/cvparse"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/workspace"
)

const minCVRunes = 300

var cvFields = []Field{
	{Name: "markdown", Type: "string", Doc: "The CV as markdown. Omit to keep the stored cv.md and only confirm history and skills."},
	{Name: "revision", Type: "string", Doc: "The cv.md revision the markdown was edited from; when cv.md changed since, nothing is saved."},
	{Name: "employment", Type: "object-list", Doc: "Confirmed employment entries (employer, title, start YYYY-MM, end YYYY-MM or present, location, summary); replaces history.employment."},
	{Name: "education", Type: "object-list", Doc: "Confirmed education entries (school, degree, field, start, end, grade_average); replaces history.education."},
	{Name: "skills", Type: "object-list", Doc: "Confirmed skills (name, years, level); replaces skills."},
	{Name: "review_complete", Type: "bool", Doc: "Say the extracted draft was reviewed, even when no entry was confirmed."},
}

type cvJob struct {
	Employer string `json:"employer"`
	Title    string `json:"title,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Location string `json:"location,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

type cvStudy struct {
	School       string `json:"school"`
	Degree       string `json:"degree,omitempty"`
	Field        string `json:"field,omitempty"`
	Start        string `json:"start,omitempty"`
	End          string `json:"end,omitempty"`
	GradeAverage string `json:"grade_average,omitempty"`
}

type cvSkill struct {
	Name  string   `json:"name"`
	Years *float64 `json:"years,omitempty"`
	Level string   `json:"level,omitempty"`
}

type cvInput struct {
	Markdown       *string    `json:"markdown"`
	Revision       string     `json:"revision"`
	Employment     *[]cvJob   `json:"employment"`
	Education      *[]cvStudy `json:"education"`
	Skills         *[]cvSkill `json:"skills"`
	ReviewComplete bool       `json:"review_complete"`
}

func scopeCV(_ context.Context, env *Env) (string, error) {
	c, err := env.Workspace.Read(workspace.FileCV)
	if err != nil || c.Revision == workspace.RevisionAbsent {
		return "", fmt.Errorf("cv.md is not readable")
	}
	return c.Revision, nil
}

func applyCV(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in cvInput
	if err := decodeInput(raw, cvFields, &in); err != nil {
		return Applied{}, err
	}
	if env.Workspace == nil {
		return Applied{}, &EnvError{Message: "no workspace could be resolved"}
	}
	cur, err := env.Workspace.Read(workspace.FileCV)
	if err != nil {
		return Applied{}, &EnvError{Message: "cv.md cannot be read", Err: err}
	}
	text := string(cur.Data)
	if in.Markdown != nil && in.Revision != "" && in.Revision != cur.Revision {
		return Applied{}, &StaleError{Message: "cv.md changed after this text was loaded; nothing was saved"}
	}
	fe := FieldErrors{}
	if in.Markdown != nil {
		text = *in.Markdown
		switch {
		case strings.TrimSpace(text) == "":
			fe.add("markdown", "is empty")
		case !utf8.ValidString(text):
			fe.add("markdown", "is not valid UTF-8")
		}
	}
	var ops []workspace.Op
	if in.Employment != nil {
		ops = append(ops, setOp("history.employment", *in.Employment))
	}
	if in.Education != nil {
		ops = append(ops, setOp("history.education", *in.Education))
	}
	if in.Skills != nil {
		ops = append(ops, setOp("skills", *in.Skills))
	}
	if len(fe) > 0 {
		return Applied{}, fe
	}
	wrote := false
	// Every refusal comes before the first write: the history edit is checked
	// without writing, cv.md goes first because it is the file that can still
	// be refused (size, conflict), and the history edit is saved last.
	if len(ops) > 0 {
		if _, err := editProfile(env, ops, true); err != nil {
			return Applied{}, err
		}
	}
	if in.Markdown != nil {
		if rep := env.Workspace.Validate(workspace.FileCV, []byte(text)); !rep.OK() {
			return Applied{}, FieldErrors{"markdown": rep.String()}
		}
		if !dry {
			saved, err := env.Workspace.Write(workspace.FileCV, []byte(text), cur.Revision)
			if err != nil {
				return Applied{}, &EnvError{Message: "cv.md could not be written", Err: err}
			}
			wrote = wrote || saved.Changed
		}
	}
	if len(ops) > 0 && !dry {
		changed, err := editProfile(env, ops, false)
		if err != nil {
			return Applied{}, err
		}
		wrote = wrote || changed
	}
	reviewed := in.ReviewComplete || len(ops) > 0
	if reviewed && !dry {
		scope := workspace.Revision([]byte(text))
		if err := env.setDecision(ctx, reviewKey("cv"), scope); err != nil {
			return Applied{}, &EnvError{Message: "the review decision could not be stored", Err: err}
		}
	}
	draft := cvparse.Parse(text)
	out := Applied{Step: "cv", DryRun: dry, Wrote: wrote, Detection: Evaluate(ctx, env, mustStep("cv"))}
	if draft.Entries() > 0 {
		out.Draft = &draft
	}
	switch {
	case len(ops) > 0:
		out.Notes = append(out.Notes, "the confirmed history and skills are stored in the profile")
	case draft.Entries() > 0:
		out.Notes = append(out.Notes, "extracted "+draft.Summary()+"; nothing is stored in the profile until you confirm entries with employment, education and skills")
		if n := draft.NeedsMonth(); n > 0 {
			entries := fmt.Sprintf("%d extracted entries give", n)
			if n == 1 {
				entries = "1 extracted entry gives"
			}
			out.Notes = append(out.Notes, entries+" a year without a month (shown as YYYY); supply start and end as YYYY-MM when confirming, the profile does not accept a bare year")
		}
	}
	return out, nil
}

func detectCV(_ context.Context, env *Env) Detection {
	if env.Workspace == nil {
		return missing("no workspace could be resolved")
	}
	c, err := env.Workspace.Read(workspace.FileCV)
	if err != nil {
		return verdict(reasonf(ReasonInvalid, "cv.md", err.Error()))
	}
	if c.Revision == workspace.RevisionAbsent {
		return missing("cv.md does not exist; complete the workspace step first")
	}
	if workspace.IsTemplate(workspace.FileCV, c.Data) {
		return verdict(reasonf(ReasonSeed, "cv.md", "your cv.md still holds the seed text; replace it with your CV"))
	}
	text := string(c.Data)
	var rs []Reason
	if n := utf8.RuneCountInString(strings.TrimSpace(text)); n < minCVRunes {
		rs = append(rs, reasonf(ReasonBlank, "cv.md", fmt.Sprintf("cv.md has %d characters; at least %d are needed", n, minCVRunes)))
	}
	for _, t := range placeholderTokens(text) {
		rs = append(rs, reasonf(ReasonPlaceholder, "cv.md", "your cv.md still contains the placeholder "+t))
	}
	for _, p := range containsPersona(text) {
		rs = append(rs, reasonf(ReasonPersona, "cv.md", "cv.md contains the example value \""+p+"\""))
	}
	draft := cvparse.Parse(text)
	if !draft.Dated() {
		rs = append(rs, reasonf(ReasonInvalid, "cv.md", "no dated experience or education entry was found; add a line such as `### Engineer, Acme Ltd (2020-01 to present)` under an Experience heading"))
	}
	if p, _, bad := profileData(env); bad == nil && len(draft.Employment)+len(draft.Education)+len(draft.Skills) > 0 &&
		len(p.History.Employment)+len(p.History.Education)+len(p.Skills) == 0 {
		rs = append(rs, reasonf(ReasonReview, "history", "the extracted draft ("+draft.Summary()+") awaits review: confirm, edit or remove each entry"))
	}
	return verdict(rs...)
}

// ImportCVFile reads only the explicitly selected file. Extraction has the same
// input/text bounds and cancellation as the dashboard preview and never writes.
func ImportCVFile(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", FieldErrors{"markdown": err.Error()}
	}
	if !info.Mode().IsRegular() {
		return "", FieldErrors{"markdown": "select a regular CV file"}
	}
	if info.Size() > toolchain.MaxPDFBytes {
		return "", FieldErrors{"markdown": "CV files are limited to 10 MiB"}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", FieldErrors{"markdown": err.Error()}
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", FieldErrors{"markdown": "select a regular CV file"}
	}
	data, err := io.ReadAll(io.LimitReader(f, toolchain.MaxPDFBytes+1))
	if err == nil {
		var text string
		text, err = toolchain.ExtractCVText(ctx, path, data)
		if err == nil {
			return text, nil
		}
	}
	return "", FieldErrors{"markdown": err.Error()}
}
