-- Every Champion pick with its time, so a challenge scores the pick a player held when it aired (picks stay open
-- all season; a pick replaces only one that was voted out). Starts from the current picks.
CREATE TABLE champion_pick_log (
    instance_id BIGINT NOT NULL REFERENCES champion_rounds(instance_id) ON DELETE CASCADE,
    participant_id BIGINT NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    contestant_id BIGINT NOT NULL REFERENCES contestants(id) ON DELETE CASCADE,
    picked_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (instance_id, participant_id, picked_at)
);

INSERT INTO champion_pick_log (instance_id, participant_id, contestant_id, picked_at)
SELECT instance_id, participant_id, contestant_id, picked_at FROM champion_picks;
