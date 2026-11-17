package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// chatStreams keeps the event stream of each conversation that has had a turn
// lately, so a client that reconnects with its last event id is caught up.
// Each stream is looked at once every retention period for as long as it is
// kept, and dropped when nobody is subscribed and nothing was published or
// opened for that period.
type chatStreams struct {
	mu        sync.Mutex
	epoch     string
	now       func() time.Time
	retention time.Duration
	items     map[string]*chatStream
}

type chatStream struct {
	es   *eventStream
	last time.Time
}

func newChatStreams(epoch string, now func() time.Time) *chatStreams {
	return &chatStreams{epoch: epoch, now: now, retention: runRetention, items: map[string]*chatStream{}}
}

func (c *chatStreams) stream(id string) *eventStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	cs := c.items[id]
	if cs == nil {
		cs = &chatStream{es: newEventStream(c.epoch)}
		c.items[id] = cs
		c.watch(id, cs)
	}
	cs.last = c.now()
	return cs.es
}

// Publish implements agent.Sink: every event of a turn goes to the stream of
// its conversation under the one event name "chat".
func (c *chatStreams) Publish(sessionID string, ev wire.ChatEvent) {
	c.stream(sessionID).publish("chat", ev)
}

// watch looks at a kept stream after each retention period until it is gone.
func (c *chatStreams) watch(id string, cs *chatStream) {
	time.AfterFunc(c.retention, func() {
		if !c.retire(id, cs) {
			c.watch(id, cs)
		}
	})
}

// retire drops the stream when it is idle and reports whether it is gone
// from its conversation, dropped now or replaced before.
func (c *chatStreams) retire(id string, cs *chatStream) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items[id] != cs {
		return true
	}
	if c.now().Sub(cs.last) < c.retention {
		return false
	}
	cs.es.mu.Lock()
	idle := len(cs.es.subs) == 0
	cs.es.mu.Unlock()
	if idle {
		delete(c.items, id)
	}
	return idle
}

// turnStart is the stream id just before the turn.start event of a turn, or
// false when the stream no longer holds it.
func (c *chatStreams) turnStart(id, turnID string) (int64, bool) {
	c.mu.Lock()
	cs := c.items[id]
	c.mu.Unlock()
	if cs == nil {
		return 0, false
	}
	cs.es.mu.Lock()
	defer cs.es.mu.Unlock()
	for _, ev := range cs.es.buf {
		var e wire.ChatEvent
		if json.Unmarshal(ev.data, &e) == nil && e.TurnID == turnID && e.Type == wire.ChatTurnStart {
			return ev.id - 1, true
		}
	}
	return 0, false
}

func (c *chatStreams) drop(id string) {
	c.mu.Lock()
	delete(c.items, id)
	c.mu.Unlock()
}

func toChatSession(x chatsessions.Session) wire.ChatSession {
	return wire.ChatSession{ID: x.ID, Title: x.Title, Channel: string(x.Channel), ParentID: x.ParentID, ParentMessageID: apikit.Safe64(x.ParentMessageID),
		CreatedAt: wire.Time{Time: x.CreatedAt}, UpdatedAt: wire.Time{Time: x.UpdatedAt}}
}

func toChatMessage(m chatsessions.Message) wire.ChatMessage {
	var cites []wire.ChatCitation
	for _, c := range m.Envelope.Citations {
		cites = append(cites, wire.ChatCitation{Label: c.Label, Snippet: c.Snippet, URL: c.URL})
	}
	return wire.ChatMessage{ID: apikit.Safe64(m.ID), TurnID: m.TurnID, Role: string(m.Role), Text: m.Body, ToolName: m.ToolName, Class: agent.ToolClass(m.ToolName), ToolCallID: m.ToolCallID,
		OK: m.OK, SystemMade: m.SystemMade, Citations: cites, Limitations: m.Envelope.Limitations, NextAction: m.Envelope.NextAction, RunID: m.Envelope.RunID, CreatedAt: wire.Time{Time: m.CreatedAt}}
}

func toChatTurn(t chatsessions.Turn) wire.ChatTurn {
	out := wire.ChatTurn{ID: t.ID, Channel: string(t.Channel), Origin: t.Origin, RuleID: t.RuleID, Tier: t.Tier, Status: t.Status, Reason: t.Reason, Detail: t.Detail,
		StartedAt: wire.Time{Time: t.StartedAt}}
	if t.FinishedAt != nil {
		out.FinishedAt = &wire.Time{Time: *t.FinishedAt}
	}
	return out
}

// chatError maps the service's failures onto the API errors.
func chatError(err error) error {
	switch {
	case errors.Is(err, agent.ErrMessageTooLong):
		return apikit.InvalidField("text", "%s", err.Error())
	case errors.Is(err, agent.ErrEmptyMessage):
		return apikit.InvalidField("text", "%s", err.Error())
	case errors.Is(err, agent.ErrTurnRunning), errors.Is(err, agent.ErrNoTurn), errors.Is(err, agent.ErrSteerFull):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such conversation or message")
	}
	return err
}

func (s *Server) chatAgent() (*agent.Agent, *chatsessions.Store, error) {
	d, err := s.dom()
	if err != nil {
		return nil, nil, err
	}
	return d.agent, d.sessions, nil
}

// maxChatSearch bounds the search text of the conversation list.
const maxChatSearch = 200

func (s *Server) listChatSessions(c *apikit.Call) (apikit.Reply, error) {
	_, ss, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	limit, offset := q.Integer("limit", 50), q.Integer("offset", 0)
	ch := chatsessions.Channel(q.Str("channel"))
	search := strings.TrimSpace(q.Str("q"))
	switch {
	case q.Err != nil:
		return apikit.Reply{}, q.Err
	case limit < 1 || limit > 200 || offset < 0:
		return apikit.Reply{}, apikit.InvalidField("limit", "limit must be between 1 and 200 and offset 0 or more")
	case ch != "" && !ch.Valid():
		return apikit.Reply{}, apikit.InvalidField("channel", "channel must be dashboard, telegram, scheduled or digest")
	case utf8.RuneCountInString(search) > maxChatSearch:
		return apikit.Reply{}, apikit.InvalidField("q", "q has at most %d characters", maxChatSearch)
	}
	list, total, err := ss.List(c.R.Context(), chatsessions.ListQuery{Channel: ch, Search: search, Limit: limit, Offset: offset})
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.ChatSessionPage{Sessions: make([]wire.ChatSession, len(list)), Total: apikit.Safe(total)}
	for i, x := range list {
		out.Sessions[i] = toChatSession(x)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) getChatSession(c *apikit.Call) (apikit.Reply, error) {
	a, ss, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	sess, msgs, turns, err := ss.Detail(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, chatError(err)
	}
	out := wire.ChatSessionDetail{Session: toChatSession(sess), Messages: make([]wire.ChatMessage, len(msgs)), Turns: make([]wire.ChatTurn, len(turns))}
	for i, m := range msgs {
		out.Messages[i] = toChatMessage(m)
	}
	for i, t := range turns {
		out.Turns[i] = toChatTurn(t)
	}
	out.ActiveTurnID, _ = a.ActiveTurn(id)
	return apikit.Reply{Data: out}, nil
}

func (s *Server) renameChatSession(c *apikit.Call) (apikit.Reply, error) {
	_, ss, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.ChatRename
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || utf8.RuneCountInString(title) > chatsessions.MaxTitle {
		return apikit.Reply{}, apikit.InvalidField("title", "a title has 1 to %d characters", chatsessions.MaxTitle)
	}
	sess, err := ss.Rename(c.R.Context(), c.R.PathValue("id"), title)
	if err != nil {
		return apikit.Reply{}, chatError(err)
	}
	return apikit.Reply{Data: toChatSession(sess)}, nil
}

func (s *Server) deleteChatSession(c *apikit.Call) (apikit.Reply, error) {
	a, ss, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if _, running := a.ActiveTurn(id); running {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a turn is running in this conversation; cancel it first")
	}
	if err := ss.Delete(c.R.Context(), id); err != nil {
		return apikit.Reply{}, chatError(err)
	}
	s.chat.drop(id)
	return apikit.Reply{Data: wire.ChatDeleted{ID: id}}, nil
}

// newSessionID in the path starts a conversation with the posted message.
const newSessionID = "new"

func (s *Server) sendChatMessage(c *apikit.Call) (apikit.Reply, error) {
	a, _, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.ChatSend
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	id := c.R.PathValue("id")
	if id == newSessionID {
		id = ""
	}
	// The dashboard session gets every tool class; a remote device gets read
	// and draft; the local token never could send a message.
	if c.P.Kind != apikit.PrincipalDashboard && c.P.Kind != apikit.PrincipalRemoteDevice {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "this route accepts the dashboard session only"), wire.ReasonSessionOnly)
	}
	remote := c.P.Kind == apikit.PrincipalRemoteDevice
	if remote && id != "" && !s.remoteOwns(c, id) {
		return apikit.Reply{}, chatError(store.ErrNotFound)
	}
	res, err := a.Send(c.R.Context(), agent.SendRequest{SessionID: id, Channel: agent.ChannelDashboard, Text: req.Text, Remote: remote})
	if err != nil {
		return apikit.Reply{}, chatError(err)
	}
	if remote && id == "" {
		if err := s.remoteOwn(c, res.SessionID); err != nil {
			return apikit.Reply{}, err
		}
	}
	s.notify("runs")
	return apikit.Reply{Status: http.StatusAccepted, Location: fmt.Sprintf("/api/chat/sessions/%s/events", res.SessionID),
		Data: wire.ChatTurnAccepted{SessionID: res.SessionID, TurnID: res.TurnID}}, nil
}

func (s *Server) chatEvents(c *apikit.Call) (apikit.Reply, error) {
	a, ss, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if c.P.Kind == apikit.PrincipalRemoteDevice && !s.remoteOwns(c, id) {
		return apikit.Reply{}, chatError(store.ErrNotFound) // a remote device sees only what it started
	}
	if _, err := ss.Get(c.R.Context(), id); err != nil {
		return apikit.Reply{}, chatError(err)
	}
	active, running := a.ActiveTurn(id)
	// A client that posts a message and then opens the stream missed the first
	// events of the turn; a connection with no cursor starts at the running
	// turn's first event, so it sees the whole turn.
	if running && c.R.Header.Get("Last-Event-ID") == "" {
		if before, ok := s.chat.turnStart(id, active); ok {
			c.R.Header.Set("Last-Event-ID", fmt.Sprintf("%s-%d", s.epoch, before))
		}
	}
	preamble := func() ([]sseEvent, bool) {
		return []sseEvent{newEvent("chat", wire.ChatEvent{Type: wire.ChatState, ActiveTurnID: active})}, false
	}
	return apikit.Reply{}, s.serveStream(c, s.chat.stream(id), preamble)
}

func (s *Server) steerChat(c *apikit.Call) (apikit.Reply, error) {
	a, _, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.ChatSteer
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	turn, queued, err := a.Steer(c.R.PathValue("id"), req.Text)
	if err != nil {
		return apikit.Reply{}, chatError(err)
	}
	return apikit.Reply{Status: http.StatusAccepted, Data: wire.ChatSteerAccepted{TurnID: turn, Queued: apikit.Safe(queued)}}, nil
}

func (s *Server) cancelChat(c *apikit.Call) (apikit.Reply, error) {
	a, _, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := a.Cancel(c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, chatError(err)
	}
	note := "The turn was cancelled."
	if len(res.Runs) > 0 {
		note = "The turn was cancelled. Runs it had already started continue on the runs board: " + strings.Join(res.Runs, ", ") + "."
	}
	runs := res.Runs
	if runs == nil {
		runs = []string{}
	}
	return apikit.Reply{Status: http.StatusAccepted, Data: wire.ChatCancelResult{TurnID: res.TurnID, Runs: runs, Note: note}}, nil
}

func (s *Server) forkChat(c *apikit.Call) (apikit.Reply, error) {
	_, ss, err := s.chatAgent()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.ChatFork
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.MessageID < 1 {
		return apikit.Reply{}, apikit.InvalidField("messageId", "messageId must be a positive whole number")
	}
	sess, err := ss.Fork(c.R.Context(), c.R.PathValue("id"), int64(req.MessageID))
	if err != nil {
		return apikit.Reply{}, chatError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Location: "/api/chat/sessions/" + sess.ID, Data: toChatSession(sess)}, nil
}
