-- The Home item a delegated task was handed over from ("Let the agent do it":
-- the inbox to evaluate, the apply list, the follow-ups due), empty for a task
-- the user delegated in a conversation. An idea has at most one open task, so a
-- second press finds the first.
ALTER TABLE agent_tasks ADD COLUMN idea TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX agent_tasks_open_idea ON agent_tasks (idea) WHERE idea <> '' AND state NOT IN ('done', 'stopped');
