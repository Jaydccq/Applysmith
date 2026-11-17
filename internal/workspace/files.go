package workspace

import "fmt"

// File names one of the configuration and document files served through the
// revisioned edit paths. The string is the {file} token of the config API.
type File string

const (
	FileProfile    File = "profile"
	FileAnswerBank File = "answer-bank"
	FilePortals    File = "portals"
	FilePolicy     File = "policy"
	FileCV         File = "cv"
	FileNarrative  File = "narrative"
	FileVoice      File = "voice"
)

// Files lists every File in status order.
var Files = []File{FileProfile, FileAnswerBank, FilePortals, FilePolicy, FileCV, FileNarrative, FileVoice}

// ParseFile maps an API token to a File.
func ParseFile(token string) (File, error) {
	for _, f := range Files {
		if string(f) == token {
			return f, nil
		}
	}
	return "", newError(CodeConfigInvalid, "", "unknown configuration file %q", token)
}

// Name is the file name inside the workspace.
func (f File) Name() string {
	if f.IsYAML() {
		return string(f) + ".yml"
	}
	return string(f) + ".md"
}

func (f File) IsYAML() bool {
	switch f {
	case FileProfile, FileAnswerBank, FilePortals, FilePolicy:
		return true
	}
	return false
}

// Optional files are never reported missing as an error and never seeded.
func (f File) Optional() bool { return f == FileVoice }

// Seeded reports whether init writes the file from an embedded template. The
// answer bank is not: its default content belongs to the answer-bank service,
// whose Seed runs wherever a workspace is set up or a server starts.
func (f File) Seeded() bool { return !f.Optional() && f != FileAnswerBank }

// maxDocumentBytes caps cv.md, narrative.md and voice.md.
const maxDocumentBytes = 256 << 10

// Validate checks content for the file without touching the disk. A Markdown
// document is checked for size (payload_too_large, which Write returns as
// that error) and UTF-8.
func Validate(f File, data []byte) Report {
	switch f {
	case FileProfile:
		_, rep := ParseProfile(data)
		return rep
	case FilePortals:
		_, rep := ParsePortals(data)
		return rep
	case FilePolicy:
		_, rep := ParsePolicy(data)
		return rep
	case FileAnswerBank:
		rep, _ := validateDoc[AnswerBankEnvelope](f.Name(), data)
		return rep
	case FileCV, FileNarrative, FileVoice:
		return validateDocument(f, data)
	}
	panic(fmt.Sprintf("workspace: unknown file %q", f))
}
