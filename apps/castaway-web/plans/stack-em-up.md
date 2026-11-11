# Stack 'Em Up (weekly game)

Status: in-progress

## Goal

A phone-only Survivor stacking challenge, Season 51 week 8, scored like Tilt Table. The phone, held upright, is
the balancing device; one finger stacks letter blocks until the tower spells SURVIVOR.

## Rules

- Side view, phone upright (portrait). The tray rocks (rolls without slipping) on a low curved cradle of radius
  `CRADLE_R` (10 cm) on a fixed stand in the bottom third. Tilting the phone (its roll in the screen plane, from
  `deviceorientation`, calibrated level on "Hold your phone upright" and the Level button) tilts the stand, and the
  tray with it. A level bubble beside Ready runs uphill; its edge is the tilt that slides blocks off.
- Real rocker physics: with its load's weight h above its underside, the tray leans R/(R − h) times the phone's
  tilt and settles. 5° of phone tilt leans an empty tray 5.7°, one block 6.8°, two 10.9°; from about three
  blocks h passes R, so the tray tips on its own and you tilt against it continuously. Gravity for the rocking is
  slowed (`G` 4.5 m/s²) and the rocking damped (`DAMP` 3/s). Off-centre blocks lean the tray their way.
- The tower falls (a drop) if the tray tips past 20° from level in the world, if the blocks above any block have
  their weight past its edge at the current tilt (a tall tower goes over at a small tilt: 22° at four blocks,
  10° at eight), if the tray rolls off the cradle's end (30° on screen), or if a block is stacked past the edge of
  the one below. An empty tray just bounces back off the end.
- Free play only: `?r=0.1`, `?g=4.5`, `?damp=3` and `?fall=20` override those, to tune on a phone.
- A slotted bamboo rack top right holds the blocks in placing order (R O V I V R U S from the bottom, so the tower
  reads SURVIVOR from the top). Touching the rack pulls the bottom block; the rest drop down a slot. A small
  pulsing ring and arrow mark it; no instruction text. Let it go over the stack and it drops straight down onto
  the top block, keeping its position along the tray; anywhere else it goes back into the rack. A block whose
  middle is past the edge of the block below falls off; a block that leaves the blocks above some lower block
  overhanging topples the tower.
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
- Physics self-check (node, not committed): a tilt controller 150 or 250 ms late, with 0.3° of sensor noise and
  the wrist rate-limited to 170°/s. An empty tray holds with no input; two blocks and up need steering, and some
  gains still hold eight blocks for 15 s at 250 ms.
- Browser (Playwright, mobile portrait, synthetic pointers and `?debug` tilt, not committed): pulling four blocks
  from the rack and stacking them; a release away from the tray goes back to the rack; a 30° tilt drops the tower.
- Not verifiable here: real tilt feel on iOS and Android. Tune `CRADLE_R`, `G`, `DAMP`, `FALL_TILT` and `DEAD` on
  devices.
