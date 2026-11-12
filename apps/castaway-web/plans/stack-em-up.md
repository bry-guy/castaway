# Stack 'Em Up (weekly game)

Status: in-progress

## Goal

A phone-only Survivor stacking challenge, Season 51 week 8, scored like Tilt Table. The phone, held upright, is
the balancing device; one finger stacks letter blocks until the tower spells SURVIVOR.

## Rules

- Side view, phone upright (portrait). A tray rests on two balls in a cup on a fixed stand in the bottom third.
  Tilting the phone (its roll in the screen plane, from `deviceorientation`, calibrated level on "Hold your phone
  upright" and the Level button) tilts gravity in the scene. A level bubble beside Ready runs uphill and reaches
  its edge at the rim.
- The tray rocks on the balls as if on an 8 cm rocker with little friction, and the balls rolling in the cup throw
  it about (SLOSH), so even an empty tray wobbles. One or two blocks survive if you're gentle; from about four
  blocks the tower falls in about 1.5 s unless you tilt against the lean. Gravity is slowed (4.5 m/s²).
- A slotted bamboo rack top right holds the blocks in placing order (R O V I V R U S from the bottom, so the tower
  reads SURVIVOR from the top). Touching the rack pulls the bottom block; the rest drop down a slot. A small
  pulsing ring and arrow mark it; no instruction text. Let it go over the stack and it drops straight down onto
  the top block, keeping its position along the tray; anywhere else it goes back into the rack. A block whose
  middle is past the edge of the block below falls off; a block that leaves the blocks above some lower block
  overhanging topples the tower.
- The balls roll in a round cup toward wherever gravity points. Tilt the phone past about 11° (12° is the rim) and
  they roll over it: balls, tray and tower all fall off the stand, and a fresh tray is set on after a
  moment.
- The tower falls (a drop) if the balls leave the cup, the tray tips past 28° from level in the world or it rolls
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
- Not verifiable here: real tilt feel on iOS and Android. Tune `G`, `CRADLE_R`, `DAMP`, `SLOSH`, `CUP_R`, `CUP_DAMP`, `CUP_LIP`, `DEAD` and `FALL_TILT`; in free play
  `?r= ?cupr= ?cupdamp= ?slosh= ?lip= ?fall= ?g= ?damp=` override them on a phone without a deploy on
  devices.

## Tuning (after trying a single ball, a cradle and a pole, all worse)

Back to the two balls in a round cup, with: overdamped balls (`CUP_DAMP` 35), so tilting leans the tray now and the
bowl forgives over about a second; the balls only go over once the outer one reaches the drawn rim (`CUP_LIP` 24°). Rocking friction
`DAMP` 4, so a flicked empty tray settles in about 1.2 s instead of rocking in waves. The balls throw the tray
less (`SLOSH` 0.3), and less the heavier it is. A tall tower topples over its own base at a smaller tilt (22° at
four blocks, 10° at eight). Past 20° (`FALL_TILT`, wood on wood) the tower slides along the tray and falls off
the end. A fallen tower and tray really fall: the drop counts, and the tray is set flat with the blocks back in the
rack, only once they hit the ground. The tray waits flat until play.

A fallen tower is handed to a small rigid-body engine (vendored `site/assets/planck.min.js`, planck.js 1.5.1, MIT):
each block starts where it was, moving with the tray, and tumbles under the screen's gravity off the tray (which
it follows), the pole and onto the sand. The drop counts once they're all on the sand or off screen, or after 3 s
if some came to rest on the tray. `DAMP` 3.3 (was 4) for a touch more wobble.

The rig (tray, saddle, balls, cup) is 30% bigger than the first build (`BIG`); blocks aren't, so there's more tray to
slide along before blocks fall off. The cup's ball friction is scaled with it so the balls drift as before. The tray
sits on a wooden saddle across the balls (`SADDLE`), so it reaches the cup's rim only at 48° against the cup: 20° of
slide plus the 24° lip, so the phone is already past dumping the balls. There's no "Hit the bowl" fall; if the tray
gets there, the rim stops it.
