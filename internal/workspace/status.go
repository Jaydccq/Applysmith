package workspace

import "sync"

// File states reported by Status.
const (
	StatusMissing  = "missing"
	StatusTemplate = "template"
	StatusInvalid  = "invalid"
	StatusReady    = "ready"
)

// FileStatus describes one workspace file.
type FileStatus struct {
	File     File
	Name     string
	Status   string
	Optional bool
	Revision string
	Report   Report
}

// Status is the workspace status served to setup and the dashboard.
type Status struct {
	Files []FileStatus
}

// Status inspects every user-owned file.
func (w *Workspace) Status() (Status, error) {
	var st Status
	for _, f := range Files {
		c, err := w.Read(f)
		if err != nil {
			return Status{}, err
		}
		entry := FileStatus{File: f, Name: f.Name(), Optional: f.Optional(), Revision: c.Revision}
		switch {
		case c.Revision == RevisionAbsent:
			entry.Status = StatusMissing
		case IsTemplate(f, c.Data):
			entry.Status = StatusTemplate
		default:
			entry.Report = w.validateKnown(f, c)
			entry.Status = StatusReady
			if !entry.Report.OK() {
				entry.Status = StatusInvalid
			}
		}
		st.Files = append(st.Files, entry)
	}
	return st, nil
}

// validated holds, per file, the report of the last content Status validated.
// Validating the answer bank (tens to hundreds of kilobytes of YAML) took most
// of Status, which the setup state runs on every Home load. The profile is
// left out: its check also reads the résumé files on disk.
var validated sync.Map // File -> validation

type validation struct {
	revision string
	report   Report
}

func (w *Workspace) validateKnown(f File, c Content) Report {
	if f == FileProfile {
		return w.Validate(f, c.Data)
	}
	if v, ok := validated.Load(f); ok && v.(validation).revision == c.Revision {
		return v.(validation).report
	}
	rep := Validate(f, c.Data)
	validated.Store(f, validation{c.Revision, rep})
	return rep
}

// LoadPolicy returns the policy in force. A missing, unreadable or invalid
// file yields the restrictive policy together with the reason.
func (w *Workspace) LoadPolicy() (Policy, Report) {
	c, err := w.Read(FilePolicy)
	switch {
	case err != nil:
		return RestrictivePolicy(), Report{Issues: []Issue{{File: FilePolicy.Name(), Code: CodeFileMissing, Message: "cannot be read: " + err.Error()}}}
	case c.Revision == RevisionAbsent:
		return RestrictivePolicy(), Report{Issues: []Issue{{File: FilePolicy.Name(), Code: CodeFileMissing, Message: "file does not exist"}}}
	}
	return ParsePolicy(c.Data)
}

// FollowupPolicy is policy.yml's follow-up cadence, or the defaults with the
// report of an invalid file.
func (w *Workspace) FollowupPolicy() (Followup, *Report) {
	p, rep := w.LoadPolicy()
	for _, issue := range rep.Issues {
		if issue.Code != CodeFileMissing {
			return DefaultPolicy().Tracker.Followup, &rep
		}
	}
	return p.Tracker.Followup, nil
}
