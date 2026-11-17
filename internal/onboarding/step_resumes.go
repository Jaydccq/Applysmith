package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

const maxResumeFile = 10 << 20

var resumeFields = []Field{
	{Name: "roster", Type: "object-list", Doc: "The whole résumé roster, replacing the stored one: id (lowercase letters, digits, hyphens), label, file (resumes/<name>.pdf), optional tags, description and default. Exactly one entry is the default."},
	{Name: "add", Type: "object", Doc: "Copy one local PDF into resumes/ and add it to the roster: path (a PDF of at most 10 MiB), id, label and optional tags and default."},
	{Name: "remove", Type: "object", Doc: "Remove one roster entry by id and delete its file from resumes/. The default entry cannot be removed while others remain."},
	{Name: "set_default", Type: "object", Doc: "Make the roster entry with this id the default."},
}

type rosterEntry struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	File         string   `json:"file"`
	Tags         []string `json:"tags,omitempty"`
	Description  string   `json:"description,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	Default      bool     `json:"default,omitempty"`
}

type addResume struct {
	Path    string   `json:"path"`
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Tags    []string `json:"tags"`
	Default bool     `json:"default"`
}

type resumeRef struct {
	ID string `json:"id"`
}

type resumeInput struct {
	Roster     *[]rosterEntry `json:"roster"`
	Add        *addResume     `json:"add"`
	Remove     *resumeRef     `json:"remove"`
	SetDefault *resumeRef     `json:"set_default"`
}

func detectResumes(_ context.Context, env *Env) Detection {
	p, raw, bad := profileData(env)
	if bad != nil {
		return *bad
	}
	if len(p.Resumes) == 0 {
		return missing("no résumé PDF is registered; upload one and add it to the roster")
	}
	var rs []Reason
	// The workspace check names every roster file that is missing, too large or
	// not a PDF; the roster's own rules (one default, unique ids) come with it.
	if rep := env.Workspace.Validate(workspace.FileProfile, raw); !rep.OK() {
		for _, is := range rep.Issues {
			rs = append(rs, reasonf(ReasonInvalid, is.Path, is.Message))
		}
	}
	return verdict(rs...)
}

func applyResumes(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in resumeInput
	if err := decodeInput(raw, resumeFields, &in); err != nil {
		return Applied{}, err
	}
	if env.Workspace == nil {
		return Applied{}, &EnvError{Message: errNoWorkspace.Error()}
	}
	given := 0
	for _, set := range []bool{in.Roster != nil, in.Add != nil, in.Remove != nil, in.SetDefault != nil} {
		if set {
			given++
		}
	}
	if given > 1 {
		return Applied{}, FieldErrors{"add": "give one of roster, add, remove or set_default, not several"}
	}
	p, _, bad := profileData(env)
	if bad != nil && bad.Status == StepMissing {
		return Applied{}, &EnvError{Message: bad.Reasons[0].Text}
	}
	roster := make([]rosterEntry, 0, len(p.Resumes))
	for _, r := range p.Resumes {
		roster = append(roster, rosterEntry{ID: r.ID, Label: r.Label, File: r.File, Tags: r.Tags, Description: r.Description, Instructions: r.Instructions, Default: r.Default})
	}
	var notes []string
	var stored, removed string
	switch {
	case in.Roster != nil:
		roster = *in.Roster
	case in.Add != nil:
		entry, err := addFromFile(ctx, env, *in.Add, len(roster) == 0, dry)
		if err != nil {
			return Applied{}, err
		}
		if entry.Default {
			for i := range roster {
				roster[i].Default = false
			}
		}
		roster = append(roster, entry)
		if dry {
			notes = append(notes, "would store "+entry.File)
		} else {
			stored = entry.File
			notes = append(notes, "stored "+entry.File)
		}
	case in.Remove != nil:
		var err error
		if roster, removed, err = withoutEntry(roster, in.Remove.ID); err != nil {
			return Applied{}, err
		}
		notes = append(notes, "removed "+in.Remove.ID)
	case in.SetDefault != nil:
		if err := makeDefault(roster, in.SetDefault.ID); err != nil {
			return Applied{}, err
		}
		notes = append(notes, in.SetDefault.ID+" is the default")
	default:
		return Applied{Step: "resumes", DryRun: dry, Detection: Evaluate(ctx, env, mustStep("resumes"))}, nil
	}
	var pending []string
	if in.Add != nil && dry {
		pending = append(pending, fmt.Sprintf("resumes[%d].file", len(roster)-1))
	}
	changed, err := editProfile(env, []workspace.Op{setOp("resumes", roster)}, dry, pending...)
	if err != nil {
		// The file this call stored was not referenced before and is not now.
		if stored != "" {
			_ = env.Workspace.RemoveResume(stored)
		}
		return Applied{}, err
	}
	if removed != "" && !dry && !referenced(roster, removed) {
		if err := env.Workspace.RemoveResume(removed); err != nil {
			notes = append(notes, "the file "+removed+" could not be deleted: "+err.Error())
		}
	}
	return Applied{Step: "resumes", DryRun: dry, Wrote: changed && !dry, Detection: Evaluate(ctx, env, mustStep("resumes")), Notes: notes}, nil
}

// withoutEntry drops the entry with the id and names its file. The default is
// kept while others remain, because a roster always has exactly one.
func withoutEntry(roster []rosterEntry, id string) ([]rosterEntry, string, error) {
	for i, r := range roster {
		if r.ID != id {
			continue
		}
		if r.Default && len(roster) > 1 {
			return nil, "", FieldErrors{"remove.id": "is the default; make another résumé the default first"}
		}
		out := append(append([]rosterEntry{}, roster[:i]...), roster[i+1:]...)
		return out, r.File, nil
	}
	return nil, "", FieldErrors{"remove.id": "no résumé has this id"}
}

func makeDefault(roster []rosterEntry, id string) error {
	found := false
	for i := range roster {
		roster[i].Default = roster[i].ID == id
		found = found || roster[i].Default
	}
	if !found {
		return FieldErrors{"set_default.id": "no résumé has this id"}
	}
	return nil
}

func referenced(roster []rosterEntry, file string) bool {
	for _, r := range roster {
		if r.File == file {
			return true
		}
	}
	return false
}

// addFromFile checks the PDF and, unless dry, copies it into resumes/. A dry
// run checks the source and the stored name exactly as the copy would. The
// first résumé of an empty roster is the default whatever the input says,
// because a roster always has exactly one.
func addFromFile(ctx context.Context, env *Env, a addResume, first, dry bool) (rosterEntry, error) {
	fe := FieldErrors{}
	if strings.TrimSpace(a.Path) == "" {
		fe.add("add.path", "name the PDF to add")
	}
	if strings.TrimSpace(a.ID) == "" {
		fe.add("add.id", "give the résumé an id: lowercase letters, digits and hyphens")
	}
	if err := fe.orNil(); err != nil {
		return rosterEntry{}, err
	}
	data, err := readResume(ctx, env, a.Path)
	if err != nil {
		return rosterEntry{}, err
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return rosterEntry{}, FieldErrors{"add.path": "is not a PDF"}
	}
	store := env.Workspace.StoreResume
	if dry {
		store = env.Workspace.CheckResume
	}
	rel, err := store(a.ID, data)
	if err != nil {
		return rosterEntry{}, FieldErrors{"add.id": err.Error()}
	}
	label := a.Label
	if label == "" {
		label = a.ID
	}
	return rosterEntry{ID: a.ID, Label: label, File: rel, Tags: a.Tags, Default: a.Default || first}, nil
}

// resumeReadTimeout bounds reading the PDF to add. On macOS a read in a
// protected folder (Desktop, Documents, Downloads, iCloud Drive, a removable
// volume) waits while the system asks whether Applysmith may open it, which a
// request must not wait on.
var resumeReadTimeout = 15 * time.Second

// readResumeFile reads the PDF to add after checking it is a regular file of
// at most 10 MiB. A test replaces it.
var readResumeFile = func(path string) ([]byte, error) {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, FieldErrors{"add.path": "is not a regular file"}
	case info.Size() > maxResumeFile:
		return nil, FieldErrors{"add.path": "is larger than 10 MiB"}
	}
	return os.ReadFile(path)
}

// readResume reads the PDF within resumeReadTimeout. A read the system holds
// or refuses names the privacy setting to change; the held read itself is
// left to finish on its own.
func readResume(ctx context.Context, env *Env, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, resumeReadTimeout)
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := readResumeFile(path)
		done <- result{data, err}
	}()
	shown := path
	if home := env.HomeDir; home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		shown = "~" + path[len(home):]
	}
	fix := "allow Applysmith in System Settings › Privacy & Security › Files and Folders (or Full Disk Access), or choose a file elsewhere, for example in your home folder"
	select {
	case r := <-done:
		var fe FieldErrors
		switch {
		case r.err == nil:
			return r.data, nil
		case errors.As(r.err, &fe):
			return nil, fe
		case errors.Is(r.err, fs.ErrPermission):
			return nil, FieldErrors{"add.path": "the system did not let Applysmith read " + shown + "; " + fix}
		default:
			return nil, FieldErrors{"add.path": "the file cannot be read: " + r.err.Error()}
		}
	case <-ctx.Done():
		return nil, FieldErrors{"add.path": fmt.Sprintf("reading %s did not finish within %s: macOS may be asking whether Applysmith may open that folder; %s", shown, resumeReadTimeout, fix)}
	}
}
