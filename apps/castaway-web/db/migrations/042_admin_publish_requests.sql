-- An instance admin's free-form DM to the bot is a request to post its text to the admin's season channels.
-- The admin names the servers in a reply ("Podracing/BrainLand"); the chosen posts then queue as announcements.
-- A request is open until it's decided, and expires a day after it was made.
CREATE TABLE admin_publish_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_discord_user_id TEXT NOT NULL,
    request_key TEXT NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'posted', 'cancelled')),
    created_at TIMESTAMPTZ NOT NULL,
    decided_at TIMESTAMPTZ,
    UNIQUE (admin_discord_user_id, request_key)
);
