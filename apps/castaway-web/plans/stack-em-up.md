# Stack 'Em Up (weekly game)

Status: in-progress

## Goal

A phone-only, two-thumb Survivor stacking challenge, Season 51 week 8, scored like Tilt Table. One thumb holds a
bamboo stick with a tray on top; the other stacks letter blocks until the tower spells SURVIVOR.

## Rules

- Side view, phone sideways. The tray rests on two balls in a cup on top of the stick. The left thumb (anywhere
  left of the table) holds the stick; sliding it moves the balls under the tray (half the thumb's travel, at most
  13 cm). The stick follows the thumb with a little lag (critically damped, ω = 14/s).
- The tray rocks as if on a 22 cm rocker: a short stack rights itself; once the stack's centre of mass is above
  that curve (around block 5–6) it tips unless the balls are kept under it. Gravity is slowed (4.5 m/s²) so an
  unattended full tower takes about 1.4 s to fall, enough to catch.
- The right thumb drags the next block from the table (blocks are laid out in placing order, R O V I V R U S, so
  the tower reads SURVIVOR from the top) and lets go over the stack. It drops straight down onto the top block,
  keeping its position along the tray. A block whose middle is past the edge of the block below falls off and
  goes back to the table; a block that leaves the blocks above some lower block overhanging topples the tower.
- The tower falls (a drop) if the stick is let go, the tray tips past 28°, or it rolls off the balls. Every
  block goes back to the table.
- Eight blocks, then hold for 3 seconds to finish.
- Practice until Ready, or three minutes at most: six blocks that spell ISLAND. Then a 3-2-1
  countdown and the timed run.
- Phones only: coarse pointer and multi-touch. `?debug` allows a desktop (Shift holds the stick, arrows slide
  it, the mouse stacks).

## Scoring

- Finishing +1, fastest +3, second fastest +2 (ties in tenths share, dense), +2 for a clean run (no drops).
- The server keeps the clock: first save (at "Go") to the save with height 8. A reload drops the tower (counted
  as a drop when it had blocks) and the clock keeps running. Shortcut, as with Tilt Table: the physics runs on the
  phone, so a determined player could fake a run.

## Data and API

- Migration `047_stack_em_up.sql`: `stack_games`, `stack_plays` (height, best height, drops, finished).
- Page `GET /games/stack-em-up[/:gameID]`; free play `?practice=1` once any game has closed, always for admins.
- `POST /games/stack-em-up/:gameID/progress` (same-origin) `{height, drops}`: drops only go up; without a new drop
  the height only grows; height 8 finishes and locks the run.
- Admin: `POST /instances/:instanceID/stack-games`, `POST /stack-games/:gameID/resolve`; cutoff resolution runs
  with the other games. Game results type `stack_em_up`; /games hub, scheduled games, profile.
- Probst: `season apply` type `stack_em_up`, `games/stack_em_up.md`, next-game 🪵; `seasons/51.yaml` week 8.

## Verification

- Go unit test `TestStackPoints`; integration test `TestStackEmUp` (refusals, resume attributes, server clock,
  tie in tenths, resolve once, results, ledger, profile, scheduled games). Probst render test.
- Physics self-check (node, not committed): up to 4 blocks right themselves; 8 blocks fall unattended in about
  1.4 s; a delayed proportional-derivative controller keeps 8 blocks up for 30 s with 150 ms and 250 ms of lag.
- Browser (Playwright, mobile landscape, synthetic pointers, not committed): two simultaneous pointers; stacking
  four blocks; letting go of the stick drops the tower.
- Not verifiable here: real two-thumb feel on iOS and Android. Tune `G`, `CRADLE_R`, `SLIDE_GAIN`, `HAND_W` and
  `FALL_TILT` on devices.
