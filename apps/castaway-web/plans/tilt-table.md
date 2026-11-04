# Tilt Table (weekly game)

Status: in-progress

## Goal

A phone-only Survivor tilt table, built on Inside Track's physics and look, in a jungle setting. Free play first
(on preview), then a scored weekly game like Free the Block.

## Rules

- Three balls, three holes. One ball in play at a time: grab it from the rack and set it on the mat, then tilt.
- A ball that leaves the table (left, right or far edge; the near edge has a rail) drops into the bamboo gutter.
  Tilt it down the gutter to the return at either bottom end, grab it, and set it on the mat again. Each fall is a rollback.
- A ball over a hole drops in if it is slow enough (under about 0.9 m/s); faster balls skip across.
- Practice until the player taps Ready, or three minutes at most. Practice balls are white and don't count.
  Then a 3-2-1 countdown, the board resets, and the timed run lasts until all three balls are in.
- Phones only: touch (coarse pointer) and live `deviceorientation` readings are required. iOS asks for motion
  access on "Let's go". The table is levelled (calibrated) at start, at Ready, and with the Level button.

## Scoring (planned, not built)

- Fastest finisher +3, second fastest +2, every finisher +1; ties share the place (the Island Scramble rule).
- +1 per ace: a ball sunk with no rollback. Assumption: aces count for players who don't finish, too.
- Record rollbacks per player.
- Time is measured on the server, from the first progress post (at "Go") to the finishing post, so reloading
  doesn't pause it. Shortcut, as with Inside Track: the physics runs on the phone, so a determined player could fake a run.

## Scored version: what it needs

Preview shares the production database and never runs migrations, so the scored version can't be on a
`feature/**` branch until its migration is on main:

- migration: `tilt_games` (instance, episode, opens/cutoff/resolved) and `tilt_plays` (started, holes mask,
  rollbacks, aces, rollbacks at the last sink, finished)
- page `/games/tilt-table/:gameID`, same-origin `POST .../progress` (holes mask, rollbacks, finished; the server
  decides aces), resume after reload, admin create/resolve, resolver loop, game results, /games hub, profile
- probst `season apply` type `tilt_table`, scores template, next-game emoji

## PWA

- `/manifest.webmanifest` and `/sw.js` are served from the root, outside the login wall.
- The service worker never caches pages (they need a session); assets are network-first with a cache fallback.
- Known risk: an iOS home-screen app has its own cookies, so the first launch needs a Discord login inside it.

## Verification

- Go unit tests: PWA files are public with the right content types; login returns to `/games/tilt-table`.
- Browser (Playwright, not committed; mobile emulation with tilt injected through `?debug`): practice → Ready →
  countdown → run; a fall counts a rollback and the ball returns through the gutter; a scripted controller sinks
  all three balls and the end card shows time, rollbacks and aces; the desktop gate; the no-sensor gate; manifest,
  icons and service-worker control after reload.
- Not verifiable here: real tilt feel on iOS and Android. Tune `GAIN`, `MAX_TILT`, `SKIM` and the rails on devices.
