# Tilt Table (weekly game)

Status: in-progress

## Goal

A phone-only Survivor tilt table, built on Inside Track's physics and look, in a jungle setting: the Season 51
week 7 scored game, like Free the Block. Free play opens only after a game closes.

## Rules

- A 0.8 × 1.875 m table: three bamboo rails whose gaps all sit against an open edge (left, right, left), the
  first two with fins that make them chutes up the edge; a mound (a smooth bump that turns slow balls back and
  bends fast ones) between the first two gaps and another in front of the cups; then three cups (red, yellow, blue)
  in bays along the far edge, as on the show. Three balls, one in play at a time: grab it from the rack and set
  it on the mat, then tilt.
- A ball that leaves the table (left, right or far edge; the near edge has a rail) drops into the bamboo gutter.
  Tilt it down the gutter to the return at either bottom end, grab it, and set it on the mat again. Each fall is a rollback.
- Cups work like Inside Track's: smaller than the ball (0.85 R), so it sits in the lip. It stays only if it reaches
  the middle slowly (arriving under about 0.3 m/s, within about 3 cm of centre); otherwise it lips out, and over
  the far cups a lip-out usually means a fall. A ball sitting in a cup is an obstacle.
- Practice until the player taps Ready, or three minutes at most: one white ball, one cup and one rail.
  Then a 3-2-1 countdown, the board resets, and the timed run lasts until all three balls are in.
- Phones only: touch (coarse pointer) and live `deviceorientation` readings are required. iOS asks for motion
  access on "Let's go". "Hold your phone flat" shows before practice and before the countdown, and the table is
  levelled (calibrated) after it, and with the Level button.

## Scoring

- Finishing +1, fastest +3, second fastest +2; ties (in tenths of a second, as shown) share the place, dense as
  in Free the Block. +1 per ace (a ball in with no rollback since the previous sink), for every player, finished or not.
- Rollbacks are recorded and shown; they cost only time and aces.
- The server keeps the clock: from the first progress save (at "Go") to the save that fills the third hole, so a
  reload doesn't pause it. Shortcut, as with Inside Track: the physics runs on the phone, so a determined player
  could fake a run.

## Data and API

- Migration `044_tilt_table.sql`: `tilt_games` (instance, episode, opens/cutoff/resolved) and `tilt_plays`
  (started, holes mask, rollbacks, aces, rollbacks at the last sink, finished).
- Page `GET /games/tilt-table[/:gameID]`: free play with `?practice=1` once any game has closed (until then that and
  the no-game page go to the scored game or /games); the scored board while
  open and unfinished (resume attributes after a reload); otherwise the player's run or the results.
- `POST /games/tilt-table/:gameID/progress` (same-origin) `{holes_mask, rollbacks}`: holes only fill, one per save;
  rollbacks only go up; the third hole finishes and locks the run. The server decides aces.
- Admin: `POST /instances/:instanceID/tilt-games` (create, or reschedule before anyone plays),
  `POST /tilt-games/:gameID/resolve`. Cutoff resolution runs with the other games.
- Game results type `tilt_table`; /games hub, scheduled games, profile. Probst `season apply` type `tilt_table`,
  scores template `games/tilt_table.md`, next-game 🌴. `seasons/51.yaml` week 7 is set to it (takes effect only after
  `probst season sync`).
- Preview shares the production database and never migrates, so the scored version must reach main before it can
  run anywhere; the free-play version is what preview shows.

## PWA

- `/manifest.webmanifest` and `/sw.js` are served from the root, outside the login wall.
- The service worker never caches pages (they need a session); assets are network-first with a cache fallback.
- Known risk: an iOS home-screen app has its own cookies, so the first launch needs a Discord login inside it.

## Verification

- Go unit tests: PWA files are public with the right content types; login returns to `/games/tilt-table`; `tiltPoints` ties and aces.
- `TestTiltTable` integration test: refusals (not open, emptied hole, rollbacks down, two holes at once, after finish), resume attributes, server clock, aces, a tie in tenths, resolve once, results, ledger, profile, scheduled games.
- Browser (scored, with mocked progress): resume restores holes, rollbacks, aces and the clock; the end card shows the server's time.
- Browser (Playwright, not committed; mobile emulation with tilt injected through `?debug`): practice → Ready →
  countdown → run; a fall counts a rollback and the ball returns through the gutter; a scripted controller sinks
  all three balls and the end card shows time, rollbacks and aces; the desktop gate; the no-sensor gate; manifest,
  icons and service-worker control after reload.
- Not verifiable here: real tilt feel on iOS and Android. Tune `GAIN`, `MAX_TILT`, `SKIM` and the rails on devices.
