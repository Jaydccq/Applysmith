package guard

import "strings"

// Class B paths are internal working material: allowed in the private tree,
// forbidden in a publication. The export step drops them and release mode
// fails when one is still present.

var classBDirs = map[string]bool{
	// Agent tool configuration for any coding-agent tool.
	".claude": true, ".codex": true, ".cursor": true, ".windsurf": true, ".gemini": true,
	".continue": true, ".cline": true, ".roo": true, ".opencode": true, ".kiro": true,
	".agents": true, ".aider": true, ".amazonq": true, ".junie": true, ".trae": true,
}

// Planning, research and audit directories, at the root or directly under docs/.
var classBPlanningDirs = map[string]bool{
	"exec-plans": true, "plans": true, "planning": true, "research": true,
	"prd": true, "prds": true, "notes": true, "agents": true, "memory": true,
	"memory-notes": true, "parity": true, "parity-audit": true, "parity-audits": true, "audits": true,
}

var classBGitHubDirs = map[string]bool{
	"instructions": true, "prompts": true, "agents": true, "hooks": true, "chatmodes": true,
}

var classBFiles = map[string]bool{
	"agents.md": true, "claude.md": true, "gemini.md": true, "qwen.md": true, "codex.md": true,
	".cursorrules": true, ".windsurfrules": true, ".clinerules": true, ".aider.conf.yml": true,
	"copilot-instructions.md": true, "memory.md": true, "plan.md": true, "plans.md": true,
	"todo.md": true, "notes.md": true, "prd.md": true,
}

// ClassBReason names why p is Class B, or returns "".
func ClassBReason(p string) string {
	parts := strings.Split(p, "/")
	dirs, base := parts[:len(parts)-1], parts[len(parts)-1]
	for i, d := range dirs {
		switch {
		case d == "openspec":
			return "the openspec planning directory"
		case classBDirs[d]:
			return "agent tool configuration directory"
		case classBPlanningDirs[d] && (i == 0 || (i == 1 && dirs[0] == "docs")):
			return "planning, research, memory or audit directory"
		case d == ".github" && i+1 < len(dirs) && classBGitHubDirs[dirs[i+1]]:
			return "agent instruction or hook directory"
		}
	}
	lower := strings.ToLower(base)
	switch {
	case classBFiles[lower]:
		return "agent instruction, memory or planning file"
	case strings.HasPrefix(lower, "parity-audit"), strings.HasPrefix(lower, "parity_audit"):
		return "parity-audit file"
	}
	return ""
}
