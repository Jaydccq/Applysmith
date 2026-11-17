package agent

import "slices"

// SpecCatalog is the tool catalog the ask-agent specification fixes: every
// tool and its permission class. The registry is built from the tools whose
// services exist; Missing names the rest, and a test pins what remains.
var SpecCatalog = map[string]Class{
	"tracker_query": ClassRead, "find_report": ClassRead, "list_pipeline": ClassRead, "application_workspace": ClassRead,
	"submissions_recent": ClassRead, "gmail_signals": ClassRead, "gmail_action_inbox": ClassRead, "gmail_digest": ClassRead,
	"gmail_deadlines": ClassRead, "gmail_thread": ClassRead, "gmail_search": ClassRead,
	"apply_list_status": ClassRead, "kb_search": ClassRead, "retrieve_evidence": ClassRead, "recall_session": ClassRead,
	"profile_summary": ClassRead, "load_prompt_mode": ClassRead, "list_schedules": ClassRead, "preview_schedules": ClassRead,
	"list_scheduled_prompts": ClassRead, "read_page": ClassRead, "answer_bank_lookup": ClassRead, "answer_bank_find": ClassRead, "story_context": ClassRead,
	"wiki_find": ClassRead, "wiki_read": ClassRead,
	"list_goals": ClassRead, "activity_today": ClassRead, ToolDesktopWindows: ClassRead, ToolDesktopLook: ClassRead, ToolRunQuestions: ClassRead,
	ToolApplyPage: ClassRead, ToolApplyReview: ClassRead, ToolLoadSkill: ClassRead,

	"application_plan": ClassDraft, "draft_application_answer": ClassDraft, "research_question": ClassDraft, "story_mining": ClassDraft,
	"propose_rule": ClassDraft, "propose_schedule_change": ClassDraft, "propose_goal_change": ClassDraft,
	ToolTaskUpdate: ClassDraft, ToolTaskAsk: ClassDraft, ToolTaskDone: ClassDraft,
	"draft_outreach": ClassDraft, "draft_followup": ClassDraft, "decode_posting": ClassDraft, "draft_gmail_reply": ClassDraft,

	"save_fact": ClassWriteLocal, "amend_fact": ClassWriteLocal, "forget_fact": ClassWriteLocal, "answer_bank_save": ClassWriteLocal, "answer_bank_forget": ClassWriteLocal, "wiki_write": ClassWriteLocal,
	"record_story_answer": ClassWriteLocal, "manage_scheduled_prompt": ClassWriteLocal, "batch_skip_list": ClassWriteLocal,
	"tailor_resume": ClassWriteLocal, "manage_goal": ClassWriteLocal, ToolApplyLeave: ClassWriteLocal, ToolDelegateTask: ClassWriteLocal,
	"gmail_thread_action": ClassWriteLocal, "tracker_set_status": ClassWriteLocal,

	"evaluate_job": ClassExternalAction, "scan_portal": ClassExternalAction, "autofill_application": ClassExternalAction,
	"apply_from_list": ClassExternalAction, "prepare_registration": ClassExternalAction,
	ToolDesktopClick: ClassExternalAction, ToolDesktopType: ClassExternalAction, ToolDesktopKeys: ClassExternalAction, ToolDesktopScroll: ClassExternalAction,
	ToolAnswerRunQuestion: ClassExternalAction,
	ToolApplyFill:         ClassExternalAction, ToolApplyAnswer: ClassExternalAction, ToolApplyPress: ClassExternalAction, ToolApplyContinue: ClassExternalAction,
}

// ToolClass is the permission class the specification gives a tool, or the
// empty string for a name outside the catalog.
func ToolClass(name string) string { return string(SpecCatalog[name]) }

// BuildRegistry registers the tools whose services are given and the two
// apply tools.
func BuildRegistry(d ToolDeps) (*Registry, error) {
	tools := append(ReadTools(d), MemoryTools(d.Memory)...)
	tools = append(tools, GoalTools(d.Goals)...)
	tools = append(tools, TaskTools(d.Tasks)...)
	tools = append(tools, ScheduledPromptTools(d.ScheduledPrompts)...)
	tools = append(tools, ActionTools(d)...)
	tools = append(tools, WriteTools(d)...)
	tools = append(tools, Layer3Tools(d)...)
	tools = append(tools, MessageTools(d)...)
	tools = append(tools, DesktopTools(d)...)
	tools = append(tools, FinishTools(d.StoppedRuns)...)
	if d.ApplyRun != nil {
		tools = append(tools, ApplyRunTools(d.ApplyRun)...)
		tools = append(tools, loadSkillTool())
	}
	return NewRegistry(d.RunGate.wrap(append(tools, applyTools(d)...))...)
}

// Missing lists the catalog tools the registry does not declare yet, sorted.
func (r *Registry) Missing() []string {
	var out []string
	for name := range SpecCatalog {
		if _, ok := r.tools[name]; !ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
