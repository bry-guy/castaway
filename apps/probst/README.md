# Probst

Small HTTP-only Castaway operator client for season setup and draft loading. No database access, scheduler, or OAuth login.

## Setup

Build from the repository root:

```sh
MISE_EXPERIMENTAL=1 mise run //apps/probst:build
apps/probst/bin/probst --help
```

For one-time local setup, create `~/.config/probst/config.json` with owner-only permissions (`mkdir -p ~/.config/probst && chmod 700 ~/.config/probst`; create the file with `umask 077` and verify `chmod 600 ~/.config/probst/config.json`). Do not commit the file or print its credentials:

```json
{
  "api_url": "https://castaway.bry-guy.net/api",
  "discord_user_id": "YOUR_DISCORD_USER_ID",
  "token": "SET_BY_PROBST_LOGIN",
  "discord_bot_token": "YOUR_DISCORD_BOT_TOKEN",
  "aliases": {
    "podracing": {"instance": "INSTANCE_UUID", "guild": "GUILD_ID", "channel": "CHANNEL_ID"}
  }
}
```

An alias works anywhere `--instance` or a `CHANNEL` argument goes and fills in `--guild`. With an `--instance` alias, `CHANNEL` is optional and defaults to the alias's channel: `probst message "hi" --instance podracing --yes`, `probst announcement save week-2 --file week2.md --instance podracing --yes`.

`https://castaway.bry-guy.net` is the tailnet HTTPS endpoint for the Castaway web API; routine Probst calls no longer need a port-forward. Probst does not fetch Kubernetes secrets or configure Tailscale for you. Obtain credentials through your approved secret provider; the JSON file stores them as plaintext on your machine, so prefer environment injection if that is unsuitable. Missing config files preserve the environment-only workflow. Explicit flags (`--server`, `--actor`) override environment variables, which override config fields; an explicitly empty environment value also overrides the file. HTTPS is required except on loopback. Redirects are rejected. `auth status` verifies service access and reports the asserted actor; it does not authenticate a human or prove admin access to every instance.

## Commands

```sh
probst login                      # Discord login in your browser; saves a session to the config file
probst logout
probst admin add DISCORD_USER --instance INSTANCE
probst access list                # website access requests (the bot also DMs you)
probst access approve DISCORD_USER NAME --instance INSTANCE   # = player add NAME --discord-user USER
probst access deny DISCORD_USER
probst auth status
probst instance list
probst instance show INSTANCE
probst instance bootstrap-admin INSTANCE
probst instance create --name NAME --season N --contestants-file seasons/51-contestants.txt
probst contestant list --instance INSTANCE
probst channel bind CHANNEL --guild GUILD --instance INSTANCE
probst channel show CHANNEL --guild GUILD
probst channel unbind CHANNEL --guild GUILD --yes
probst player list --instance INSTANCE
probst player add NAME [--discord-user USER] --instance INSTANCE
probst player link PARTICIPANT --discord-user USER --instance INSTANCE
probst player unlink PARTICIPANT --instance INSTANCE --yes
probst draft show PARTICIPANT --instance INSTANCE
probst draft import THREAD_URL --instance INSTANCE [--before RFC3339] [-v] [--yes]
probst draft import --file FILE --participant PLAYER --instance INSTANCE [--yes]
probst scores --instance INSTANCE
probst announcement send CHANNEL --guild GUILD --instance INSTANCE --file FILE [--at "2026-10-01 20:00"] [--yes]
probst announcement list --instance INSTANCE

# Drafts: save once, edit freely, schedule when ready. NAME is yours to pick (e.g. week-2-results).
probst announcement save NAME [CHANNEL] --instance INSTANCE --file FILE --yes
probst announcement show NAME --instance INSTANCE
probst announcement edit NAME --instance INSTANCE --file FILE --yes
probst announcement schedule NAME --instance INSTANCE --at "2026-10-07 19:00"   # requests approval; sends at --at after a "yes"
probst announcement unschedule NAME --instance INSTANCE                          # back to a draft
probst announcement delete NAME --instance INSTANCE --yes
probst announcement mark-sent NAME --instance INSTANCE [--message MESSAGE_ID]   # you posted it yourself

# One-off reply as the bot, sent right away and not saved.
probst message [CHANNEL] "The tribe has spoken." --instance INSTANCE [--reply-to MESSAGE_ID] --yes
```

Use `--json` for machine-readable output. `--server` and `--actor` override configuration. A channel rebind requires `--yes` and API admin authorization over both instances. Player identity replacement requires explicit unlinking first; conflicts never silently transfer identities.

**Scheduled announcements wait for approval.** `announcement send --at` and `announcement schedule` save the text as a draft and DM every instance admin; nothing posts until one replies "yes" (then it sends at `--at`, or right away if that time has passed). `message` and `announcement send` without `--at` are your own immediate posts and are not gated. `announcement send` previews the file verbatim (mentions never notify unless `--notify`); `--at` takes America/New_York wall time or RFC3339. Re-running the same send is a no-op. A failed send is marked `failed` in `announcement list` and in bot logs and is not retried; to send it again, re-run with a new `--key`. Saved announcements live in the instance, so `announcement list` is where to find them. Anything not yet sent can be edited, rescheduled, unscheduled, or deleted; the bot uses whatever the text is when it sends. Mentions never ping unless you pass `--notify` (to `message`, `announcement save`, or `announcement send`), which lets `<@user>` mentions ping those players; `@everyone` and roles never ping. `message` posts directly with the bot token in your config and keeps no record; if it errors, check the channel before resending.

Bootstrap is disabled unless the API has `BOOTSTRAP_ADMIN_DISCORD_USER_ID` configured. The service-authenticated actor must match that ID, and the instance must have no admins. An already-authorized matching retry succeeds. This does not grant access to existing administered instances.

Player commands use API-owned guild/channel bindings; threads inherit a parent binding when they lack an explicit binding. Bindings do not validate Discord permissions—the bot verifies thread context when resolving inheritance.

## Season setup

These steps use Probst against the API. Check for an existing instance before `instance create`: repeating it creates another instance with the same name and season. Bootstrap retries for the same authorized admin and unchanged draft imports are safe; `player add` reuses a same-name player on a sequential retry but is not concurrency-safe idempotency.

```sh
# Production access: the public /api requires a Discord login session and
# rejects service tokens (those are only valid in-cluster on castaway-web:8080).
export PROBST_API_URL=https://castaway.bry-guy.net/api PROBST_DISCORD_USER_ID=235246238382030849
probst login

probst instance create --name "Season 51 (BrainLand)" --season 51 --contestants-file seasons/51-contestants.txt
probst instance bootstrap-admin INSTANCE              # makes PROBST_DISCORD_USER_ID the first admin
probst channel bind CHANNEL --guild GUILD --instance INSTANCE
probst player add NAME --discord-user USER --instance INSTANCE   # reuses a same-name player
probst draft import THREAD_URL --instance INSTANCE --before CUTOFF   # review, then add --yes
```

Contestant files list one name per line; write nicknames in quotes (`Danny "Kilby" Kilby`). First names, surnames, and nicknames all become draft aliases.

Limits: `bootstrap-admin` works only on an instance with no admins and only for the API's configured `BOOTSTRAP_ADMIN_DISCORD_USER_ID`. There is no command to add a second admin.

## Loading drafts

After one-time configuration and private HTTPS routing, the usual dry run is `probst draft import THREAD_URL --instance INSTANCE --verbose`. Review it, then repeat with `--yes` to submit only READY drafts. Keep `--instance` explicit to avoid writing to the wrong season.

`draft import` is a dry run unless `--yes`. It prints one row per player: `READY`, `UNCHANGED`, `NEEDS REVIEW` (with reasons), `UNLINKED` (a Discord author with no player link), or `NO DRAFT`. `-v` shows how every line matched. `--yes` writes only `READY` drafts; everything else needs a repost or a `--file` import.

Thread reading needs `CASTAWAY_DISCORD_BOT_TOKEN` (fnox profile `castaway-discord-bot`) and the bot's Message Content intent. Rules:

- A message is a draft only if at least 15 of its lines name contestants; chat is ignored.
- Each author's latest draft before `--before` wins; later drafts and edits after the cutoff are reported.
- Numbered lines rank by number (any of `1.`, `1)`, `1 -`, `1:`); otherwise lines rank top to bottom. Comma lists work.
- Names match exactly on full name, quoted nickname, first name, or surname, or fuzzily when the winner is clear. Existing fuzzy thresholds are unchanged. In a complete, correctly numbered (or unnumbered), duplicate-free draft with exactly one unmatched pick, a whole name word can identify the sole unused contestant: `An` can resolve to `Thien An Nguyen` without storing an extra nickname. `--verbose` labels this match `inferred` and explains why. Ambiguous matches are never resolved by removing already-used contestants.
- Weaker names may show `possible ...; requires confirmation` in the diagnostics. These are suggestions only: `--yes` does not accept them. Review the original message, correct a separate file with canonical names, dry-run `draft import --file fixed.txt --participant PLAYER --instance INSTANCE`, then add `--yes`. Only add a quoted nickname via `contestant rename` when it is actually intended; contestant names are shared across seasons.
- The API still rejects any draft that is not every contestant exactly once.

## Weekly loop

Episode times come from the instance schedule (Season 51: CBS Wednesdays 8pm ET, episodes 1–13). Every write below is a dry run unless `--yes`, and repeating a `--yes` run is safe.

```sh
# Tribes: one line per tribe. The whole arrangement is replaced a minute after the episode starts,
# so starting tribes, swaps, merges, and splits are all the same command. Players left out lose their tribe.
# A color goes in parentheses (red, orange, yellow, green, blue, purple, pink, brown, black, white). A tribe
# that doesn't exist yet needs one, so a typo can't create a tribe; existing tribes keep their color unless one is given.
printf 'Savu: Adam, Kate, Mooney\nDina (pink): Kyle, Riley, Sarah\n' > tribes.txt
probst tribes set --file tribes.txt --episode 2 --instance INSTANCE --yes
probst tribes show --instance INSTANCE [--at "2026-10-07 21:00"]
probst tribes color Dina pink --instance INSTANCE [--yes]   # recolor an existing tribe; no swap needed
# The tribe swap post (messages/tribe-change.md): tribes at the swap, plus the episode's tribe-challenge points.
# --yes saves an unsent draft (key s51-ep2-tribe-swap); schedule it with `announcement schedule` after approval.
probst tribes announce --season ../../seasons/51.yaml --episode 2 [--kind swap|merge|assignment] [--name KEY] [--at "2026-10-07 21:00"]

# Challenges: every player on a winning tribe gets +2 (immunity) or +1 (reward).
probst challenge immunity Savu --episode 2 --instance INSTANCE --yes
probst challenge reward Savu Toka --episode 2 --instance INSTANCE --yes   # several winners
probst challenge reward Toka --episode 2 --key ep2-reward-2 --instance INSTANCE --yes   # second reward

# Inside Track (admin): list an episode's runs, then correct one. Dry run unless --yes; --cups N fills the lowest N cups
# unless --mask is given; balls used stays as it is unless --balls-used is given. Corrections are refused once the game resolves.
probst track plays --episode 5 --instance INSTANCE
probst track set Adam --episode 5 --cups 3 --instance INSTANCE [--balls-used 12] [--mask 5] [--yes]

# After each episode: pull survivoR (usually up 2-4 days after airing), review, then record.
probst episode sync --season 51 --episode 2 --instance INSTANCE          # preview; saves the raw data locally
probst episode sync --season 51 --episode 2 --instance INSTANCE --yes    # records boots + tribal immunity/reward

# Draft events: open once before the first import, close when drafts are due.
probst draft open --tribes Savu,Toka --instance INSTANCE --yes
probst draft watch THREAD_URL --instance INSTANCE --yes   # bot reads drafts posted there
probst draft import --file fixed.txt --participant Kate --instance INSTANCE --yes   # fix a problem draft for a player
probst draft reject Kate --instance INSTANCE --yes        # or drop it; they lose their spot
probst draft close --instance INSTANCE --yes

probst wordle open --episode 2 --instance INSTANCE
probst wordle import --file week2.txt --episode 2 --instance INSTANCE --yes   # before it closes
probst wordle import THREAD_URL --episode 2 --instance INSTANCE --yes        # or read text shares from a thread
probst wordle submit Kate 3 --episode 2 --instance INSTANCE             # by hand; X = failed (counts as 7)
probst wordle resolve --episode 2 --instance INSTANCE --yes             # after it closes

probst recap --episode 2 --instance INSTANCE > week2.md                 # review, then:
probst announcement save week-2-scores --file week2.md --instance INSTANCE --yes
probst announcement schedule week-2-scores --at "2026-10-07 12:00" --instance INSTANCE
```

- `wordle import --file` reads one `Player: 3` per line (`X` = failed; `#` comments and blank lines ignored; names as in `player list`). Bad lines, unknown names, and duplicates are shown as SKIP and not saved.
- `wordle import THREAD_URL` reads text Discord shares like `Wordle 1,561 3/6`, taking each linked player's first share posted while the Wordle is open. Players must be on a tribe when it closes. Submissions are rejected after noon ET on the next episode's air day, so import just before then.
- `episode sync` downloads survivoR's JSON for that episode into `~/.local/share/probst/survivor/US<season>/episode-NN.json` (castaways, boots, challenges, votes, advantages, journeys) and records each boot at its final place (21 = first out) plus +2/+1 for tribal immunity/reward wins. Individual challenges are shown but not scored. Re-running is safe; if survivoR is late, record by hand with `challenge` and the outcomes API.
- After `draft open`, each player's first saved draft is a stored event (order, tribe, bonus) in the same transaction as the picks. 1st gets +2, 2nd +1. Each gets a tribe drawn at random from the smallest tribes (never more than one apart) and a pinged Jeff post queued in the alias channel. Resubmitting only changes picks. `draft close` ends bonuses and posts a light rib for the last submitter; later drafts still get a tribe and post, but no bonus (and they miss earlier tribe points). Copy lives in `castaway-web/internal/httpapi/draft_submissions.go`.
- With `draft watch`, the bot reads every post and edit in the thread (and replays the thread on startup; each message version is handled once). Chat is ignored; a post naming most of the cast is a draft. A complete draft from a linked player — all castaways once, each name matched exactly or unambiguously (first name, last name, or nickname) — is saved. Anything else DMs the admin contact a link and the problems, every time, and the player hears nothing. A player's first draft post, even with problems, holds their submission order: fixing it with `draft import --participant` completes that spot; `draft reject` drops it and their next draft goes to the back of the line. Only complete drafts get the bonus, tribe, and post.
- `recap --episode N` prints the scores post as of now: new boots since the last recap in spoilers (`||name||`) with a line of flair, the biggest and smallest point gain since the last recap, and the leaderboard with tribes. It saves a snapshot to `~/.local/share/probst/scores/INSTANCE/episode-NN.json` that the next recap diffs against; the first recap counts gains from zero. Re-running overwrites that week's snapshot. The text is fixed once saved, so run it after the Wordle resolves and before scheduling.
- Podracing's requested scores timing is Wednesday **noon America/New_York**, except the first September 30, 2026 post at **5pm EDT**. The commands above are an operator-managed workflow, not an automatic recurring scheduler. No existing scores announcement or scheduler was found during the preview deployment, so the one-off/recurring timing is not yet installed; see [the scheduling plan](plans/weekly-score-announcements.md). The manual Wordle window remains 1pm on the episode's air day to noon on the next episode's, independently of score-post timing.
- `scores` (and the bot's `/castaway scores`) hide players who haven't drafted.
- Wordle scoring: the best individual result gets +2 (ties share). The tribe with the best average among its submitters gets +1 for every member. Players who don't submit aren't counted.
- Challenges count the tribes as they are during the episode (an hour after it starts). Record a swap with the episode it takes effect in; changes must be made in episode order.

## Validation

```sh
MISE_EXPERIMENTAL=1 mise run //apps/probst:ci
MISE_EXPERIMENTAL=1 mise run //apps/castaway-web:integration -- -run TestProbstChannelAndPlayerAdministration -count=1
```

The integration task uses a fresh disposable PostgreSQL container and invokes the built Probst binary against a local test API. It does not use production databases.

`TestDraftThreadKeelingOriginalMessage` and `TestDraftThreadMooneyOriginalMessage` exercise captured, unedited messages without contacting Discord. They verify all 21 ranks, pending submission credit, and replay safety; Mooney's weak matches require a corrected revision before any picks or rewards are saved. See [the matching plan](plans/contextual-draft-name-matching.md) and [fixture provenance](testdata/README.md).

See [requirements](functional-requirements.md), [security requirements](non-functional-requirements.md), [readiness](production-readiness-checklist.md), [administration plan](plans/player-administration.md), [private-endpoint proposal](plans/private-api-endpoint.md), and [changelog](CHANGELOG.md).

## Pronouns

`probst player list --json` includes each player's `pronouns` (`he/him`, `she/her`, `they/them`) when set. Use them only when writing Discord message copy about a player, never anywhere else; see the [`castaway-pronouns`](../../.agents/skills/castaway-pronouns/SKILL.md) skill.

## Season schedule (`probst season plan`)

`probst season plan seasons/51.yaml` prints the whole season as a dated timeline in Eastern time
(episodes, draft window, weekly game open/close, results due, scores posts, one-off posts). It then lists
how the file differs from the live instance (episode air times, unsent announcements not in the file).
It's read-only. `TBD` values are placeholders: they're listed with ⚠ and never acted on. Posts show
⚠ until `approved: true`. Unknown fields are rejected so typos surface. See `plans/season-schedule.md`
for the format and the `apply`/runner steps that follow.

`probst season apply FILE [--yes]` creates/reschedules the file's games (Press the Button; Castawordle still
needs `castawordle week` for its answer). `probst season post FILE --week N [--yes]` renders that week's
scores post from `seasons/NN-templates/scores.md` (all wording lives in that directory: `games/<type>.md` for each game's results, `reminder.md`, `next-game.md`, `lines/*.txt` for weekly variants) — leader, biggest gainer, biggest slider (wording rotates weekly),
top 3 + last place pinged, boots since last week's post, next game — and with `--yes` saves and schedules it
at the file's time. Re-run before then to refresh numbers.

`probst scores generate [--week N] [--no-ai]` drafts the week's scores post to `seasons/drafts/`: Probst renders
the numbers, order (league tiebreakers), mentions, boots and next game; `pi` (skill
`.agents/skills/castaway-scores-flavor`) writes only the intro/leader/gainer/slider lines, and any line missing
a required name or number falls back to the template. Edit the draft, then
`probst season post seasons/51.yaml --week N --body DRAFT --yes` saves and schedules it.

**Admin DMs.** `season post --yes` saves the post behind an approval gate: the bot DMs every instance admin the
exact text, and it posts at its scheduled time once one replies **yes** to that DM (editing the text re-asks).
When a draft post has problems, every admin gets a DM; reply to it with fixes, one per line (`7. Thien An`), and
the bot resubmits the corrected draft for the player.

## Season automation (`probst season reconcile`)

A Kubernetes CronJob (`deploy/base/probst/cronjob.yaml`) runs `probst season reconcile --instance ID` every 5 minutes.
It reads the season config synced into the database, not a file: after editing `seasons/NN.yaml` (or its
template directory), run `probst season sync seasons/NN.yaml` (dry run) then `--yes`. Sync refuses while the
live episode schedule differs from the file, and a concurrent sync loses rather than overwriting.
**Merge:** `probst season merge seasons/NN.yaml N` (dry run, then `--yes`) sets `merge_episode`, opens Pick Your
Champion (locks when Episode N+1 airs) and syncs. Commit the file change. Post the merge announcement yourself (it
must contain "Pick Your Champion" so replies to it count as picks).

`probst season reconcile FILE` still runs a file directly for local testing. Each successful scheduled run
stamps a heartbeat; if none lands for 30 minutes, admins get a DM (once per stall).
It only acts when the file has `automation: {enabled: true}` (plus `from_week` and the channel's `guild`; optional `approval_lead`, default `12h`), and
it never sends anything itself: posts go through the admin approval DM, everything else is a one-time admin
DM. Each run:

1. **Next game:** once a week's game opens, DMs the admins if next week's game isn't picked in the file or
   created on the server (`season check FILE` does just this step).
2. **Results:** from 3 hours after each aired episode, imports it from survivoR once it's complete. All
   tables come from one pinned survivoR commit. An episode is complete when survivoR lists it, its boot has
   a final place, and every challenge it describes has results; missing results are never read as "no
   challenge". The server applies boots and tribe immunity/reward wins in one transaction (or nothing),
   reusing matching hand-entered results (`epN-immunity`/`epN-reward`) and refusing anything that
   disagrees with what's recorded (admin DM). Medevacs/quits and several people leaving at once are
   **proposals**: the admins get a DM with exactly what will be recorded, and replying **yes** records it
   (the server runs the stored import; if survivoR changes first, the old yes is refused and a new DM goes
   out). Missing boots, unmatched names, non-tribal or tied challenges, and anything from
   `scoring.merge_episode` on are **holds**: record those by hand, then
   `probst episode import N --file FILE --resolve-holds --yes` imports the rest. Until survivoR is complete
   the job just keeps checking. If results are still missing at `weekly.results.due`, the admins get a DM.
3. **Scores post:** from `automation.approval_lead` before the week's post time (default `12h`: the 8pm post's
   approval DM goes out at 8am ET) once its episode is imported, it saves the standings snapshot and drafts the
   post (`seasons/NN-templates/scores.md`) for approval; once a game opens it also drafts the game-day reminder
   (`weekly.reminder.at`, no pings) for approval, pinging the top 3 and last place. If the game isn't scored
   yet, the draft shows a placeholder where its results go. The server never sends a placeholder: it holds the
   post, and when the game is scored the draft is redrafted with the results and re-asked. An admin-edited draft
   keeps the admin's words: the results replace its placeholder line. If the admin deleted that line, the results
   can't go in: the post stays held and the admins get an alert. At the post time an
   unscored game blocks the post (admins get a waiting DM). Every approval DM states the send time in Eastern.
   Late results are fine: up to 3 hours after 8pm the post is drafted and sends as soon as
   it's approved; after that, the admins get a DM instead. If scores change before it's sent, it can't be
   approved or sent with the old numbers: an untouched draft is redrafted and re-asked; an admin-edited one
   keeps the admin's text, re-asks, and the admins get an alert to check the numbers (that note is an alert, not part of the DM).

`probst episode import N --file FILE [--yes]` runs the same import by hand (`season import` and `episode sync` are deprecated) (dry run without `--yes`).
Kill switch: `kubectl -n castaway patch cronjob probst-season-check -p '{"spec":{"suspend":true}}'` or
`automation.enabled: false`. The job reuses the bot's service token (`CASTAWAY_API_AUTH_TOKEN`, by
`secretKeyRef`) acting as an admin via `PROBST_DISCORD_USER_ID`, and reaches castaway-web over in-cluster
HTTP only because `PROBST_ALLOW_HTTP_SERVER` names that exact URL. It runs `seasons/51.yaml` (Podracing)
since Oct 7.

**Daily reminder:** once a day from `automation.nudge_at` (ET, default `10:00`), the admins get one DM listing
every issue still open on that run (next game unset, results missing, holds, conflicts, a waiting post).
Fixed issues drop out; nothing open, no DM.

## Episode discussion threads

Set `weekly.discussion_thread: true` in a season file to open each episode's public discussion thread at its air time (the bot does it; see the castaway-discord-bot README). `probst discussion open --instance ID --season N --episode N --guild G --channel C` prints the plan; add `--yes` to open it now, which is the way to test a channel such as BrainLand. Opening a key that already exists changes nothing.
