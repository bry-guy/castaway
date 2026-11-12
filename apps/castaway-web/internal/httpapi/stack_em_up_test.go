package httpapi

import (
	"fmt"
	"testing"
	"time"
)

// Finishers rank by time in tenths (ties share, dense). Finishing +1, fastest +3, second +2 (drops don't matter);
// an unfinished tower earns nothing, however tall.
func TestStackPoints(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	places, points := stackPoints([]stackPlayer{
		{ID: "a", Finished: true, Time: s(41.23), Drops: 1}, {ID: "b", Finished: true, Time: s(41.27)}, // same tenth: a tie
		{ID: "c", Finished: true, Time: s(50)}, {ID: "d", Finished: true, Time: s(60), Drops: 3},
		{ID: "e", Best: 7}, {ID: "f"},
	})
	if got := fmt.Sprint(places, points); got != "map[a:1 b:1 c:2 d:3 e:0 f:0] map[a:4 b:4 c:3 d:1 e:0 f:0]" {
		t.Errorf("places and points %s", got)
	}
}
