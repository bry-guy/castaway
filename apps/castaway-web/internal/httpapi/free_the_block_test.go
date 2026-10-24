package httpapi

import (
	"fmt"
	"testing"
)

// Ranking: solvers by fewest moves, ties share the place (+2 for first, +1 for second, +1 for solving); the rest 0.
func TestFreeTheBlockPoints(t *testing.T) {
	places, points := ftbPoints([]ftbPlayer{
		{ID: "a", Moves: 81, Solved: true}, {ID: "b", Moves: 90, Solved: true}, {ID: "c", Moves: 81, Solved: true}, {ID: "d", Moves: 70},
	})
	if got := fmt.Sprint(places, points); got != "map[a:1 b:3 c:1 d:0] map[a:3 b:1 c:3 d:0]" {
		t.Errorf("tie for first: places and points %s", got)
	}
	places, points = ftbPoints([]ftbPlayer{{ID: "x", Moves: 80, Solved: true}, {ID: "y", Moves: 81, Solved: true}, {ID: "z", Moves: 82, Solved: true}})
	if got := fmt.Sprint(places, points); got != "map[x:1 y:2 z:3] map[x:3 y:2 z:1]" {
		t.Errorf("second place: places and points %s", got)
	}
}
