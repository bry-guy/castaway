package gameplay

import "fmt"

// ChampionChallengeActivityType records castaways winning individual (post-merge) immunity or reward. The
// occurrence's participants are the players whose locked Champion won, each with {"contestant_name": ...}.
const ChampionChallengeActivityType = "champion_challenge"

// ChampionChallengePoints is the per-picker award for each kind.
var ChampionChallengePoints = map[string]int32{"immunity": 3, "reward": 2}

type championParticipantMetadata struct {
	ContestantID   string `json:"contestant_id"`
	ContestantName string `json:"contestant_name"`
}

func (s *Service) resolveChampionChallenge(resolverCtx resolverContext) ([]resolvedLedgerEntry, error) {
	kind := resolverCtx.occurrence.OccurrenceType
	points, ok := ChampionChallengePoints[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported champion challenge kind %q", kind)
	}
	entries := make([]resolvedLedgerEntry, 0, len(resolverCtx.occurrenceParticipants))
	for _, row := range resolverCtx.occurrenceParticipants {
		var m championParticipantMetadata
		if err := parseJSON(row.Metadata, &m); err != nil || m.ContestantID == "" {
			return nil, fmt.Errorf("champion challenge participant %q needs contestant metadata", row.ParticipantName)
		}
		entries = append(entries, resolvedLedgerEntry{
			ParticipantID: row.ParticipantID,
			EntryKind:     bonusEntryKindAward,
			Points:        points,
			Visibility:    bonusVisibilityPublic,
			Reason:        fmt.Sprintf("Your Champion %s won %s", m.ContestantName, kind),
			AwardKey:      "champion_challenge:" + m.ContestantID,
		})
	}
	return entries, nil
}
