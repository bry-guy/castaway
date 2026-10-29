# Changelog

All notable changes to `castaway-discord-bot` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- An instance admin's free-form DM is a publish request: the bot echoes the exact text and lists the season channels the admin administers. Reply with a server name (case-insensitive, or a channel ID when names repeat), several separated by `/`, or `no` to cancel. Unknown names keep the request open and list the options. Non-admins get no reply.
- Episode discussion threads: the bot opens a queued thread as a public thread with no opener, deletes the "started a thread" system message, and posts `Survivor S<season> E<N> Discussion: <#thread>` in the season channel. Missing Create Public Threads, Manage Messages, View Channel, or Send Messages is reported to admins before any thread is opened.

- Approval DMs state the send time in Eastern, up front and bold (`posts Wed Oct 21 8:00pm ET`), on the first ask, re-asks, and "missed" DMs; "approved" replies use the same format.

- Announcements and admin DMs post with link previews suppressed.

- Every user-facing Discord message (DMs, replies, channel posts, score/leaderboard formatting, player-visible errors) now comes from embedded templates in `internal/messages/templates/`; output is unchanged.

- Pick Your Champion: `probst season merge FILE N` ends Tribe scoring at merge Episode N and opens picks (website `/games/champion`, or reply to the bot's "Pick Your Champion" post) until Episode N+1 airs; then picks are fixed and shown to all. Post-merge challenges import from survivoR and award pickers of the winners (+3 immunity, +1 reward). The merge episode's own challenges are held for an admin.

- A held post whose time passes unapproved gets one "⏰ Missed" DM; replying **yes** posts it immediately (late approvals now allowed for 7 days, was 3 hours).

- Episode imports that need judgment (medevac/quit, two people leaving) are DMed to admins as an exact proposal; replying **yes** records it (`POST /admin-alerts/:id/approve`, revision-bound). `probst episode import N --file F` replaces `season import`/`episode sync`.

- Posts threaded announcements, opening the thread from a starter message on first use and reopening it if deleted.

- `/castaway scores` and `score` hide players who have not drafted.
### Changed
- Restrict `/castaway` to `score [player]`, `scores`, and `draft [player]`, using API channel bindings, ephemeral public-only replies, and explicit BrainLand/Podracing guild registration.
- Restrict registration and dispatch to the configured multi-guild allowlist without clearing globals or falling back to global registration.
- Render `/castaway score` and `/castaway scores` with total points plus draft and visible bonus breakdowns from the leaderboard API.

## [0.1.0] - 2026-03-06

### Added
- Reads watched draft threads (needs the Message Content intent), forwards posts to the API, and DMs the admin contact about each problem draft.
- Standalone Discord bot app with slash commands for scores, drafts, and instance context.
- Local stack integration through Docker Compose and root `mise run start` / `stop`.
- fnox + 1Password integration for Discord secrets.
