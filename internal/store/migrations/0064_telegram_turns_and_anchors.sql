-- An Ask Agent turn a Telegram message started, kept until its answer is in
-- the chat: the poller hands the turn over and goes on, a follower sends the
-- answer page by page (sent counts the pages Telegram accepted), and a start
-- resumes the followers of turns not yet done. update_id makes a retried
-- update find its turn instead of asking again.
CREATE TABLE telegram_turns (
    turn_id    TEXT PRIMARY KEY CHECK (turn_id <> ''),
    update_id  INTEGER NOT NULL UNIQUE,
    chat_id    INTEGER NOT NULL,
    session_id TEXT NOT NULL CHECK (session_id <> ''),
    reply_to   INTEGER NOT NULL DEFAULT 0,
    sent       INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    done_at    TEXT
) STRICT;

CREATE INDEX telegram_turns_open ON telegram_turns (chat_id, created_at) WHERE done_at IS NULL;

-- What a message the bot sent belongs to, so a reply to it reaches the right
-- place: the Ask Agent conversation it continues, and the Gmail thread it is
-- about. Only messages with one of them are kept.
CREATE TABLE telegram_anchors (
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    thread     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (chat_id, message_id),
    CHECK (session_id <> '' OR thread <> '')
) STRICT;
