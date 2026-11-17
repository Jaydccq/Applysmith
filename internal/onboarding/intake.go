package onboarding

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"applysmith/internal/api/wire"
	"applysmith/internal/intake"
	"applysmith/internal/workspace"
)

// IntakePreview reads the selected documents with the CV parser and compares
// them with profile.yml. It writes nothing; the revision it returns is the one
// IntakeAccept must be given.
func IntakePreview(_ context.Context, env *Env, in []wire.IntakeDocument) (wire.IntakePreview, error) {
	docs, err := intake.Prepare(in)
	if err != nil {
		return wire.IntakePreview{}, asFieldErrors(err)
	}
	p, rev, err := intakeProfile(env)
	if err != nil {
		return wire.IntakePreview{}, err
	}
	cs := intake.Build(docs, p)
	out := wire.IntakePreview{Revision: rev, Documents: intake.Infos(docs), Items: cs.Items, Notes: []string{}}
	for _, d := range out.Documents {
		if d.Duplicate {
			out.Notes = append(out.Notes, d.Label+" has the same content as an earlier document and was read once")
		}
	}
	if len(cs.Items) == 0 {
		out.Notes = append(out.Notes, "the documents hold no job, study or skill that the parser can read; the CV parser needs dated entries under Experience and Education headings")
	}
	out.Notes = append(out.Notes, "nothing is stored until you accept items; a skill a course, repository, project or reference only mentions is a question, not a fact")
	return out, nil
}

// IntakeAccept applies the selected items of a preview to profile.yml. The
// change set is rebuilt from the same documents and must come from the revision
// the preview was built from. It writes only through the revisioned profile
// edit, and never touches cv.md or the answer bank.
func IntakeAccept(_ context.Context, env *Env, req wire.IntakeAcceptRequest) (wire.IntakeResult, error) {
	docs, err := intake.Prepare(req.Documents)
	if err != nil {
		return wire.IntakeResult{}, asFieldErrors(err)
	}
	p, rev, err := intakeProfile(env)
	if err != nil {
		return wire.IntakeResult{}, err
	}
	if req.Revision != rev {
		return wire.IntakeResult{}, &StaleError{Message: staleProfile}
	}
	applied, err := intake.Build(docs, p).Select(req.Selections)
	if err != nil {
		return wire.IntakeResult{}, asFieldErrors(err)
	}
	res := wire.IntakeResult{Revision: rev, Applied: applied.Items, Notes: []string{}}
	if res.Applied == nil {
		res.Applied = []wire.IntakeItem{}
	}
	if len(applied.Ops) == 0 {
		return res, nil
	}
	wrote, err := editProfileAt(env, applied.Ops, false, rev)
	if err != nil {
		return wire.IntakeResult{}, err
	}
	res.Wrote = wrote
	cur, err := env.Workspace.Read(workspace.FileProfile)
	if err != nil {
		return wire.IntakeResult{}, &EnvError{Message: "profile.yml cannot be read", Err: err}
	}
	res.Revision = cur.Revision
	return res, nil
}

func asFieldErrors(err error) error {
	var rej intake.Rejection
	if errors.As(err, &rej) {
		return FieldErrors(rej)
	}
	return err
}

// intakeProfile is the profile the change set is built against, with the
// revision of the bytes it was read from.
func intakeProfile(env *Env) (workspace.Profile, string, error) {
	p, data, bad := profileData(env)
	if bad != nil {
		msg := "profile.yml is not ready"
		if len(bad.Reasons) > 0 {
			msg += ": " + bad.Reasons[0].Text
		}
		return workspace.Profile{}, "", &RejectedError{Message: msg}
	}
	return p, workspace.Revision(data), nil
}

// LoadIntakeFiles reads the files the user named, each as "kind:path" or a bare
// path, which is a CV. A directory is refused, nothing is searched for, and the
// workspace's own generated documents and reports are not evidence.
func LoadIntakeFiles(w *workspace.Workspace, specs []string) ([]wire.IntakeDocument, error) {
	if len(specs) > intake.MaxDocuments {
		return nil, FieldErrors{"documents": fmt.Sprintf("a batch holds at most %d documents, got %d", intake.MaxDocuments, len(specs))}
	}
	out := make([]wire.IntakeDocument, 0, len(specs))
	for i, spec := range specs {
		field := fmt.Sprintf("documents[%d]", i)
		kind, path := wire.IntakeKindCV, spec
		if k, rest, ok := strings.Cut(spec, ":"); ok && isIntakeKind(k) {
			kind, path = k, rest
		}
		if err := checkIntakePath(w, path); err != nil {
			return nil, FieldErrors{field: err.Error()}
		}
		st, err := os.Stat(path)
		switch {
		case err != nil:
			return nil, FieldErrors{field: err.Error()}
		case !st.Mode().IsRegular():
			return nil, FieldErrors{field: path + " is not a file; name each document, nothing is searched for"}
		case st.Size() > intake.MaxDocBytes:
			return nil, FieldErrors{field: path + " is larger than 10 MiB"}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, FieldErrors{field: err.Error()}
		}
		out = append(out, wire.IntakeDocument{Name: filepath.Base(path), Kind: kind, Text: string(b)})
	}
	return out, nil
}

func isIntakeKind(k string) bool {
	for _, x := range intake.Kinds() {
		if x == k {
			return true
		}
	}
	return false
}

// checkIntakePath refuses a file the workspace generated: application
// documents and reports are written from the profile, so reading them back
// would present the program's own wording as new evidence.
func checkIntakePath(w *workspace.Workspace, path string) error {
	if w == nil {
		return nil
	}
	abs, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s does not exist", path)
		}
		return err
	}
	root, err := filepath.EvalSymlinks(w.Root)
	if err != nil {
		return nil
	}
	for _, dir := range []string{"documents", "reports", workspace.StateDirName} {
		if rel, err := filepath.Rel(filepath.Join(root, dir), abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s is inside the workspace's %s folder, which holds generated files; they are not evidence", path, dir)
		}
	}
	return nil
}
