package httpapi

import (
	"fmt"
	"testing"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/freetheblock"
)

// Ranking: solvers by fewest moves, ties share the place (+3 for first, +2 for second, +1 for solving, +5 for the optimum); the rest 0.
func TestFreeTheBlockPoints(t *testing.T) {
	places, points := ftbPoints([]ftbPlayer{
		{ID: "a", Moves: 81, Solved: true}, {ID: "b", Moves: 90, Solved: true}, {ID: "c", Moves: 81, Solved: true}, {ID: "d", Moves: 70},
	})
	if got := fmt.Sprint(places, points); got != "map[a:1 b:3 c:1 d:0] map[a:4 b:1 c:4 d:0]" {
		t.Errorf("tie for first: places and points %s", got)
	}
	places, points = ftbPoints([]ftbPlayer{{ID: "x", Moves: 80, Solved: true}, {ID: "y", Moves: 81, Solved: true}, {ID: "z", Moves: 82, Solved: true}})
	if got := fmt.Sprint(places, points); got != "map[x:1 y:2 z:3] map[x:4 y:3 z:1]" {
		t.Errorf("second place: places and points %s", got)
	}
	places, points = ftbPoints([]ftbPlayer{{ID: "p", Moves: freetheblock.Optimum, Solved: true}, {ID: "q", Moves: 26, Solved: true}, {ID: "r", Moves: freetheblock.Optimum}})
	if got := fmt.Sprint(places, points); got != "map[p:1 q:2 r:0] map[p:9 q:3 r:0]" {
		t.Errorf("optimum: places and points %s", got)
	}
}
