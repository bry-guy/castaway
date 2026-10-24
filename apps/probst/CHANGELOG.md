# Changelog

## Unreleased

- Free the Block: `season apply` creates `free_the_block` games (`POST /instances/:id/free-the-block-games`) for weeks set to `type: free_the_block`; the scores post renders `games/free_the_block.md` from each solver's `moves` and `solved`; `next-game.md` shows 🧱.

- `probst track plays --episode N` prints an episode's Inside Track runs (place, score, cups, balls left, finished, points). `probst track set PLAYER --episode N --cups N [--mask M] [--balls-used K] [--yes]` corrects one run: `--cups` fills the lowest N cups, balls used stays unless `--balls-used` is given, and a dry run shows before → after.

- Scores posts are drafted for approval `automation.approval_lead` before their time (default `12h`, so the 8pm post's approval DM arrives at 8am ET) instead of only once the game is scored. Until the game is scored the draft shows a placeholder for its results; the server holds it, so a placeholder never sends, and the redraft with the results re-asks. An unscored game at the post's time still waits and alerts admins.

- Once a scores post's game is scored, an admin-edited draft keeps the admin's text: the results replace its placeholder line and the post is re-asked. If the admin deleted the placeholder line, the post stays held and admins get a `results-missing` alert.

- Approval DMs lead with the send time in Eastern (`**posts Wed Oct 21 8:00pm ET**`), on the first ask and every re-ask.

- Tribe colors: `tribes set` files accept `Dina (pink): Adam, Keeling`; a tribe that doesn't exist yet needs a color, so a typo can't create one. `probst tribes color TRIBE COLOR [--yes]` recolors an existing tribe without re-running a swap. `tribes show` prints each tribe's emoji. Pink (🩷) is a color.
- `probst tribes announce --season FILE --episode N` renders the tribe swap post from `messages/tribe-change.md` (tribes at the swap, each member mentioned, tribe-challenge points for the episode); `--yes` saves an unsent draft. `--kind swap|merge|assignment` picks the first line.

- Scores post grammar: leader and gainer lines take plural verbs when several players share the spot (`lines/*.txt` get `{{if .Plural}}`), and `next-game.md` shows 🎯 for Inside Track.

- Inside Track: `season apply` creates `inside_track` games (`POST /instances/:id/track-games`), `season apply --week N` applies one week, and the scores post renders `games/inside_track.md` from `score`, `cups`, `balls_left`.

- Island Scramble post reads `hint_points` (before-hint bonus) instead of `time_points`.

- Island Scramble scores section shows the new rules and each finisher's time and wrong-check points.

- Scheduled announcements (`announcement send --at`, `announcement schedule`) wait for an admin's DM "yes"; `schedule --yes` send-now removed. `message` and immediate `send` still post directly.

- Press the Button scores section calls out every rule that fired (most, second, ties, fewest, volume, streaks, first, last).

- Admin DMs from the scheduled job (results missing, holds, import proposals, conflicts, scores waiting/expired, game not ready, daily nudge) now come from the season's `messages/*.md` templates; multi-person eliminations use `lines/boot-many.txt`.

- `probst season sync FILE --fix-episodes` sets the live episode schedule to the file's first (dry run shows each change); `probst season post --preview` renders a dry run before the week's game is scored.

- All post wording now lives in the season's template directory (`seasons/51-templates/`: `scores.md`, `reminder.md`, `next-game.md`, `games/<type>.md`, `lines/*.txt`); no message text in code. `probst season sync` stores the whole set. The scores post gains a per-game results section (`{{.GameScore}}`), and `weekly.reminder.at` adds a game-day reminder drafted for admin approval when the game opens (no pings).

- Pick Your Champion: `probst season merge FILE N` ends Tribe scoring at merge Episode N and opens picks (website `/games/champion`, or reply to the bot's "Pick Your Champion" post) until Episode N+1 airs; then picks are fixed and shown to all. Post-merge challenges import from survivoR and award pickers of the winners (+3 immunity, +1 reward). The merge episode's own challenges are held for an admin.

- Failure alert: each successful scheduled `season reconcile --instance` run stamps a heartbeat; if none lands for 30 minutes, admins get one DM per stall.

- Season config lives in the database: `probst season sync FILE` saves the season file and scores template (revision-checked, refuses on episode mismatches); the scheduled `season reconcile --instance ID` runs the synced copy.

- Episode imports that need judgment (medevac/quit, two people leaving) are DMed to admins as an exact proposal; replying **yes** records it (`POST /admin-alerts/:id/approve`, revision-bound). `probst episode import N --file F` replaces `season import`/`episode sync`.

- Spell It Out is now called **Island Scramble** (display name only; `spell_it_out`, URLs and data unchanged).
- Tests use their own season files (`testdata/`), so `seasons/*.yaml` can change freely; added a non-Wednesday/DST/skip-week schedule test.

- `season reconcile FILE` (the scheduled job): next-game readiness alerts, all-or-nothing survivoR episode import with holds for anything ambiguous, and scores posts drafted for admin approval once the week's game is scored and results are in. `season check FILE` runs only the readiness check; `season import FILE --episode N` runs the import by hand. Season files gain `automation: {enabled, from_week, guild}`.
- Daily reminder DM (`automation.nudge_at`, default 10am ET) collecting every still-open issue.
- `boot PLACE CONTESTANT` records a finishing place by hand (e.g. a medevac the import held).
- Scores posts: when everyone moved by the same amount, say so instead of "nobody moved"; game links point to `/games`.
- Plain HTTP is allowed only to loopback or the exact URL in `PROBST_ALLOW_HTTP_SERVER`.

- Infer a single unmatched whole name word from a complete draft's sole unused contestant, without adding artificial nicknames or changing fuzzy thresholds. Weak candidates remain confirmation-only suggestions.
- Add offline Keeling and Mooney original-message coverage, including all ranks, pending submission credit, and replay safety.

- `contestant rename CONTESTANT NEW_NAME`.

- `login`/`logout` (Discord login for the public `/api`), `admin add`, and `access list|approve|deny`.
- `draft watch THREAD_URL` and `draft reject PLAYER` for bot-read draft threads.
- `recap --episode N` prints the weekly scores post (spoilered boots, biggest gainer/loser, leaderboard with tribes) and diffs against last week's local snapshot.
- Wordle rounds now run 1pm ET on an episode's air day to noon on the next one's.
- `episode sync --season S --episode E` pulls survivoR data for an episode and records boots and tribal challenge wins.
- `draft open --tribes` / `draft close` run draft-submission events (+2/+1, balanced random tribes, pinged posts).
- `announcement mark-sent` records announcements you posted yourself.
- `scores` hides players without a draft.
- `--notify` on `message` and `announcement save`/`send` lets `<@user>` mentions ping.
- Config `aliases` give instances and channels friendly names (`--instance podracing`); with an instance alias, `CHANNEL` defaults to its channel for `message` and `announcement save`/`send`.
- Add draft announcements: `announcement save`/`show`/`edit`/`schedule`/`unschedule`/`delete` by name, with a readable `list`.
- Add `message` for one-off, unsaved bot posts (optionally as a reply).
- `wordle import --file` reads hand-scored results.
- Add the weekly loop: `tribes set`/`show`, `challenge immunity|reward`, and `wordle open`/`import`/`submit`/`resolve`.
- Add `announcement send`/`list`: queue verbatim Discord announcements (now or `--at` a scheduled time) that the Castaway bot posts with mentions disabled.
- Add optional owner-only local JSON configuration for Probst API and Discord credentials, with environment and flag overrides.
- Add season setup (`instance create`, `contestant list`, `player add`) and `draft import` from Discord threads or text files, with chat-tolerant parsing and dry-run review.

- Add the Probst HTTP operator client for instance inspection, first-admin bootstrap, channel bindings, player links, drafts, and public scores.
- Add destructive-action confirmation, structured output, and disposable API integration coverage.
