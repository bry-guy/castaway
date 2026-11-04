package httpapi

import (
	"fmt"
	"testing"
	"time"
)

// Ranking: finishers by time in tenths of a second, ties share the place (dense). Finishing +1, fastest +3,
// second +2, and +1 per ace for everyone, finished or not.
func TestTiltPoints(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	places, points := tiltPoints([]tiltPlayer{
		{ID: "a", Finished: true, Time: s(41.23), Aces: 1}, {ID: "b", Finished: true, Time: s(41.27)}, // same tenth: a tie
		{ID: "c", Finished: true, Time: s(50), Aces: 3}, {ID: "d", Finished: true, Time: s(60)},
		{ID: "e", Holes: 2, Aces: 2}, {ID: "f"},
	})
	if got := fmt.Sprint(places, points); got != "map[a:1 b:1 c:2 d:3 e:0 f:0] map[a:5 b:4 c:6 d:1 e:2 f:0]" {
		t.Errorf("places and points %s", got)
	}
	if got := tiltClock(s(83.46)); got != "1:23.4" {
		t.Errorf("clock %s", got)
	}
}
