package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// profileEvent is one season event in a player's history (a tribe assignment, their Champion pick) with the
// points it earned them by week.
type profileEvent struct {
	Name, URL string
	Week      int // the week it started
	Weeks     []eventWeek
}

type eventWeek struct{ Week, Points int }

// profileEvents lists a player's tribe assignments and Champion pick, oldest first, each with its tribe or
// Champion challenge points per week (an entry's week is the latest episode aired by its time).
func (s *Server) profileEvents(ctx context.Context, participantID string) ([]profileEvent, error) {
	type span struct {
		event      profileEvent
		start, end time.Time
		kind       string
	}
	week := `GREATEST(1, (SELECT COUNT(*) FROM instance_episodes e WHERE e.instance_id = p.instance_id AND e.airs_at <= %s))::int`
	rows, err := s.pool.Query(ctx, `SELECT 'Tribe '||g.name, '', `+fmt.Sprintf(week, "m.starts_at")+`, m.starts_at, COALESCE(m.ends_at, '9999-01-01'), 'tribe_challenge'
		FROM participant_group_membership_periods m JOIN participant_groups g ON g.id = m.participant_group_id JOIN participants p ON p.id = m.participant_id
		WHERE p.public_id = $1 AND g.kind = 'tribe'
		UNION ALL
		SELECT 'Champion: '||c.name, '/games/champion', `+fmt.Sprintf(week, "r.locks_at")+`, r.locks_at, '9999-01-01'::timestamptz, 'champion_challenge'
		FROM champion_picks cp JOIN champion_rounds r ON r.instance_id = cp.instance_id JOIN contestants c ON c.id = cp.contestant_id JOIN participants p ON p.id = cp.participant_id
		WHERE p.public_id = $1
		ORDER BY 4`, participantID)
	if err != nil {
		return nil, err
	}
	spans, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (span, error) {
		var sp span
		return sp, row.Scan(&sp.event.Name, &sp.event.URL, &sp.event.Week, &sp.start, &sp.end, &sp.kind)
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT a.activity_type, o.effective_at, `+fmt.Sprintf(week, "o.effective_at")+`, SUM(l.points)::int
		FROM bonus_point_ledger_entries l JOIN participants p ON p.id = l.participant_id
		JOIN activity_occurrences o ON o.id = l.activity_occurrence_id JOIN instance_activities a ON a.id = o.activity_id
		WHERE p.public_id = $1 AND l.visibility <> 'secret' AND a.activity_type IN ('tribe_challenge', 'champion_challenge')
		GROUP BY 1, 2, 3, p.instance_id ORDER BY 2`, participantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var at time.Time
		var w, pts int
		if err := rows.Scan(&kind, &at, &w, &pts); err != nil {
			return nil, err
		}
		for i := range spans {
			sp := &spans[i]
			if sp.kind != kind || at.Before(sp.start) || !at.Before(sp.end) {
				continue
			}
			if n := len(sp.event.Weeks); n > 0 && sp.event.Weeks[n-1].Week == w {
				sp.event.Weeks[n-1].Points += pts
			} else {
				sp.event.Weeks = append(sp.event.Weeks, eventWeek{w, pts})
			}
			break
		}
	}
	events := make([]profileEvent, len(spans))
	for i, sp := range spans {
		events[i] = sp.event
	}
	return events, rows.Err()
}
