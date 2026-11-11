# Stack 'Em Up (weekly game)

Status: in-progress

## Goal

A phone-only Survivor stacking challenge, Season 51 week 8, scored like Tilt Table. The phone, held upright, is
the balancing device; one finger stacks letter blocks until the tower spells SURVIVOR.

## Rules

- Side view, phone upright (portrait). A tray rests on two balls in a cup on a fixed stand in the bottom third.
  Tilting the phone (its roll in the screen plane, from `deviceorientation`, calibrated level on "Hold your phone
  upright" and the Level button) tilts gravity in the scene.
- The tray balances on the two balls like a stick on a finger (its weight sits above its 6 mm rocker curve), so it
  tips one way and keeps going unless the phone is tilted against it: an empty tray takes about 3 s to go from
  barely off level to over, one block about 1 s, a full tower about 1.8 s (a tall tower is a longer, slower
  pendulum, but needs bigger corrections). Gravity is slowed (4.5 m/s²); the tray is heavy (5 kg to a block's
  1 kg) so the first block doesn't throw it about.
- The balls sit on the cup's flat floor and stick until the phone tilts past 3°, then roll downhill against
  rolling friction of the same size, so they stop again once it's level. Their speeding up throws the tray the
  other way. Past the floor's edge (7 cm) they go over the lip; an empty tray tipping past 28° also goes. Either
  way balls, tray and tower fall off the stand, then a fresh, flat tray is set on after a moment.
- A level bubble beside Ready runs uphill; inside its inner ring (3°) the balls stay put.
- A slotted bamboo rack top right holds the blocks in placing order (R O V I V R U S from the bottom, so the tower
  reads SURVIVOR from the top). Touching the rack pulls the bottom block; the rest drop down a slot. A small
  pulsing ring and arrow mark it; no instruction text. Let it go over the stack and it drops straight down onto
  the top block, keeping its position along the tray; anywhere else it goes back into the rack. A block whose
  middle is past the edge of the block below falls off; a block that leaves the blocks above some lower block
  overhanging topples the tower.
- The tower falls (a drop) if the balls go over the lip, the tray tips past 28° from level in the world or it rolls
  off the balls. Every block goes back to the rack.
- Eight blocks, then hold for 3 seconds to finish.
- Practice until Ready, or three minutes at most: six blocks that spell ISLAND. Then a 3-2-1 countdown and the
  timed run.
- Phones only: coarse pointer and motion access (iOS asks on "Let's go"). `?debug` allows a desktop (arrows tilt
  a degree at a time, the mouse stacks).

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
  2.7 s; a tilt controller 150 ms or 250 ms late, with the wrist rate-limited to 170°/s, keeps 8 blocks up for
  30 s.
- Browser (Playwright, mobile portrait, synthetic pointers and `?debug` tilt, not committed): pulling four blocks
  from the rack and stacking them; a release away from the tray goes back to the rack; a 30° tilt drops the tower.
- Not verifiable here: real tilt feel on iOS and Android. Tune `G`, `CRADLE_R`, `M_TRAY`, `STICK`, `CUP_HALF`, `DEAD` and `FALL_TILT` on
  devices.
