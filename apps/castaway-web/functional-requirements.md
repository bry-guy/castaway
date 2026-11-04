# castaway-web Functional Requirements

`castaway-web` provides the persistent HTTP API for Castaway draft state.

## Inputs

- HTTP requests to the documented API routes
- PostgreSQL-backed persisted state
- seed data from `seeds/historical-seasons.json`
- local verification seed data from `seeds/verification-merge-gameplay.json`
- environment-based runtime configuration

## Required capabilities

- require Discord browser sessions for all content pages, with access requests for accounts not linked to the configured season; keep OAuth, non-sensitive assets, and health checks reachable
- provide responsive scores and game pages with system-following light/dark appearance and a saved manual override
- install as a PWA (web app manifest, root service worker, icons) without caching signed-in pages
- offer Tilt Table on phones (scored weekly; free play after a game closes): tilt-sensor input, grab-and-place balls, gutter rollbacks, timed run after up to three minutes of practice
- persist instance-owned, unscored Castawordle games and own-player progress; validate 4–8-letter SCOWL size 70 US/UK words and inflections, return duplicate-aware tile feedback, enforce six guesses and cutoff, and resume across devices
- automatically submit full guesses; show `invalid word, try again` without consuming a turn and clear transient feedback on edits; show total/bonus scores without a Draft column
- restrict test games, including existing previews, to instance admins on lists, detail pages, play reads, and guess writes; preserve their answers and saved progress
- let admins prepare an answer for one scheduled puzzle per instance/episode; derive opening at 1pm Eastern and cutoff at noon on the following episode date, and allow only linked players in that instance to play during the window
- permit instance-admin creation only; serialize competing guesses and replay an accepted guess/position without consuming another turn; never expose unfinished answers or write scoring inputs/bonus awards from trial play
- expose a health endpoint for local and production monitoring
- create and list instances
- import an instance from structured submissions
- create and list contestants for an instance
- create and list participants for an instance
- create and retrieve draft picks for a participant
- parse watched-thread drafts with unchanged exact/clear-fuzzy matching, bounded single-unused-contestant whole-word inference, and non-binding weak-match suggestions; reject ambiguous, incomplete, duplicate, and malformed drafts
- preserve pending submission claims without granting draft rewards until a complete draft is saved; replay messages without duplicating picks, bonuses, tribe memberships, or announcements
- create and retrieve ordered outcome positions
- compute and return leaderboard results from drafts plus outcomes, including linked Discord user ids and current tribe names for bot-facing score formatting
- support bot-friendly filters for instances, participants, contestants, activities, and leaderboard lookups
- support bonus gameplay persistence and resolution for:
  - tribal pony
  - tribe wordle
  - journeys
  - Stir the Pot
  - individual pony auctions and ownership
  - Loan Shark borrowing and repayment
  - individual pony immunity payouts
- support explicit service-authenticated first-admin bootstrap, API-owned Discord guild/channel-to-instance bindings with instance-admin authorization, and conflict-safe Discord player linking
- support service-authenticated, instance-admin Wordle round creation, result entry, closure, inspection, and retry-safe public award resolution for legacy instances; enforce cutoff and block generic occurrence-write bypasses
- let instance admins replace an instance's tribe arrangement from a point in time (start, swap, merge, split) chronologically, and award tribe immunity (+2) and reward (+1) wins to members at that time, retry-safe by key
- support opt-in Wordle scoring of +2 for the best individual result and +1 to every member of the tribe with the best submitter average
- support player-context write flows via linked Discord users for merge gameplay actions
- allow instance admins to submit Stir the Pot contributions and individual pony bids on behalf of named participants
- allow instance admins to inspect the current Stir the Pot total for a named tribe without exposing other tribes' totals
- bind newly opened Stir the Pot rounds and auction lots to the next scheduled episode for the instance
- reveal consumed secret bonus points into public-safe ledger rows when hidden spends use them
- support instance-admin write flows for opening/closing merge gameplay windows and recording immunity winners
- seed historical seasons into the database for development and testing
- keep the documented API contract aligned with the running server

## Outputs

- JSON responses for all public API routes
- persisted instance, contestant, participant, draft, outcome, bonus ledger, pony ownership, loan, and gameplay state in PostgreSQL
- generated OpenAPI output derived from TypeSpec

## Current non-goals

- production-grade multi-tenant auth and authorization beyond current trusted-local workflows
