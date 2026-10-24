# Free the Block (weekly game)

Status: in-progress

## Goal

A weekly Castawordle-hub game modeled on Inside Track: one scored run per player per episode, server-scored, league points through the bonus ledger (`award_key` `free_the_block`).

## Rules

- Klotski (Huarong Dao) on a 4-wide, 5-tall board: one 2×2 idol, four 1×2 verticals, one 2×1 horizontal, four 1×1 soldiers, two empty cells. Start is the classic 横刀立马 layout (empty cells at the bottom centre).
- Blocks slide in any of four directions. Goal: the idol at cols 1–2, rows 3–4, out through the bottom gap.
- Move count: consecutive slides of one block are one move (whatever their direction). The optimum is 81; `internal/freetheblock` finds it by BFS and a unit test asserts it.
- Points: +1 for solving; among solvers, fewest moves +2 and second fewest +1; ties share the place (the Island Scramble rule). Non-solvers 0.

## Anti-cheat

- The page posts the full move list (`piece`, `dir`, `steps`) after every slide, and `finished` on a solve.
- The server replays the list from the start (`freetheblock.Replay`): it rejects blocked or malformed moves, moves after the idol is out, and `finished` without a solve.
- Saved progress is stored as a compact string (3 characters a slide). An update must extend the saved string, so a run only grows, and a finished run is locked.
- Moves and solved state come from the replay, never from the client.

## Data and API

- Migration `041_free_the_block.sql`: `free_the_block_games` (per instance and episode, opens/cutoff/resolved) and `free_the_block_plays` (encoded moves, finished time).
- Page: `GET /games/free-the-block[/:gameID]`; progress `POST /games/free-the-block/:gameID/progress` (same-origin).
- Admin: `POST /instances/:instanceID/free-the-block-games` (create or reschedule before anyone plays), `POST /free-the-block-games/:gameID/resolve`. Cutoff resolution runs in `ResolveDueGames` and the button resolver loop.
- Game results: `GET /instances/:id/game-results/:episode` returns type `free_the_block` (`place`, `points`, `moves`, `solved`). The admin alerts list and `/games` hub include it; the player profile lists it.
- Probst: `season apply` (`type: free_the_block`), scores template `games/free_the_block.md`, next-game 🧱.

## Not in this change

- No season week is scheduled for it yet; `seasons/51.yaml` weeks 6–12 are still TBD.
- No admin list/correction endpoint for runs (Inside Track has one). A correction would mean editing a move list; add it if an episode needs one.
- The layout exists twice: `internal/freetheblock` and `site/assets/free-the-block.js`. They must stay in step; the integration test covers the server side only.

## Verification

- `internal/freetheblock` unit tests: optimum 81, replay rules, encoding round trip.
- `TestFreeTheBlock` integration test: illegal and non-extending lists, finish rules, BFS solve, a tie at 81, a solve at 82, resolve once, results, ledger, profile, scheduled-games list.
- Browser smoke test (Playwright, not committed): the BFS solution played by drags reaches MOVES 81 and the end card; a saved solved run loads locked as finished.
