package freetheblock

import (
	"reflect"
	"testing"
)

// The fewest-moves count for the 横刀立马 layout is 81; the BFS path must replay to that exactly.
func TestSolveIsOptimal(t *testing.T) {
	moves, err := Solve()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Replay(moves)
	if err != nil {
		t.Fatalf("solution does not replay: %v", err)
	}
	if !res.Solved || res.Moves != 81 {
		t.Fatalf("solve: %+v over %d records, want solved in 81 moves", res, len(moves))
	}
}

func TestReplayRules(t *testing.T) {
	cases := map[string][]Move{
		"soldier up into the horizontal": {{Piece: 6, Dir: "up", Steps: 1}},
		"vertical sideways":              {{Piece: 1, Dir: "right", Steps: 1}},
		"horizontal up":                  {{Piece: 5, Dir: "up", Steps: 1}},
		"slide past an occupied cell":    {{Piece: 0, Dir: "down", Steps: 3}},
		"unknown direction":              {{Piece: 6, Dir: "north", Steps: 1}},
		"zero steps":                     {{Piece: 9, Dir: "left", Steps: 0}},
		"piece out of range":             {{Piece: 10, Dir: "left", Steps: 1}},
		"off the board":                  {{Piece: 1, Dir: "up", Steps: 1}},
	}
	for name, moves := range cases {
		if _, err := Replay(moves); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Consecutive slides of one block are one move, whatever their direction; another block starts a new one.
	res, err := Replay([]Move{{Piece: 9, Dir: "left", Steps: 1}, {Piece: 9, Dir: "left", Steps: 1}, {Piece: 9, Dir: "right", Steps: 2}, {Piece: 6, Dir: "down", Steps: 1}})
	if err != nil || res.Moves != 2 || res.Solved {
		t.Fatalf("runs: %+v, %v", res, err)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	moves := []Move{{Piece: 0, Dir: "down", Steps: 4}, {Piece: 9, Dir: "left", Steps: 1}}
	got, err := Decode(Encode(moves))
	if err != nil || !reflect.DeepEqual(got, moves) {
		t.Fatalf("round trip: %v, %v", got, err)
	}
	for _, bad := range []string{"a", "ax1", "z" + "u1", "au9", "au0"} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("Decode(%q) accepted", bad)
		}
	}
}
