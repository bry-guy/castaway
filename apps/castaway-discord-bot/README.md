# castaway-discord-bot

`castaway-discord-bot` is a standalone Discord bot that queries `castaway-web` and presents Survivor draft data through slash commands.

## Documentation

- Changelog: `CHANGELOG.md`
- Functional requirements: `functional-requirements.md`
- Non-functional requirements: `non-functional-requirements.md`
- Production readiness: `production-readiness-checklist.md`
- Shared app blueprint: `../../docs/castaway-discord-bot-blueprint.md`

## Commands

Top-level command: `/castaway`

### Active player commands

- `/castaway score [player]`
- `/castaway scores`
- `/castaway draft [player]`

`player` is an optional native Discord user selection and defaults to the invoker. All replies are ephemeral, include the season, and suppress mention notifications. DMs and retired command payloads are rejected. Scores show only public points; historical secret data is preserved but not rendered.

Instance context comes exclusively from API-owned guild/channel bindings, with parent-channel inheritance for threads. There is no personal/guild-default fallback. Operators use [Probst](../probst/README.md) for bindings and player links. Unbound/unmapped errors point to the configured contact (`CASTAWAY_ADMIN_CONTACT_DISCORD_USER_ID`, default verified @brain ID `235246238382030849`).

Registration and dispatch are limited to the explicit `DISCORD_TARGET_SERVER_IDS` allowlist. Commands are bulk-overwritten only in those guilds; global registrations are never changed. `DISCORD_TARGET_SEVER_ID` remains a single-guild fallback when the allowlist is unset.

### Deprecated query commands (not registered or dispatched)

The following legacy handlers, state, and tests remain for compatibility history only. Their options and private-score behavior are not available through the active player surface.

- `/castaway score participant:<name> [instance] [season]`
- `/castaway scores [instance] [season]`
- `/castaway draft participant:<name> [instance] [season]`
- `/castaway activities [instance] [season]`
- `/castaway activity activity:<name> [instance] [season]`
- `/castaway occurrences activity:<name> [instance] [season]`
- `/castaway occurrence activity:<name> occurrence:<name> [instance] [season]`
- `/castaway history participant:<name> [instance] [season]`
- `/castaway bids [instance]`
- `/castaway ponies [instance]`
- `/castaway link participant:<name> [instance] [season]`
- `/castaway unlink participant:<name> [instance] [season]`

`scores` uses the public weekly-score format: rank, tribe badge, real Discord mention when linked, total points, and public draft/bonus breakdown. `score` uses that same public format for public views, but linked self and admins viewing private score data get an ephemeral detailed breakdown including secret bonus points.

`history` now responds ephemerally. `draft` and `scores` also respond ephemerally so they stay out of the channel.

### Deprecated merge gameplay commands
- Stir the Pot
  - `/castaway pot status [instance]`
  - `/castaway pot show tribe:<tribe> [instance]` (admin-only current tribe total visibility)
  - `/castaway pot add points:<n> [player] [instance]` (player is admin-only; otherwise the caller must be linked and it defaults to them)
  - `/castaway pot start [instance]` (admin, targets the next scheduled episode)
  - `/castaway pot close [instance]` (admin, closes contributions and triggers Jeff's tribe-by-tribe results announcement)
- Individual Pony Auction
  - `/castaway auction status [instance]`
  - `/castaway auction start survivor:<contestant> [instance]` (admin, targets the next scheduled episode)
  - `/castaway auction stop survivor:<contestant> [instance]` (admin)
  - `/castaway auction award survivor:<contestant> [instance]` (admin, records individual immunity)
  - `/castaway bid survivor:<contestant> points:<n> [player] [instance]` (player is admin-only; otherwise the caller must be linked and it defaults to them)
- Loan Shark
  - `/castaway loan status [instance]`
  - `/castaway loan request points:<n> [instance]`
  - `/castaway loan repay points:<n> [instance]`

Player and admin write commands default to ephemeral responses.

When a hidden spend reveals one or more secret bonus points, the bot can also post a public announcement to a configured channel.

### Deprecated context commands
- `/castaway instances [season]`
- `/castaway instance set instance:<name> [season] [scope:me|guild]`
- `/castaway instance show`
- `/castaway instance unset [scope:me|guild]`

## Local development

Start and seed the full local stack from the repo root:

```bash
mise run start
mise run seed
```

That starts:
- the score-compose PostgreSQL (not used by the bot locally)
- `castaway-web`
- `castaway-discord-bot`

### Secret setup

This app uses the shared monorepo 1Password vault, `castaway`, through the root `fnox.toml`.

The app's `mise.toml` sets `FNOX_PROFILE=castaway-discord-bot`, so you should not need to inline secret env vars before `fnox exec` or `mise run`.

Secret env vars used by the bot:

- `CASTAWAY_DISCORD_BOT_TOKEN`
- `CASTAWAY_DISCORD_APPLICATION_ID`
- `DISCORD_TARGET_SEVER_ID` (legacy single-guild fallback)
- `CASTAWAY_DISCORD_PUBLIC_KEY` (loaded now for future Discord signature verification work; not currently consumed by the gateway bot)

Make sure `fnox` can access 1Password through `op` by doing one of the following:

- run `op signin`, or
- export `OP_SERVICE_ACCOUNT_TOKEN` / `FNOX_OP_SERVICE_ACCOUNT_TOKEN`

Validate access from the repo root:

```bash
fnox check -P castaway-discord-bot
fnox exec -P castaway-discord-bot -- env \
  | rg '^(CASTAWAY_DISCORD_BOT_TOKEN|CASTAWAY_DISCORD_APPLICATION_ID|DISCORD_TARGET_SEVER_ID|CASTAWAY_DISCORD_PUBLIC_KEY)=' \
  | sed 's/=.*$/=<redacted>/'
```

### Public config

Non-secret defaults are provided through `apps/castaway-discord-bot/mise.toml`:

- `CASTAWAY_API_BASE_URL=http://localhost:8080`
- `BOT_STATE_BACKEND=bolt`
- `BOT_STATE_PATH=./data/state.db`
- `LOG_LEVEL=INFO`

Optional production-oriented config:

- `DISCORD_TARGET_SERVER_IDS` is a comma-separated guild allowlist configured outside the secret store
- `CASTAWAY_API_AUTH_TOKEN` is required for the active channel-binding API
- `CASTAWAY_ADMIN_CONTACT_DISCORD_USER_ID` selects the setup contact displayed without notification
- `BOT_STATE_DATABASE_URL` when `BOT_STATE_BACKEND=postgres`
- `CASTAWAY_ANNOUNCEMENT_CHANNEL_ID` to publish public secret-point reveal messages (for example, `#survivor`); the bot uses real Discord mentions when it knows the linked user id

Override them in your shell only when you need a non-default local setup.

### Run the bot locally

The default local workflow is to let the root stack manage the bot lifecycle:

```bash
mise run start
mise run bot-logs
mise run stop
```

If you want to run only the bot service, use:

```bash
mise run bot
mise run bot-logs
```

If you want to run the process directly on the host instead of through Docker Compose, you can still use:

```bash
mise run //apps/castaway-discord-bot:run
```

Validate config and state wiring without connecting to Discord:

```bash
mise run //apps/castaway-discord-bot:check-config
```

If you are migrating saved defaults from BoltDB to PostgreSQL, set `BOT_STATE_BACKEND=postgres`, set `BOT_STATE_DATABASE_URL`, and run:

```bash
BOLT_STATE_IMPORT_PATH=./data/state.db mise run import-bolt-state
```

## Discord setup notes

- Set `DISCORD_TARGET_SERVER_IDS` to the explicit comma-separated guild allowlist; each guild receives only the three active player commands. The old `DISCORD_TARGET_SEVER_ID` setting remains a single-guild fallback.
- Invite the bot with both the `bot` and `applications.commands` scopes.
- The bot only needs guild slash command support for the MVP; it does not require privileged message content intent.

## App tasks

```bash
cd apps/castaway-discord-bot
mise run lint
mise run test
mise run build
mise run run
mise run check-config
mise run import-bolt-state
./bin/castaway-discord-bot --version
```

If the bot cannot resolve secrets, `mise run run` and `mise run check-config` will fail fast via `fnox check` before starting the process.

## Notes

- Guild/personal defaults are deprecated and ignored by active commands; channel bindings are administered through Probst.
- Production bot-to-API traffic can be authenticated with `CASTAWAY_API_AUTH_TOKEN` as a bearer token.
- The bot supports both `bolt` and `postgres` state backends. PostgreSQL is the intended production direction and should use its own logical database, `castaway_discord_bot`, with separate credentials from `castaway-web`.
- BoltDB remains a valid local and compatibility backend, and `import-bolt-state` provides an explicit migration path into PostgreSQL.

## Admin DM publish

An instance admin's free-form DM (anything that isn't a reply to one of the bot's prompts) is a request to post its text. The bot echoes the exact text, lists the season channels that admin administers, and asks for a name: reply with one name, several separated by `/` (`Podracing/BrainLand`), a channel ID when names repeat, or `no` to cancel. Each chosen channel gets the text once, with no pings and no link previews. Requests expire after 24 hours. Non-admins get no reply.

## Episode discussion threads

When an episode's discussion thread is queued (probst `weekly.discussion_thread`), the bot opens a public thread with no opener, removes Discord's "started a thread" message, and posts just the thread link (`<#thread>`) in the season channel. It checks View Channel, Send Messages, Create Public Threads and Manage Messages first; a missing one is DM'd to the admins and nothing opens.

