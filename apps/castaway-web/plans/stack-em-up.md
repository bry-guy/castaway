# Stack 'Em Up (weekly game)

Status: in-progress

## Goal

A phone-only Survivor stacking challenge, Season 51 week 8, scored like Tilt Table. The phone, held upright, is
the balancing device; one finger stacks letter blocks until the tower spells SURVIVOR.

## Rules

- Side view, phone upright (portrait). The tray balances on one ball sitting in a cup on a fixed stand in the
  bottom third, like a stick on a finger. Tilting the phone (its roll in the screen plane, from
  `deviceorientation`, calibrated level on "Hold your phone upright" and the Level button) tilts gravity in the
  scene. A level bubble beside Ready runs uphill; its edge is a tilt as far as the rim.
- The tray tips one way and keeps going unless you tilt against it, continuously. How fast is a design knob, not
  physics (`TAU`, seconds to grow e-fold, by blocks on the tray): `[0.6, 1.0, 0.9, 0.8, 0.72, 0.65, 0.6, 0.56,
  0.52]`. The empty tray is twitchy; one centred block steadies it; every block after makes it quicker. A real
  empty tray would be the slowest, so this curve is imposed. An off-centre stack also pulls its side down
  (`OFF_GAIN`).
- The tray's underside meets the shallow cup's rim at about 27° on screen (from the drawn geometry), past the
  20° world fall angle, so a loaded tower normally drops before it gets there. It knocks against the rim and
  bounces back; a loaded tray touching it drops the tower, so it can't be parked there. An empty tray that lies
  on the rim for 0.6 s is set level again, and a block let go onto it slides off ("Missed!", not a drop).
- After a fall, and while waiting to play, the tray is set level in the world (not on screen), as far as the rim
  allows.
- Free play only: `?tau=0.6,1,…` and `?fall=20` override the curve and the fall angle, to tune on a phone.
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
  the wrist rate-limited to 170°/s. At 250 ms, mean survival over 20 s falls from 20 s (up to 3 blocks) to 13 s
  at 8, and some gains still hold 8. An unattended tray never reverses direction. A phone held tilted to park a
  loaded tray on the rim always drops it.
- Browser (Playwright, mobile portrait, synthetic pointers and `?debug` tilt, not committed): pulling four blocks
  from the rack and stacking them; a release away from the tray goes back to the rack; a 30° tilt drops the tower.
- Not verifiable here: real tilt feel on iOS and Android. Tune `TAU`, `OFF_GAIN`, `FALL_TILT`, `CUP_GAP`/`CUP_W` (the rim) and `DEAD` on
  devices.
