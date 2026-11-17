// Clean-room: docs/provenance.md#CR-AGENT-TASKS

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"applysmith/internal/agent"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/agent/tasks"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
)

// agentTaskRoutes serve the task panel of the Ask page: the user sees each
// delegated task's plan and state and pauses, resumes or stops it. Resume
// lets a task go on by itself again, so the routes stay off remote listeners.
var agentTaskRoutes = []Route{
	route("GET", "/api/agent/tasks", reads("AgentTaskList")),
	route("POST", "/api/agent/tasks/{id}/pause", reads("AgentTask")),
	route("POST", "/api/agent/tasks/{id}/resume", reads("AgentTask")),
	route("POST", "/api/agent/tasks/{id}/stop", reads("AgentTask")),
	route("POST", "/api/agent/ideas/{idea}", func(r *Route) { reads("AgentIdeaStarted")(r); r.SessionOnly = true }),
}

func taskError(err error) error {
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such task")
	case errors.Is(err, tasks.ErrConflict), errors.Is(err, tasks.ErrTooMany):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	return err
}

func toWireTask(t tasks.Task) wire.AgentTask {
	steps := make([]wire.AgentTaskStep, len(t.Steps))
	for i, s := range t.Steps {
		steps[i] = wire.AgentTaskStep{Text: s.Text, Status: s.Status, Note: s.Note}
	}
	return wire.AgentTask{ID: t.ID, SessionID: t.SessionID, Idea: t.Idea, Title: t.Title, Mandate: t.Mandate, State: t.State, Steps: steps,
		Question: t.Question, Outcome: t.Outcome, Turns: apikit.Safe(t.Turns), MaxTurns: apikit.Safe(tasks.MaxTurns),
		CreatedAt: wire.Time{Time: t.CreatedAt}, UpdatedAt: wire.Time{Time: t.UpdatedAt}}
}

func (s *Server) listAgentTasks(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := d.tasks.List(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.AgentTaskList{Tasks: make([]wire.AgentTask, len(list))}
	for i, t := range list {
		out.Tasks[i] = toWireTask(t)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) pauseAgentTask(c *apikit.Call) (apikit.Reply, error) {
	return s.controlAgentTask(c, tasks.ActionPause)
}
func (s *Server) resumeAgentTask(c *apikit.Call) (apikit.Reply, error) {
	return s.controlAgentTask(c, tasks.ActionResume)
}
func (s *Server) stopAgentTask(c *apikit.Call) (apikit.Reply, error) {
	return s.controlAgentTask(c, tasks.ActionStop)
}

// controlAgentTask applies the user's control. Stop also ends the turn the
// task is taking; its runs go on, as when the user stops any turn. Resume lets
// the loop take the task up at once.
func (s *Server) controlAgentTask(c *apikit.Call, action string) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	t, err := d.tasks.Control(c.R.Context(), c.R.PathValue("id"), action)
	if err != nil {
		return apikit.Reply{}, taskError(err)
	}
	switch action {
	case tasks.ActionStop:
		if _, err := d.agent.Cancel(t.SessionID); err != nil && !errors.Is(err, agent.ErrNoTurn) {
			return apikit.Reply{}, err
		}
	case tasks.ActionResume:
		d.agent.WakeTasks()
	}
	return apikit.Reply{Data: toWireTask(t)}, nil
}

// ideaBatch is how many postings or follow-ups one press hands over.
const ideaBatch = 5

// handOver ends every idea's request: the user asked for the outcome, not for
// a call at each step.
const handOver = " Handle it to the end without asking me at each step."

// agentIdea is a Home item the user can hand over to the Ask Agent as a
// delegated task ("Let the agent do it"). open writes the user's request for
// what waits now in the idea's new conversation, or "" when nothing does. It
// holds counts and tracker ids alone, never a posting's or an email's text, so
// the request the guard reads as the user's own is the app's wording, which
// the press makes theirs.
type agentIdea struct {
	title string
	steps []string
	open  func(ctx context.Context, s *Server, d *domain, sessionID string) (string, error)
}

// agentIdeas are the ideas Needs you offers. None of them submits: the apply
// request forbids it in words the submit gate reads.
var agentIdeas = map[string]agentIdea{
	// The press queues the evaluations itself, as the inbox's own button does,
	// because the agent's second evaluation in a turn would wait on a card: the
	// first one's result is outside content. The task then waits for them.
	"evaluate_inbox": {title: "Evaluate the newest postings in the inbox",
		steps: []string{"Wait for the evaluations of the newest postings", "Say which are worth applying to"},
		open: func(ctx context.Context, s *Server, d *domain, sessionID string) (string, error) {
			n, err := s.evaluateNewest(ctx, d, sessionID)
			switch {
			case err != nil || n == 0:
				return "", err
			case n == 1:
				return "Evaluate the newest posting waiting in my pipeline inbox (its evaluation is queued in this conversation) and, when it is done, tell me whether it is worth applying to and why." + handOver, nil
			}
			return fmt.Sprintf("Evaluate the %d newest postings waiting in my pipeline inbox (their evaluations are queued in this conversation) and, when they are done, tell me which are worth applying to and why.", n) + handOver, nil
		}},
	"apply_ready": {title: "Apply to the postings ready on the apply list",
		steps: []string{"Start the applications from the apply list", "Answer what each run stops on from the Answer Bank and profile", "Say which wait for my Submit and which need me"},
		open: func(ctx context.Context, s *Server, _ *domain, _ string) (string, error) {
			lanes, err := s.apply.ReadLanes(ctx, wire.LaneApplyNow, wire.LaneSelective)
			n := len(lanes[wire.LaneApplyNow]) + len(lanes[wire.LaneSelective])
			if err != nil || n == 0 {
				return "", err
			}
			return fmt.Sprintf("Apply to %d postings from my apply list. Fill each application and answer its questions from my Answer Bank and profile, but do not submit any of them: I confirm each Submit myself.", min(n, ideaBatch)) + handOver, nil
		}},
	"draft_followups": {title: "Draft the follow-ups that are due",
		steps: []string{"Draft each follow-up", "Show me the drafts"},
		open: func(ctx context.Context, s *Server, _ *domain, _ string) (string, error) {
			tr, err := s.tracker()
			if err != nil {
				return "", err
			}
			pol, _ := s.cfg.Workspace.FollowupPolicy()
			list, err := tr.Followups(ctx, pol, true)
			if err != nil {
				return "", err
			}
			var ids []string
			for _, sg := range list {
				if (sg.Kind == tracker.KindFollowUp || sg.Kind == tracker.KindReplyCheck) && len(ids) < ideaBatch {
					ids = append(ids, fmt.Sprintf("#%d", sg.ID))
				}
			}
			if len(ids) == 0 {
				return "", nil
			}
			return fmt.Sprintf("Draft the follow-ups that are due on my applications %s, one draft each, and show me the drafts. Do not send anything.", strings.Join(ids, ", ")) + handOver, nil
		}},
}

// evaluateNewest promotes the newest postings the inbox drain would still take
// (pending, not failed twice) to evaluation behind the gate the inbox's own
// button uses, at most ideaBatch of them, and ties each run to the
// conversation, so its end is told there. It counts the queued.
func (s *Server) evaluateNewest(ctx context.Context, d *domain, sessionID string) (int, error) {
	rows, err := d.repo.InboxList(ctx, discovery.InboxQuery{Status: discovery.InboxPending, Newest: true})
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return 0, e
	}
	gate := evaluate.ScannerGate(profile, evaluate.BlockedFromDiscovery(d.repo))
	enq := conversationEnqueuer{Enqueuer: evaluate.PoolEnqueuer{Pool: d.pool, Priority: evaluate.PriorityUser},
		note: func(runID string) { agentActions{s}.noteOrigin(ctx, d, runID, sessionID, "evaluate_job") }}
	n := 0
	for _, row := range rows {
		if n == ideaBatch {
			break
		}
		if !row.Waiting() { // a posting gone from its board fails every time
			continue
		}
		queued, _, err := d.repo.Promote(ctx, enq, gate, row.ID)
		if err != nil {
			if n > 0 { // what was queued goes on; the full queue takes no more
				break
			}
			return 0, err
		}
		n += len(queued)
	}
	if n > 0 {
		s.notify("scans", "runs")
	}
	return n, nil
}

// conversationEnqueuer starts evaluations for a conversation: each run's origin
// is recorded as soon as it has an id, as an evaluation the agent starts.
type conversationEnqueuer struct {
	discovery.Enqueuer
	note func(runID string)
}

func (e conversationEnqueuer) Enqueue(ctx context.Context, r discovery.EvalRequest) (string, error) {
	id, err := e.Enqueuer.Enqueue(ctx, r)
	if err == nil && id != "" {
		e.note(id)
	}
	return id, err
}

// agentModelMissing refuses a press that starts the agent while the agent
// has no model: its turn would end at no_model_route, after the press had
// made a conversation (and a task) for nothing. The words are the turn's.
func agentModelMissing(ctx context.Context, d *domain) error {
	_, err := d.router.AgentRoute(ctx, llm.AgentTask)
	switch k := llm.KindOf(err); {
	case errors.Is(err, llm.ErrAgentOffline):
		return apikit.WithReason(apikit.Unavailable(agent.DetailOfflineModel), wire.ReasonNoModelRoute)
	case k == llm.KindNoRoute || k == llm.KindUnconfigured:
		return apikit.WithReason(apikit.Unavailable(agent.DetailNoModelRoute), wire.ReasonNoModelRoute)
	}
	return nil
}

// startAgentIdea is POST /api/agent/ideas/{idea}, "Let the agent do it": a new
// conversation whose delegated task is the idea's request, which takes its
// first turn by itself. A second press answers the task already open.
func (s *Server) startAgentIdea(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx, name := c.R.Context(), c.R.PathValue("idea")
	idea, ok := agentIdeas[name]
	if !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "there is no such idea")
	}
	if tk, open, err := d.tasks.OpenIdea(ctx, name); err != nil || open {
		return apikit.Reply{Data: wire.AgentIdeaStarted{SessionID: tk.SessionID, TaskID: tk.ID}}, err
	}
	if err := agentModelMissing(ctx, d); err != nil {
		return apikit.Reply{}, err
	}
	// The workspace's task limit is read before the idea does anything, so a
	// press it refuses has queued nothing.
	list, err := d.tasks.List(ctx)
	if err != nil {
		return apikit.Reply{}, err
	}
	busy := 0
	for _, t := range list {
		if t.Open() {
			busy++
		}
	}
	if busy >= tasks.MaxOpen {
		return apikit.Reply{}, taskError(tasks.ErrTooMany)
	}
	sess, err := d.sessions.Create(ctx, chatsessions.Dashboard, idea.title)
	if err != nil {
		return apikit.Reply{}, err
	}
	drop := func() {
		if derr := d.sessions.Delete(ctx, sess.ID); derr != nil {
			s.log.Warn("an idea's conversation was not removed", "session", sess.ID, "error", derr.Error())
		}
	}
	request, err := idea.open(ctx, s, d, sess.ID)
	if err != nil || request == "" {
		drop()
		if err == nil {
			err = apikit.NewErr(wire.CodeConflict, "nothing waits for this now")
		}
		return apikit.Reply{}, err
	}
	steps := make([]tasks.Step, len(idea.steps))
	for i, text := range idea.steps {
		steps[i] = tasks.Step{Text: text}
	}
	runs, err := d.origins.RunsOf(ctx, sess.ID)
	if err != nil {
		drop()
		return apikit.Reply{}, err
	}
	tk, err := d.tasks.Create(ctx, tasks.NewTask{SessionID: sess.ID, Idea: name, Title: idea.title, Mandate: request, Steps: steps, WaitsForRuns: len(runs) > 0})
	if err != nil {
		drop()
		if errors.Is(err, tasks.ErrOpen) { // another press got there first
			if tk, open, oerr := d.tasks.OpenIdea(ctx, name); oerr == nil && open {
				return apikit.Reply{Data: wire.AgentIdeaStarted{SessionID: tk.SessionID, TaskID: tk.ID}}, nil
			}
		}
		return apikit.Reply{}, taskError(err)
	}
	d.agent.WakeTasks()
	s.notifyTopics("runs")
	return apikit.Reply{Status: http.StatusAccepted, Data: wire.AgentIdeaStarted{SessionID: tk.SessionID, TaskID: tk.ID}}, nil
}

// agentTaskConfig lets the delegated tasks go on by themselves: they wait on
// the runs their conversation started and tell the user, through the
// announcement policy, when one they were not watching asks or is done.
func (s *Server) agentTaskConfig(d *domain) *agent.TaskConfig {
	return &agent.TaskConfig{RunsOf: d.origins.RunsOf, Going: d.origins.ActiveIn,
		Announce: func(ctx context.Context, t tasks.Task) {
			title, body := "A task needs you: "+t.Title, t.Question
			if t.State == tasks.Done {
				title, body = "A task is done: "+t.Title, t.Outcome
			}
			ev := notify.Event{Key: fmt.Sprintf("task|%s|%s|%d", t.ID, t.State, t.Turns), Kind: notify.EventTaskUpdate, Title: title, Body: body,
				Link: "/ask?conversation=" + t.SessionID}
			if err := d.notify.policy.Announce(ctx, ev); err != nil {
				s.log.Warn("a task's announcement was not queued", "task", t.ID, "error", err.Error())
			}
		}}
}
