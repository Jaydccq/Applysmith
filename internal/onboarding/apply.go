package onboarding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"applysmith/internal/workspace"
)

// decodeInput decodes a writer's JSON input. Unknown fields and wrongly typed
// fields are reported per field; an empty input is an empty object.
func decodeInput(raw json.RawMessage, fields []Field, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return FieldErrors{"input": "must be one JSON object"}
	}
	fe := FieldErrors{}
	known := map[string]bool{}
	for _, f := range fields {
		known[f.Name] = true
	}
	for k := range keys {
		if !known[k] {
			fe.add(k, "unknown field")
		}
	}
	if len(fe) > 0 {
		return fe
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			fe.add(te.Field, "expected "+te.Type.String())
		} else if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			fe.add(strings.Trim(name, `"`), "unknown field")
		} else {
			fe.add("input", err.Error())
		}
		return fe
	}
	return nil
}

// yamlValue renders a Go value as YAML flow text; JSON is valid YAML.
func yamlValue(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func setOp(path string, v any) workspace.Op {
	return workspace.Op{Kind: workspace.OpSet, Path: path, Value: yamlValue(v)}
}

// profileRead is one read of profile.yml that the detectors of one
// evaluation share; before, each of them parsed it again.
type profileRead struct {
	once sync.Once
	p    workspace.Profile
	raw  []byte
	bad  *Detection
}

// profileData reads profile.yml; absent is reported as a missing Detection.
func profileData(env *Env) (workspace.Profile, []byte, *Detection) {
	if m := env.profile; m != nil {
		m.once.Do(func() { m.p, m.raw, m.bad = readProfile(env) })
		return m.p, m.raw, m.bad
	}
	return readProfile(env)
}

func readProfile(env *Env) (workspace.Profile, []byte, *Detection) {
	if env.Workspace == nil {
		d := missing("no workspace could be resolved")
		return workspace.Profile{}, nil, &d
	}
	c, err := env.Workspace.Read(workspace.FileProfile)
	if err != nil {
		d := verdict(reasonf(ReasonInvalid, "profile.yml", err.Error()))
		return workspace.Profile{}, nil, &d
	}
	if c.Revision == workspace.RevisionAbsent {
		d := missing("profile.yml does not exist; complete the workspace step first")
		return workspace.Profile{}, nil, &d
	}
	p, rep := workspace.ParseProfile(c.Data)
	if !rep.OK() {
		var rs []Reason
		for _, is := range rep.Issues {
			rs = append(rs, reasonf(ReasonInvalid, is.Path, is.Error()))
		}
		d := verdict(rs...)
		return p, c.Data, &d
	}
	return p, c.Data, nil
}

// editProfile applies operations to profile.yml through the workspace write
// path (validation, revision check, backup). It reports whether the bytes
// changed. With dry set it validates and writes nothing; pending names the
// issue paths (such as resumes[2].file) of files the real call would create
// first, whose "does not exist" a dry run does not count.
func editProfile(env *Env, ops []workspace.Op, dry bool, pending ...string) (bool, error) {
	return editProfileAt(env, ops, dry, "", pending...)
}

// editProfileAt is editProfile for edits built against one revision of
// profile.yml: when revision is not empty and the file has moved on, nothing is
// written, because list positions in the edits may no longer mean the same
// entries.
func editProfileAt(env *Env, ops []workspace.Op, dry bool, revision string, pending ...string) (bool, error) {
	if env.Workspace == nil {
		return false, &EnvError{Message: "no workspace could be resolved"}
	}
	cur, err := env.Workspace.Read(workspace.FileProfile)
	if err != nil {
		return false, &EnvError{Message: "profile.yml cannot be read", Err: err}
	}
	if cur.Revision == workspace.RevisionAbsent {
		return false, &EnvError{Message: "profile.yml does not exist; complete the workspace step first"}
	}
	if revision != "" && cur.Revision != revision {
		return false, &StaleError{Message: staleProfile}
	}
	out, err := workspace.ApplyOps(cur.Data, ops)
	if err != nil {
		return false, &EnvError{Message: "profile.yml cannot be edited in place", Err: err}
	}
	if bytes.Equal(out, cur.Data) {
		return false, nil
	}
	if rep := env.Workspace.Validate(workspace.FileProfile, out); !rep.OK() {
		fe := FieldErrors{}
		for _, is := range rep.Issues {
			if dry && is.Code == workspace.CodeFileMissing && slices.Contains(pending, is.Path) {
				continue
			}
			fe.add(is.Path, is.Message)
		}
		if err := fe.orNil(); err != nil {
			return false, err
		}
	}
	if dry {
		return true, nil
	}
	if _, err := env.Workspace.Write(workspace.FileProfile, out, cur.Revision); err != nil {
		return false, &EnvError{Message: "profile.yml could not be written", Err: err}
	}
	return true, nil
}

// sectionHash is a hash of selected top-level profile keys, so an
// acknowledgement stops applying only when its own data changes.
func sectionHash(profile []byte, keys ...string) string {
	var doc map[string]any
	if yaml.Unmarshal(profile, &doc) != nil {
		return ""
	}
	sub := map[string]any{}
	for _, k := range keys {
		sub[k] = doc[k]
	}
	b, _ := json.Marshal(sub)
	return hashBytes(b)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func profileScope(keys ...string) func(context.Context, *Env) (string, error) {
	return func(_ context.Context, env *Env) (string, error) {
		_, raw, bad := profileData(env)
		if bad != nil {
			return "", fmt.Errorf("profile.yml is not readable")
		}
		return sectionHash(raw, keys...), nil
	}
}

func mustStep(id string) Step {
	s, ok := StepByID(id)
	if !ok {
		panic("onboarding: unknown step " + id)
	}
	return s
}

func itoa(n int) string { return fmt.Sprint(n) }

// sortReasons orders reasons by field so output is stable (map iteration
// above is unordered).
func sortReasons(rs []Reason) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Field < rs[j].Field })
}
