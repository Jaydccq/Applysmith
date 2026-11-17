package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const (
	logTailLines = 200
	// logTailBytes bounds how much of the log file is read to find its last
	// lines.
	logTailBytes = 256 << 10
)

func (s *Server) systemInfo(*apikit.Call) (apikit.Reply, error) {
	return apikit.Reply{Data: wire.SystemInfo{WorkspacePath: s.cfg.Workspace.Root}}, nil
}

// logTail returns the last n lines of the server log with every remembered
// secret replaced. The log is written through the same redactor; reading it
// through the redactor again keeps a value stored after the line was written
// out of the page.
func (s *Server) logTail(n int) ([]string, error) {
	f, err := os.Open(filepath.Join(s.cfg.Workspace.StateDir(), "logs", logFileName))
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := max(0, info.Size()-logTailBytes)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, logTailBytes))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line may be cut
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" {
			out = append(out, vault.Redact(l))
		}
	}
	return out, nil
}

func (s *Server) systemLogs(*apikit.Call) (apikit.Reply, error) {
	lines, err := s.logTail(logTailLines)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.LogLines{Lines: lines}}, nil
}

// diagnostics is the export a user can attach to a report: versions, the
// health components, the doctor results and the redacted log tail. It holds no
// secret value and no workspace content.
type diagnostics struct {
	Version       string              `json:"version"`
	BuildID       string              `json:"buildId"`
	APIVersion    int                 `json:"apiVersion"`
	SchemaVersion int                 `json:"schemaVersion"`
	Platform      string              `json:"platform"`
	Components    []wire.Component    `json:"components"`
	Doctor        []wire.DoctorResult `json:"doctor"`
	Log           []string            `json:"log"`
}

func (s *Server) systemDiagnostics(c *apikit.Call) (apikit.Reply, error) {
	var doctor []wire.DoctorResult
	if h, err := s.setupHost(); err == nil && s.cfg.Setup != nil {
		if rep, err := s.cfg.Setup.Doctor(c.R.Context(), h); err == nil {
			doctor = rep.Results
		}
	}
	if doctor == nil {
		doctor = []wire.DoctorResult{}
	}
	lines, err := s.logTail(logTailLines)
	if err != nil {
		return apikit.Reply{}, err
	}
	doc := diagnostics{Version: s.cfg.Version, BuildID: s.cfg.BuildID, APIVersion: wire.APIVersion, SchemaVersion: store.SchemaVersion(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Components: s.liveComponents(s.components.Load()), Doctor: doctor, Log: lines}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return apikit.Reply{}, err
	}
	body := []byte(vault.Redact(buf.String()))
	h := c.W.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="applysmith-diagnostics.json"`)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(body)
	c.Handled = true
	return apikit.Reply{}, nil
}

// putPolicyConfig replaces policy.yml from the raw editor. Like the structured
// edit it admits the dashboard session on this computer only.
func (s *Server) putPolicyConfig(c *apikit.Call) (apikit.Reply, error) {
	if c.P.Kind != apikit.PrincipalDashboard || !fromLoopback(c) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "the policy is edited from the dashboard on this computer")
	}
	var req wire.ConfigSave
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.ExpectedRevision == "" {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "expectedRevision is required; read the file first and quote its revision")
	}
	saved, err := s.cfg.Workspace.Write(workspace.FilePolicy, []byte(req.Content), req.ExpectedRevision)
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	return apikit.Reply{Data: wire.ConfigSaved{Revision: saved.Revision, Changed: saved.Changed}}, nil
}
