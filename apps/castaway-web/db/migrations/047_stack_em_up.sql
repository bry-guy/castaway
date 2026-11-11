-- Stack 'Em Up: hold a stick with a tray on top and stack eight letter blocks until the tower spells SURVIVOR,
-- then hold it for three seconds. Each player gets one timed run per game; the page saves the stack's height and
-- the drop count (towers that fell) at "Go", after every block and every drop, so the server keeps the clock:
-- from the first save to the save that holds the full tower. A reload drops the tower and the clock keeps going.
CREATE TABLE stack_games (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    instance_id BIGINT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    episode_number INTEGER NOT NULL,
    opens_at TIMESTAMPTZ NOT NULL,
    cutoff_at TIMESTAMPTZ NOT NULL CHECK (cutoff_at > opens_at),
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (instance_id, episode_number)
);

CREATE TABLE stack_plays (
    game_id BIGINT NOT NULL REFERENCES stack_games(id) ON DELETE CASCADE,
    participant_id BIGINT NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    height INTEGER NOT NULL DEFAULT 0 CHECK (height BETWEEN 0 AND 8), -- blocks on the tower now
    best_height INTEGER NOT NULL DEFAULT 0 CHECK (best_height BETWEEN 0 AND 8),
    drops INTEGER NOT NULL DEFAULT 0 CHECK (drops >= 0),
    finished_at TIMESTAMPTZ CHECK (finished_at IS NULL OR height = 8),
    PRIMARY KEY (game_id, participant_id)
);
