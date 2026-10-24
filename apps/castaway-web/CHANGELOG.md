# Changelog

All notable changes to `castaway-web` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]
- Free the Block weekly game (`/games/free-the-block`, migration 041): slide the blocks to free the idol through the bottom gap (Klotski, 横刀立马 start). The page saves the whole move list after every slide; the server replays it from the start, so it alone counts moves and decides whether the idol is out. One scored run per player, open until solved or the cutoff; a finished run is locked. Consecutive slides of one block count as one move. League points: +1 for solving, fewest moves +2, second fewest +1 (ties share the place). Admin API `POST /instances/:id/free-the-block-games` and `POST /free-the-block-games/:id/resolve`; game results type `free_the_block` with `moves` and `solved`; admins get the unscored practice board at `/games/free-the-block?practice=1`.
- Player profiles reveal drafts once any episode is scored, even if draft submissions were never closed (players saw "Drafts are revealed after drafts close" mid-season).
- Inside Track admin corrections: `GET /instances/:id/track-games/:episode/plays` (admin) lists every run with place, score, cups, balls used, finished and league points, before or after resolve. `PUT /instances/:id/track-games/:episode/plays/:participant` (admin) sets `cups_mask` (0–7), `balls_used` (0–12) and `finished`, validated like progress; it may move a run backwards, returns 409 once the game resolves, and writes only the play row.
- Scores posts drafted before their game is scored may carry a placeholder (`PUT /instances/:id/score-posts/:key` takes `awaiting_results`). Such a post is held at send time instead of sent; an unchanged-standings redraft with new text now replaces an unedited draft (and re-asks) instead of being ignored (migration 040).
- `PUT /instances/:id/score-posts/:key` takes `game_results` and `results_placeholder`. Once the game is scored, an admin-edited draft that still has its placeholder line gets the results in its place (`action` `results-inserted`), keeps the admin's text, and is re-asked; without the line it stays held and admins get a `scores-results-missing` alert (`action` `results-missing`).
- Tribe colors through the API: `PUT /instances/:id/tribes` takes an optional `color` per tribe; a tribe that doesn't exist yet needs one (so a typo can't create a tribe). `PATCH /instances/:id/tribes/:name` (admin) recolors an existing tribe without touching its membership history. `GET /instances/:id/tribes` returns each tribe's `color` and `emoji`. Pink (🩷, `#ff4fa3`) joins the colors (migration 039).
- Island Scramble scoring: +1 for solving, +1 for solving before the hint, fewest wrong checks +2 and second +1 (max 4; ties share); no speed points. Game results replace `time_points` with `hint_points`.
- Inside Track weekly game (`/games/inside-track`, migration 038): 3 practice balls then 12 scored balls into three foam-track cups; score is cups plus balls left once all three are in; one run per player, saved after every ball so a reload resumes. League points equal the score. Admin API `POST /instances/:id/track-games`, `POST /track-games/:id/resolve`; game results type `inside_track` with `score`, `cups`, `balls_left`. Admins get the practice board when no game is open.

- Island Scramble hint: from 8am Eastern on the fifth day after opening (Monday), the page shows one letter per word, picked from a hash of the game ID.

- Press the Button game results include each player's rule breakdown (`rank_rule`, `rank_points`, `volume_points`, `streak_points`).

- Draft buff posts, the draft-rewards thread, and admin alerts (rescored post, stale automation) come from embedded templates in `internal/httpapi/messages/`.

- `PUT /instances/:id/episodes` (admin) creates or moves a season's episodes to match a season file.

- Weekly scores posts include the week's game results (new `GET /instances/:id/game-results/:episode`), and automated posts without a score fingerprint (game-day reminders) use the same approval-gated `score-posts` upsert.

- Pick Your Champion: `probst season merge FILE N` ends Tribe scoring at merge Episode N and opens picks (website `/games/champion`, or reply to the bot's "Pick Your Champion" post) until Episode N+1 airs; then picks are fixed and shown to all. Post-merge challenges import from survivoR and award pickers of the winners (+3 immunity, +1 reward). The merge episode's own challenges are held for an admin.

- Failure alert: each successful scheduled `season reconcile --instance` run stamps a heartbeat; if none lands for 30 minutes, admins get one DM per stall.

- Season config lives in the database: `probst season sync FILE` saves the season file and scores template (revision-checked, refuses on episode mismatches); the scheduled `season reconcile --instance ID` runs the synced copy.

- A held post whose time passes unapproved gets one "⏰ Missed" DM; replying **yes** posts it immediately (late approvals now allowed for 7 days, was 3 hours).

- Episode imports that need judgment (medevac/quit, two people leaving) are DMed to admins as an exact proposal; replying **yes** records it (`POST /admin-alerts/:id/approve`, revision-bound). `probst episode import N --file F` replaces `season import`/`episode sync`.

- Spell It Out is now called **Island Scramble** (display name only; `spell_it_out`, URLs and data unchanged).

- Season automation (migrations 031–032): one-time admin alert DMs (`PUT /instances/:id/admin-alerts/:key`, delivered by the bot); `GET /instances/:id/scheduled-games` (each week's game and whether it's scored, no answers); `POST /instances/:id/episode-imports` applies an episode's boots and tribe challenge wins in one transaction, reusing matching hand-entered results and refusing conflicting ones; weekly standings snapshots (`/score-snapshots/:week`, frozen once that week's post is sent); `PUT /instances/:id/score-posts/:key` saves automation's scores post for approval tied to the standings it describes. Such a post can't be approved or sent once scores change; untouched drafts are redrafted, admin-edited ones keep the admin's text and are re-asked.

- Fix: Spell It Out did nothing for admins, because `scramble.js` and `game-admin.js` both declared a top-level `form` and the second script failed to load; both now run in their own block. The browser check now covers Spell It Out (30 letters + 10 decoys at 320px, drag to fill/swap/return, wrong check, solve) and fails on any page error.
- Spell It Out: drag tiles onto slots (swapping with what is there) or back to the tray, alongside tap-to-place; phrases may be up to 30 letters plus up to 10 decoys (decoys no longer count against the limit). Game tiles in Spell It Out and Castawordle use a regular-width bold system font instead of Impact.
- Spell It Out (migration 029): arrange letter tiles (the phrase plus random decoys) into a hidden phrase at `/games/scramble/:id`. Tiles stay hidden until the player presses Start, which starts their clock; checks only say right or wrong. At cutoff the three fastest solves earn +3/+2/+1 (fewer wrong checks breaks a time tie; exact ties share). Admins create games with `POST /api/instances/:id/scramble-games` (or Probst `season apply` with `type: spell_it_out, phrase_file, decoys`) and one-hour admin-only tests from `/games`. Phrases are A–Z and spaces, up to 30 tiles with decoys; words longer than 9 letters wrap.
- Press the Button hints now come from the server and hint at every scoring rule without explaining it: first press, landing on a shared count, taking first or second place, a first press at the bottom, 3–5 day streaks, and 100/1,000/10,000 presses, plus 10/50/250/500 as no-points noise.
- Games hub: every game lives under `/games` (episode list, `?season=` for past seasons), `/games/castawordle/:id` and `/games/button/:id`; old `/castawordle` and `/button` links redirect. Closed games show everyone's results to the season (Castawordle after close with the answer; Press the Button once scored), and profiles list a player's game results.
- Press the Button (migration 028): count readout, Savu/Toka press totals, hints at 10/50/100/250/500/1,000 presses (no points), daily streaks (3/4/5+ Eastern days: +1/+2/+3), first presser +1, last presser −1.
- Admin DM approvals (migration 027): `PUT .../announcements/{id}/approval` holds a post until an instance admin replies "yes" to the bot's DM; it then sends at exactly its time (never late, never other text; edits re-ask).
- Draft problems DM every instance admin; replying with `7. Thien An` lines resubmits the corrected draft as the player's (admins only; never over a saved draft).
- Press the Button shows a small "count: N" under the button, and players earn +1 per order of magnitude past 10 presses (100: +1, 1,000: +2, 10,000+: +3), on top of the ranking rules.
- Standings tiebreakers everywhere: total, then draft points (without bonus), then earliest first draft submission.
- Private Castawordle (`POST /castawordle/{id}/private`, migration 026): one player's own puzzle for a scored week. Only they see it, it hides the original from them, and its result replaces theirs in the original's round.
- Press the Button admin test games: "Test Press the Button" on Games makes a one-hour admin-only game at `/button/{id}` that never scores.
- Press the Button (/button, migration 025): one big button, presses counted per player, scored at cutoff (most +2, 2nd most -1, least +1, shared counts +group size up to 3); resolves itself each minute. Leaderboard accepts `?at=`; outcomes include `updated_at`.
- Profile drafts use short names (survivoR `castaway`, migration 024) and show each scored pick as (+value − distance) with an ⓘ explainer; current-season eliminations are italic once scored. Past standings hide Tribe when a season had none.
- Nav "Castawordle" is now "Games" (/games hub); past standings show a 🏆 for winners and hide Bonus when a season had none; profile season header links to standings.
- Players can view and change their own pronouns on Me; pronouns are never shown to other players.
- Seasons page lists league seasons with winners; past seasons have standings pages, and past league players have profiles. Participants gain a hidden pronouns field (migration 023).
- Profiles: Me page and clickable Scores names show a season score and draft plus league history (secret points hidden; current drafts revealed after drafts close). Castawordle guesses submit with Enter. Cookieless mobile sign-in asks to confirm the Discord account.
- Make test puzzles admin-only, preserving existing answers/progress. Admins can prepare one player-visible puzzle per instance episode; derive the 1pm–next-episode-noon Eastern window from the instance schedule, with DST support and duplicate protection (migration 021). Gameplay remains unscored.

- Replace the pronunciation dictionary with SCOWL size 70 US/UK words and inflections; preserve historical saved-guess replay. Invalid-word errors read `invalid word, try again` and clear after edits. Remove the Scores page's Draft column without changing scoring.

- Automatically check complete Castawordle guesses on touch/physical keyboards; rejected words remain editable and cost no turn. Darken ruled-out keyboard letters in both themes.

- Discord-authenticated content pages, responsive system typography, and device-following/saved light/dark themes.
- Unscored Last Torch Castawordle at `/castawordle/{gameID}`: instance-owned 4–8-letter puzzles, private server answers, six guesses, duplicate-letter feedback, transactional retries, and persisted own-player progress (migration 020).
- Admin trial creation and PostgreSQL/browser coverage for authorization, mobile layout, theme persistence, concurrent moves, lost responses, resume, and absence of scoring side effects.

- Watched drafts can infer a single whole name word from the sole unused contestant in a complete, valid draft. Weak suggestions require a corrected submission; automatic fuzzy thresholds and ambiguity checks remain unchanged.
- Test the captured Keeling and Mooney original messages offline, including all ranks, no rewards for unconfirmed guesses, pending submission credit, and idempotent replay.

- Draft buffs after 1st/2nd go to a shared "Draft rewards" thread; announcements can target a bot-opened thread (migration 019).

- `PATCH /instances/{id}/contestants/{contestantID}` renames a contestant (admin only).

- Optional public listener (`PUBLIC_PORT`): website, Discord login, and session-only `/api`; access requests; `POST /instances/{id}/admins`.

### Added
- Watched draft threads: `PUT .../draft-submissions/thread`, `GET /draft-threads`, `POST /draft-threads/{id}/messages`, and `POST .../draft-submissions/{participantID}/reject`. A player's first draft post claims their submission order even if it has problems; migration 018 records handled message versions.
- Draft-submission events: `POST /instances/{id}/draft-submissions` (open) and `/close`; first drafts record order, +2/+1 bonus, a balanced random tribe, and a queued announcement atomically.
- Leaderboard rows include `has_draft`; `POST .../announcements/{id}/sent` records hand-posted announcements.
- Announcements accept `notify_users` (migration 017) so `<@user>` mentions can ping; `@everyone`/roles never do.
- Draft announcements (migration 016): create with `draft: true`, edit text (`PUT .../body`), schedule or send now (`PUT .../schedule`), unschedule back to draft (`DELETE .../schedule`), or delete, until the bot claims it. The bot sends the latest text.
- Season 51 episode schedule (CBS Wednesdays 8pm ET), admin tribe assignment over time (`/instances/{id}/tribes`), tribe immunity/reward awards (`/tribe-challenges`, +2/+1), and opt-in Wordle scoring: +2 best individual, +1 to the tribe with the best submitter average.
- API-owned Discord channel bindings, restricted first-admin bootstrap, and transactional player-link administration for Probst.
- Repeatable Season 43 Hurl rehearsal using pinned survivoR data, fake drafts and public Wordle awards, with explicit opt-in accelerated BrainLand delivery.
- Admin-operated Wordle rounds for legacy instances, with explicit submission windows, public bonus awards, atomic resolution, and retry-safe creation/closure/resolution.

## [0.1.0] - 2026-03-06

### Added
- Persistent Castaway web API with instances, participants, drafts, outcomes, and leaderboard endpoints.
- Bot-friendly instance, participant, and leaderboard filters.
- OpenAPI generation and route parity checks to prevent API drift.
