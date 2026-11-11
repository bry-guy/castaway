# Stack 'Em Up (weekly game)

Status: in-progress

## Goal

A phone-only Survivor stacking challenge, Season 51 week 8, scored like Tilt Table. The phone, held upright, is
the balancing device; one finger stacks letter blocks until the tower spells SURVIVOR.

## Rules

- Side view, phone upright (portrait). The phone is the pole: it stands still on screen, running off the bottom,
  with a ball on top that the tray hinges on. Weights hang under the tray's ends (`KEEL_M` each, 15 cm down).
  Tilting the phone swings the pole's top sideways as if it were `STICK_L` (0.6 m) long, through a smoothing
  follower so the sensor's jitter isn't amplified; jerking the phone sideways (`devicemotion` acceleration, dead
  band 0.15 m/s², clamped ±6, scaled by `SHAKE`) pushes it too. Only the pole's top moving matters: holding the
  phone tilted and still does nothing.
- Real physics, a pendulum on a moving pivot: while the load's weight (tray, weights, blocks) sits below the ball,
  0–2 blocks, the tray hangs level by itself. From three it sits above, a broom: it tips unless you move the
  pole's top the way it leans to get under it (lean or jerk the phone that way). Gravity is slowed (`G` 4.5 m/s²)
  and the swing has air drag (`DAMP` 1/s). Off-centre blocks lean the tray their way.
- The tower falls (a drop) if the tray tips past 20° from level in the world, if the blocks above any block have
  their weight past its edge at the current tilt (22° at four blocks, 10° at eight), if the tray swings into the
  pole (60°), or if a block is stacked past the edge of the one below. An empty tray bounces off the pole.
- The level bubble runs uphill; at its edge the pole can't swing further (40°).
- Free play only: `?len=0.6`, `?keel=1.1`, `?g=4.5`, `?damp=1`, `?shake=1` (0 off, negative flips its direction)
  and `?fall=20` override those, to tune on a phone. iOS is asked for orientation and motion access in one tap.
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
- Physics self-check (node, not committed): 0–2 blocks hang level unattended and under a held 15° phone tilt;
  3+ fall unattended within about 2 s; tilting toward the lean reduces it; a 3 m/s², 150 ms jolt drops eight
  blocks but not two. A broom-balancing player that sees the tower 150 or 250 ms late (0.3° noise, wrist
  rate-limited to 170°/s) but knows its own hand holds 4 and 8 blocks for 15 s at both lags (6 at 150 ms).
- Browser (Playwright, mobile portrait, synthetic pointers and `?debug` tilt, not committed): pulling four blocks
  from the rack and stacking them; a release away from the tray goes back to the rack; a 30° tilt drops the tower.
- Not verifiable here: real tilt feel on iOS and Android. Tune `STICK_L`, `KEEL_M`, `SHAKE`, `G`, `DAMP`, `FALL_TILT` and `DEAD` on
  devices.
