-- An episode's public discussion thread: probst queues it for the episode's air time; the bot opens the thread
-- and posts one line in the season channel. A row is sent at most once: a claim that never finishes fails, since
-- it may already have opened a thread. A failed row with no thread can be queued again.
CREATE TABLE discussion_threads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id BIGINT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    guild_id TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    due_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    claimed_at TIMESTAMPTZ,
    thread_id TEXT,
    line_message_id TEXT,
    failure TEXT,
    UNIQUE (instance_id, request_key)
);
