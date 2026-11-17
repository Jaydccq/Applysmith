-- The user tapped Tomorrow on a Gmail deadline reminder: one more reminder at
-- until_ms takes the place of the thread's reminders up to then. No row holds
-- message text.
CREATE TABLE gmail_reminder_snoozes (
    thread_id TEXT PRIMARY KEY,
    until_ms  INTEGER NOT NULL,
    set_at    TEXT NOT NULL
) STRICT;
