-- Tilt Table: tilt the phone to roll three balls, one at a time, into three holes. Each player gets one timed run
-- per game; the page saves which holes are filled and how many rollbacks (falls off the table) after every sink and
-- every fall, so a reload resumes the run. The clock is the server's: from the first save (at "Go") to the third sink.
-- An ace is a ball sunk with no rollback since the previous sink; the server counts them from sink_rollbacks.
CREATE TABLE tilt_games (
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

CREATE TABLE tilt_plays (
    game_id BIGINT NOT NULL REFERENCES tilt_games(id) ON DELETE CASCADE,
    participant_id BIGINT NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    holes_mask INTEGER NOT NULL DEFAULT 0 CHECK (holes_mask BETWEEN 0 AND 7), -- bit per hole: red, yellow, blue
    rollbacks INTEGER NOT NULL DEFAULT 0 CHECK (rollbacks >= 0),
    aces INTEGER NOT NULL DEFAULT 0 CHECK (aces BETWEEN 0 AND 3),
    sink_rollbacks INTEGER NOT NULL DEFAULT 0, -- rollbacks when the last ball sank
    finished_at TIMESTAMPTZ CHECK (finished_at IS NULL OR holes_mask = 7),
    PRIMARY KEY (game_id, participant_id)
);
