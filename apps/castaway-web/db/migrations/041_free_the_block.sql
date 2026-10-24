-- Free the Block: slide the blocks to free the idol (Klotski). One scored run per player. The page saves the
-- whole move list after each move, encoded as 3 characters per slide (piece, direction, steps; see
-- internal/freetheblock). The server replays it from the start, so it alone counts moves and decides solved.
CREATE TABLE free_the_block_games (
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

CREATE TABLE free_the_block_plays (
    game_id BIGINT NOT NULL REFERENCES free_the_block_games(id) ON DELETE CASCADE,
    participant_id BIGINT NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    moves TEXT NOT NULL DEFAULT '',
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (game_id, participant_id)
);
