-- Discord users who may view a season's site (scores, seasons, player pages) without being a player in it.
CREATE TABLE instance_viewers (
    instance_id BIGINT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    discord_user_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (instance_id, discord_user_id)
);
