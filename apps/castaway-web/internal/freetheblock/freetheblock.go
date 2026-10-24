// Package freetheblock is Free the Block: Klotski (Huarong Dao) on a 4×5 board. The server replays a player's
// move list from Start (Replay), so the score never trusts the client. A move is one block sliding along its
// axis; consecutive slides of the same block in the same direction count as one move.
package freetheblock

import (
	"errors"
	"fmt"
)

const (
	Width, Height = 4, 5
	Pieces        = 10
	MaxSteps      = 4 // the longest slide on this board
)

// Piece ids: 0 is the idol (2×2); 1–4 are 1×2 verticals; 5 is the 2×1 horizontal; 6–9 are 1×1 soldiers.
// The start is the classic 横刀立马 layout: the empty cells sit at the bottom centre.
var (
	shapes = [Pieces]struct {
		code uint64 // key digit: 1 idol, 2 vertical, 3 horizontal, 4 soldier
		w, h int
	}{{1, 2, 2}, {2, 1, 2}, {2, 1, 2}, {2, 1, 2}, {2, 1, 2}, {3, 2, 1}, {4, 1, 1}, {4, 1, 1}, {4, 1, 1}, {4, 1, 1}}
	start = [Pieces][2]int{{1, 0}, {0, 0}, {3, 0}, {0, 2}, {3, 2}, {1, 2}, {1, 3}, {2, 3}, {0, 4}, {3, 4}}
	// The idol must reach the bottom centre (cols 1–2, rows 3–4) and slide out through the bottom opening.
	goal     = [2]int{1, 3}
	dirOrder = []string{"up", "down", "left", "right"}
	dirs     = map[string]struct {
		code   byte
		dx, dy int
	}{"up": {'u', 0, -1}, "down": {'d', 0, 1}, "left": {'l', -1, 0}, "right": {'r', 1, 0}}
)

// Move slides piece Piece Steps cells in direction Dir ("up", "down", "left" or "right"). Consecutive moves of
// the same piece are one move in the count, so the client may send one record per drag or one per cell.
type Move struct {
	Piece int    `json:"piece"`
	Dir   string `json:"dir"`
	Steps int    `json:"steps"`
}

// Result is what a move list does from the start: moves counted, and whether the idol got out.
type Result struct {
	Moves  int
	Solved bool
}

type state [Pieces][2]int

// occupancy marks each cell with the piece id + 1 covering it (0 is empty).
func occupancy(s *state) [Height][Width]int {
	var g [Height][Width]int
	for p, pos := range s {
		for dy := 0; dy < shapes[p].h; dy++ {
			for dx := 0; dx < shapes[p].w; dx++ {
				g[pos[1]+dy][pos[0]+dx] = p + 1
			}
		}
	}
	return g
}

// fits reports whether piece p would cover only empty cells (or its own) at (x, y).
func fits(g *[Height][Width]int, p, x, y int) bool {
	sh := shapes[p]
	if x < 0 || y < 0 || x+sh.w > Width || y+sh.h > Height {
		return false
	}
	for dy := 0; dy < sh.h; dy++ {
		for dx := 0; dx < sh.w; dx++ {
			if c := g[y+dy][x+dx]; c != 0 && c != p+1 {
				return false
			}
		}
	}
	return true
}

// Replay plays moves from the start. It rejects a move that is malformed, blocked, or made
// after the idol is out; otherwise it returns the move count and whether the idol is out.
func Replay(moves []Move) (Result, error) {
	s := state(start)
	var res Result
	var prev Move
	for i, m := range moves {
		n := i + 1
		d, ok := dirs[m.Dir]
		switch {
		case res.Solved:
			return res, fmt.Errorf("move %d: the idol is already out", n)
		case m.Piece < 0 || m.Piece >= Pieces:
			return res, fmt.Errorf("move %d: piece %d does not exist", n, m.Piece)
		case !ok:
			return res, fmt.Errorf("move %d: direction %q is not up, down, left or right", n, m.Dir)
		case m.Steps < 1 || m.Steps > MaxSteps:
			return res, fmt.Errorf("move %d: steps must be 1–%d", n, MaxSteps)
		}
		g := occupancy(&s)
		for k := 1; k <= m.Steps; k++ {
			if !fits(&g, m.Piece, s[m.Piece][0]+k*d.dx, s[m.Piece][1]+k*d.dy) {
				return res, fmt.Errorf("move %d: piece %d is blocked", n, m.Piece)
			}
		}
		s[m.Piece] = [2]int{s[m.Piece][0] + m.Steps*d.dx, s[m.Piece][1] + m.Steps*d.dy}
		if i == 0 || m.Piece != prev.Piece {
			res.Moves++
		}
		prev = m
		res.Solved = s[0] == goal
	}
	return res, nil
}

// Encode packs a move list into 3 characters a move: piece letter (a–j), direction letter (u, d, l, r), steps (1–4).
// Call it only on a list Replay accepted.
func Encode(moves []Move) string {
	b := make([]byte, 0, 3*len(moves))
	for _, m := range moves {
		b = append(b, byte('a'+m.Piece), dirs[m.Dir].code, byte('0'+m.Steps))
	}
	return string(b)
}

// Decode reverses Encode and rejects anything it could not have produced.
func Decode(s string) ([]Move, error) {
	if len(s)%3 != 0 {
		return nil, errors.New("bad move encoding")
	}
	moves := make([]Move, 0, len(s)/3)
	for i := 0; i < len(s); i += 3 {
		m := Move{Piece: int(s[i]) - 'a', Steps: int(s[i+2]) - '0'}
		for name, d := range dirs {
			if d.code == s[i+1] {
				m.Dir = name
			}
		}
		if m.Piece < 0 || m.Piece >= Pieces || m.Dir == "" || m.Steps < 1 || m.Steps > MaxSteps {
			return nil, errors.New("bad move encoding")
		}
		moves = append(moves, m)
	}
	return moves, nil
}

// key identifies a position up to moving same-shaped blocks: base-5 digits of each cell's shape code.
func key(s *state) uint64 {
	var codes [Height][Width]uint64
	for p, pos := range s {
		for dy := 0; dy < shapes[p].h; dy++ {
			for dx := 0; dx < shapes[p].w; dx++ {
				codes[pos[1]+dy][pos[0]+dx] = shapes[p].code
			}
		}
	}
	var k uint64
	for _, row := range codes {
		for _, c := range row {
			k = k*5 + c
		}
	}
	return k
}

// Solve finds a fewest-moves solution from the start. An edge is one block moving anywhere it can reach by
// itself (a flood fill with the other blocks fixed), so the path length is the optimal move count.
func Solve() ([]Move, error) {
	type origin struct {
		parent uint64
		steps  []Move // the unit slides that make this move
	}
	s0 := state(start)
	k0 := key(&s0)
	from := map[uint64]origin{k0: {}}
	queue := []state{s0}
	for head := 0; head < len(queue); head++ {
		cur := queue[head]
		ck := key(&cur)
		if cur[0] == goal {
			var moves []Move
			for k := ck; k != k0; k = from[k].parent {
				moves = append(append([]Move(nil), from[k].steps...), moves...)
			}
			return moves, nil
		}
		g := occupancy(&cur)
		for p := 0; p < Pieces; p++ {
			for _, end := range reach(&g, p, cur[p]) {
				next := cur
				next[p] = end.pos
				if _, ok := from[key(&next)]; !ok {
					nk := key(&next)
					from[nk] = origin{parent: ck, steps: end.steps}
					queue = append(queue, next)
				}
			}
		}
	}
	return nil, errors.New("no solution")
}

type reached struct {
	pos   [2]int
	steps []Move
}

// reach lists, in discovery order, every cell block p can reach by itself (others fixed), except its start.
func reach(g *[Height][Width]int, p int, start [2]int) []reached {
	parent := map[[2]int]struct {
		from [2]int
		move Move
	}{start: {}}
	order := [][2]int{start}
	for head := 0; head < len(order); head++ {
		at := order[head]
		for _, name := range dirOrder {
			d := dirs[name]
			next := [2]int{at[0] + d.dx, at[1] + d.dy}
			if _, seen := parent[next]; seen || !fits(g, p, next[0], next[1]) {
				continue
			}
			parent[next] = struct {
				from [2]int
				move Move
			}{at, Move{Piece: p, Dir: name, Steps: 1}}
			order = append(order, next)
		}
	}
	var out []reached
	for _, pos := range order[1:] {
		var steps []Move
		for at := pos; at != start; at = parent[at].from {
			steps = append([]Move{parent[at].move}, steps...)
		}
		out = append(out, reached{pos: pos, steps: steps})
	}
	return out
}
