import "@typespec/http";
import "@typespec/openapi3";

using TypeSpec.Http;

@service(#{ title: "Castaway Web API" })
@server("http://localhost:8080", "Local development")
namespace CastawayWeb;

model ErrorResponse {
  error: string;
}

model HealthResponse {
  status: string;
}

model CastawordleGame {
  id: string;
  instance_id: string;
  name: string;
  word_length: int32;
  guess_limit: int32;
  opens_at: utcDateTime;
  cutoff_at: utcDateTime;
  unscored: boolean;
  test: boolean;
  episode_number?: int32;
}

model CastawordlePlay {
  game: CastawordleGame;
  guesses: { word: string; feedback: string[]; }[];
  status: string;
  answer?: string;
}

@route("/instances/{instanceID}/castawordle")
@post
op createCastawordle(
  @path instanceID: string,
  @body body: { name: string; answer: string; episode_number?: int32 | null; opens_at?: utcDateTime; cutoff_at?: utcDateTime; scored?: boolean; window?: { opens_at: utcDateTime; cutoff_at: utcDateTime; }; },
): { @statusCode statusCode: 201; ...CastawordleGame; } | { @statusCode statusCode: 200; ...CastawordleGame; } | ErrorResponse;

@route("/castawordle/{gameID}/resolve")
@post
op resolveCastawordle(@path gameID: string): WordleRoundResolution | ErrorResponse;

model ButtonGame {
  id: string;
  episode_number: int32 | null;
  opens_at: utcDateTime;
  cutoff_at: utcDateTime;
  resolved: boolean;
}

@route("/instances/{instanceID}/button-games")
@post
op createButtonGame(
  @path instanceID: string,
  @body body: { episode_number?: int32; opens_at: utcDateTime; cutoff_at: utcDateTime; },
): ButtonGame | ErrorResponse;

@route("/button-games/{gameID}/resolve")
@post
op resolveButtonGame(@path gameID: string): { awards: Record<int32>; } | ErrorResponse;

@route("/instances/{instanceID}/scramble-games")
@post
op createScrambleGame(
  @path instanceID: string,
  @body body: { episode_number?: int32; phrase: string; decoys?: int32; decoy_letters?: string; opens_at: utcDateTime; cutoff_at: utcDateTime; },
): ButtonGame | ErrorResponse;

@route("/scramble-games/{gameID}/resolve")
@post
op resolveScrambleGame(@path gameID: string): { awards: Record<int32>; } | ErrorResponse;

@route("/instances/{instanceID}/track-games")
@post
op createTrackGame(
  @path instanceID: string,
  @body body: { episode_number: int32; opens_at: utcDateTime; cutoff_at: utcDateTime; },
): ButtonGame | ErrorResponse;

@route("/track-games/{gameID}/resolve")
@post
op resolveTrackGame(@path gameID: string): { awards: Record<int32>; } | ErrorResponse;

@route("/instances/{instanceID}/free-the-block-games")
@post
op createFreeTheBlockGame(
  @path instanceID: string,
  @body body: { episode_number: int32; opens_at: utcDateTime; cutoff_at: utcDateTime; },
): ButtonGame | ErrorResponse;

@route("/free-the-block-games/{gameID}/resolve")
@post
op resolveFreeTheBlockGame(@path gameID: string): { awards: Record<int32>; } | ErrorResponse;

model TrackPlay {
  participant_id: string;
  name: string;
  cups: int32;
  balls_used: int32;
  finished: boolean;
  score: int32;
  place: int32;
  points: int32;
}

/** Admin: every run in an episode's Inside Track game, best place first (works before the game resolves). */
@route("/instances/{instanceID}/track-games/{episode}/plays")
@get
op listTrackPlays(@path instanceID: string, @path episode: int32): { game_id: string; episode_number: int32; open: boolean; resolved: boolean; plays: TrackPlay[]; } | ErrorResponse;

/** Admin correction of one player's run; may move backwards; 409 once the game resolves. Writes only that run. */
@route("/instances/{instanceID}/track-games/{episode}/plays/{participant}")
@put
op setTrackPlay(@path instanceID: string, @path episode: int32, @path participant: string, @body body: { cups_mask: int32; balls_used: int32; finished: boolean; }): { score: int32; } | ErrorResponse;

@route("/announcements/approvals/claim")
@post
op claimAnnouncementApproval(@body body: { guild_ids: string[]; }): { announcement: unknown | null; send_at?: utcDateTime; revision?: string; missed?: boolean; admin_discord_user_ids?: string[]; } | ErrorResponse;

@route("/announcements/{announcementID}/approve")
@post
op approveAnnouncement(@path announcementID: string, @body body: { admin_discord_user_id: string; revision: string; }): { scheduled_at: utcDateTime; } | ErrorResponse;

@route("/announcements/{announcementID}/revise")
@post
op reviseAnnouncement(@path announcementID: string, @body body: { admin_discord_user_id: string; revision: string; body: string; }): { ok: boolean; } | ErrorResponse;

@route("/instances/{instanceID}/admin-alerts/{alertKey}")
@put
op raiseAdminAlert(@path instanceID: string, @path alertKey: string, @body body: { body: string; action?: { kind: "episode_import"; payload: Record<unknown>; }; }): { created: boolean; } | ErrorResponse;

@route("/instances/{instanceID}/scheduled-games")
@get
op listScheduledGames(@path instanceID: string): { games: { type: string; id: string; episode_number: int32; opens_at: utcDateTime; cutoff_at: utcDateTime; scored: boolean; }[]; } | ErrorResponse;

model EpisodeImportBoot { position: int32; contestant_id: string; }
model EpisodeImportChallenge { key: string; legacy_keys?: string[]; kind: string; winning_tribes: string[]; effective_at: utcDateTime; }

@route("/instances/{instanceID}/episode-imports")
@post
op importEpisode(@path instanceID: string, @body body: { episode_number: int32; source: string; source_revision?: string; boots: EpisodeImportBoot[]; challenges: EpisodeImportChallenge[]; champion_challenges?: { key: string; kind: string; winners: string[]; effective_at: utcDateTime; }[]; }): { status: string; outcomes_recorded?: int32; challenge_awards?: int32; } | ErrorResponse;

@route("/instances/{instanceID}/episode-imports")
@get
op listEpisodeImports(@path instanceID: string): { imports: { episode_number: int32; source: string; source_revision: string; boot_positions: int32[]; challenge_keys: string[]; applied_at: utcDateTime; }[]; } | ErrorResponse;

model SeasonConfig { season_yaml: string; scores_template: string; revision: string; updated_at: utcDateTime; updated_by: string; }

@route("/instances/{instanceID}/season-config")
@get
op getSeasonConfig(@path instanceID: string): SeasonConfig | ErrorResponse;

@route("/instances/{instanceID}/season-config")
@put
op putSeasonConfig(@path instanceID: string, @body body: { season_yaml: string; scores_template: string; base_revision?: string; }): { revision: string; } | ErrorResponse;

@route("/instances/{instanceID}/automation-heartbeat")
@put
op automationHeartbeat(@path instanceID: string): { ok: boolean; } | ErrorResponse;

model ChampionRound { merge_episode: int32; opens_at: utcDateTime; locks_at: utcDateTime; }

@route("/instances/{instanceID}/champion-round")
@put
op putChampionRound(@path instanceID: string, @body body: ChampionRound): ChampionRound | ErrorResponse;

@route("/instances/{instanceID}/champion-round")
@get
op getChampionRound(@path instanceID: string): { round: ChampionRound; picks?: { player: string; contestant: string; }[]; } | ErrorResponse;

@route("/discord/guilds/{guildID}/channels/{channelID}/champion-pick")
@put
op putChampionPick(@path guildID: string, @path channelID: string, @body body: { discord_user_id: string; contestant: string; }): { contestant: string; } | ErrorResponse;

model ScoreSnapshot { week: int32; fingerprint: string; leaderboard: unknown[]; taken_at: utcDateTime; frozen: boolean; }

@route("/instances/{instanceID}/score-snapshots/{week}")
@put
op takeScoreSnapshot(@path instanceID: string, @path week: int32, @body body: { request_key: string; }): ScoreSnapshot | ErrorResponse;

@route("/instances/{instanceID}/score-snapshots/{week}")
@get
op getScoreSnapshot(@path instanceID: string, @path week: int32): ScoreSnapshot | ErrorResponse;

@route("/instances/{instanceID}/score-posts/{requestKey}")
@put
op upsertScorePost(@path instanceID: string, @path requestKey: string, @body body: { guild_id: string; channel_id: string; body: string; send_at: utcDateTime; score_fingerprint?: string; notify_users?: boolean; awaiting_results?: boolean; game_results?: string; results_placeholder?: string; }): { action: string; } | ErrorResponse;

/** Make the season's episode schedule match a season file: listed episodes are created or moved. */
@route("/instances/{instanceID}/episodes")
@put
op putEpisodes(@path instanceID: string, @body body: { episodes: { episode_number: int32; label?: string; airs_at: utcDateTime; }[]; }): { changed: int32; } | ErrorResponse;

/** A scored episode game's results for the weekly scores post (409 until it's scored; never includes answers). */
@route("/instances/{instanceID}/game-results/{episode}")
@get
op getGameResults(@path instanceID: string, @path episode: int32): { type: string; players: Record<unknown>[]; tribes: { name: string; emoji: string; value: float64; }[]; } | ErrorResponse;

@route("/admin-alerts/claim")
@post
op claimAdminAlert(@body body: { guild_ids: string[]; }): { alert: { id: string; body: string; revision?: string | null; } | null; admin_discord_user_ids?: string[]; } | ErrorResponse;

@route("/admin-alerts/{alertID}/approve")
@post
op approveAdminAction(@path alertID: string, @body body: { revision: string; }): Record<unknown> | ErrorResponse;

@route("/admin-alerts/{alertID}/delivered")
@post
op confirmAdminAlert(@path alertID: string): { ok: boolean; } | ErrorResponse;

@route("/announcements/{announcementID}/approval-delivered")
@post
op confirmApprovalDelivery(@path announcementID: string, @body body: { revision: string; }): { ok: boolean; } | ErrorResponse;

@route("/instances/{instanceID}/announcements/{announcementID}/approval")
@put
op requestAnnouncementApproval(@path instanceID: string, @path announcementID: string, @body body: { send_at: utcDateTime; }): unknown | ErrorResponse;

@route("/castawordle/{gameID}/private")
@post
op createPrivateCastawordle(
  @path gameID: string,
  @body body: { participant_id: string; name: string; answer: string; },
): { @statusCode statusCode: 201; id: string; word_length: int32; } | ErrorResponse;

@route("/castawordle/{gameID}/play")
@get
op getCastawordlePlay(@path gameID: string): CastawordlePlay | ErrorResponse;

@route("/castawordle/{gameID}/play/guesses")
@post
op guessCastawordle(@path gameID: string, @body body: { guess: string; position: int32; }): CastawordlePlay | ErrorResponse;

model Instance {
  id: string;
  name: string;
  season: int32;
  created_at: utcDateTime;
  current_episode?: InstanceEpisodeBrief;
}

model InstanceEpisodeBrief {
  id: string;
  episode_number: int32;
  label: string;
  airs_at: utcDateTime;
}

model Contestant {
  id: string;
  name: string;
}

model Participant {
  id: string;
  name: string;
  discord_user_id?: string;
  /** Admin-only; used solely for Probst message copy. */
  pronouns?: "he/him" | "she/her" | "they/them";
}

model DraftPick {
  position: int32;
  contestant_id: string;
  contestant_name: string;
}

model Outcome {
  position: int32;
  contestant_id?: string;
  contestant_name?: string;
}

model LeaderboardRow {
  participant_id: string;
  participant_name: string;
  participant_discord_user_id?: string;
  current_tribe_name?: string;
  /** The tribe's buff color (purple, yellow, ...) and its emoji, for posts. */
  current_tribe_color?: string;
  current_tribe_emoji?: string;
  score: int32;
  draft_points: int32;
  bonus_points: int32;
  total_points: int32;
  points_available: int32;
}

model BonusLedgerEntry {
  id: string;
  activity_id: string;
  activity_type: string;
  activity_name: string;
  activity_occurrence_id: string;
  occurrence_type: string;
  occurrence_name: string;
  source_group_id?: string;
  source_group_name?: string;
  entry_kind: string;
  points: int32;
  visibility: string;
  reason: string;
  effective_at: utcDateTime;
  award_key?: string;
  created_at: utcDateTime;
}

alias JsonObject = Record<unknown>;

model ParticipantBonusLedgerResponse {
  participant: Participant;
  bonus_points: int32;
  ledger: BonusLedgerEntry[];
}

// --- Activities & Occurrences ---

model Activity {
  id: string;
  instance_id: string;
  activity_type: string;
  name: string;
  status: string;
  starts_at: utcDateTime;
  ends_at?: utcDateTime;
  metadata: JsonObject;
  created_at: utcDateTime;
  updated_at: utcDateTime;
}

model Occurrence {
  id: string;
  activity_id: string;
  occurrence_type: string;
  name: string;
  effective_at: utcDateTime;
  starts_at?: utcDateTime;
  ends_at?: utcDateTime;
  status: string;
  source_ref?: string;
  metadata: JsonObject;
  created_at: utcDateTime;
  updated_at: utcDateTime;
}

model OccurrenceParticipantResult {
  id: int64;
  activity_occurrence_id: string;
  participant_id: string;
  participant_group_id?: string;
  role: string;
  result: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model OccurrenceGroupResult {
  id: int64;
  activity_occurrence_id: string;
  participant_group_id: string;
  role: string;
  result: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model ResolveLedgerEntry {
  id: string;
  instance_id: string;
  participant_id: string;
  activity_occurrence_id: string;
  source_group_id?: string;
  entry_kind: string;
  points: int32;
  visibility: string;
  reason: string;
  effective_at: utcDateTime;
  award_key?: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model ListInstancesResponse {
  instances: Instance[];
}

model CreateInstanceEpisodeRequest {
  episode_number: int32;
  label: string;
  airs_at: utcDateTime;
}

model CreateInstanceRequest {
  name: string;
  season: int32;
  contestants?: string[];
  managed_progression?: boolean;
  episodes?: CreateInstanceEpisodeRequest[];
}

model CreateInstanceResponse {
  instance: Instance;
}

model GetInstanceResponse {
  instance: Instance;
  episodes: InstanceEpisodeBrief[];
  contestants: Contestant[];
  participants: Participant[];
}

model CreateContestantRequest {
  name: string;
}

model CreateContestantResponse {
  contestant: Contestant;
}

model ListContestantsResponse {
  contestants: Contestant[];
}

model CreateParticipantRequest {
  name: string;
}

model CreateParticipantResponse {
  participant: Participant;
}

model ListParticipantsResponse {
  participants: Participant[];
}

model ReplaceDraftRequest {
  contestant_ids: string[];
  idempotency_key?: string;
  effective_at?: utcDateTime;
  reason?: string;
}

model ReplaceDraftResponse {
  status: string;
}

model GetDraftResponse {
  participant: Participant;
  picks: DraftPick[];
}

model UpsertOutcomeRequest {
  contestant_id?: string;
  idempotency_key?: string;
  effective_at?: utcDateTime;
  reason?: string;
  @doc("For managed instances, set true to publish this as a correction to the current public score without including later pending results. The target position must already be published. Without it, writes during an active episode remain pending and corrections after scoring are rejected. Conflicting pending changes are rejected.")
  correction?: boolean;
}

model ProgressionCommandRequest {
  idempotency_key: string;
  effective_at: utcDateTime;
}

model UpsertOutcomeResponse {
  outcome: {
    position: int32;
    contestant_id?: string;
  };
}

model ListOutcomesResponse {
  outcomes: Outcome[];
}

model LeaderboardResponse {
  leaderboard: LeaderboardRow[];
}

model ImportSubmission {
  participant_name: string;
  rankings: string[];
}

model ImportInstanceRequest {
  season: int32;
  name?: string;
  submissions: ImportSubmission[];
}

model StartStirThePotRoundRequest {
  name?: string;
}

model AddStirThePotContributionRequest {
  participant_id?: string;
  points: int32;
}

model StartAuctionLotRequest {
  contestant_id: string;
}

model SetAuctionBidRequest {
  participant_id?: string;
  points: int32;
}

model LoanSharkRequest {
  points: int32;
}

model RecordIndividualPonyImmunityRequest {
  contestant_id: string;
  effective_at?: utcDateTime;
}

model MergeAuctionResultRow {
  round: int32;
  contestant: string;
  winner: string;
  price: int32;
}

model RecordMergeAuctionRequest {
  name?: string;
  mode?: string;
  raw_csv?: string;
  results: MergeAuctionResultRow[];
}

model FinaleBingoScoreInput {
  participant_id: string;
  box_points: int32;
  bingo_count: int32;
  notes?: string;
}

model FinaleBingoLoanSharkInput {
  shark_participant_id: string;
  target_participant_id: string;
}

model RecordFinaleBingoLoanSharksRequest {
  name?: string;
  assignments: FinaleBingoLoanSharkInput[];
  effective_at?: utcDateTime;
}

model RecordFinaleBingoScoresRequest {
  name?: string;
  scores: FinaleBingoScoreInput[];
  loan_sharks?: FinaleBingoLoanSharkInput[];
  effective_at?: utcDateTime;
}

// --- Activities & Occurrences request/response models ---

model ListActivitiesResponse {
  activities: Activity[];
}

model CreateActivityRequest {
  activity_type: string;
  name: string;
  status: string;
  starts_at: utcDateTime;
  ends_at?: utcDateTime;
  metadata?: JsonObject;
}

model CreateActivityResponse {
  activity: Activity;
}

model ListOccurrencesResponse {
  occurrences: Occurrence[];
}

model CreateOccurrenceRequest {
  occurrence_type: string;
  name: string;
  effective_at: utcDateTime;
  starts_at?: utcDateTime;
  ends_at?: utcDateTime;
  status: string;
  source_ref?: string;
  metadata?: JsonObject;
}

model CreateOccurrenceResponse {
  occurrence: Occurrence;
}

model CreateOccurrenceParticipantRequest {
  participant_id: string;
  participant_group_id?: string;
  role: string;
  result?: string;
  metadata?: JsonObject;
}

model CreateOccurrenceParticipantResponse {
  participant_result: OccurrenceParticipantResult;
}

model CreateOccurrenceGroupRequest {
  participant_group_id: string;
  role: string;
  result?: string;
  metadata?: JsonObject;
}

model CreateOccurrenceGroupResponse {
  group_result: OccurrenceGroupResult;
}

model ResolveOccurrenceResponse {
  created_entries: ResolveLedgerEntry[];
  created_count: int32;
}

model WordleRoundResolution {
  created_entries: ResolveLedgerEntry[];
  created_count: int32;
}

model WordleRound {
  id: string;
  instance_id: string;
  activity_id: string;
  activity_type: string;
  occurrence_type: string;
  status: string;
  name: string;
  @minLength(1)
  @maxLength(64)
  round_key: string;
  effective_at: utcDateTime;
  opens_at: utcDateTime;
  cutoff_at: utcDateTime;
  closed_at: utcDateTime | null;
  resolved: boolean;
  resolution: WordleRoundResolution | null;
  created_at: utcDateTime;
  updated_at: utcDateTime;
}

model WordleRoundParticipant {
  id: int64;
  participant_id: string;
  participant_name: string;
  participant_group_id?: string;
  participant_group_name?: string;
  role: string;
  result: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model TribeMember {
  participant_id: string;
  name: string;
}

model Tribe {
  id: string;
  name: string;
  /** Buff color (red, orange, yellow, green, blue, purple, pink, brown, black, white); absent if never set. */
  color?: string;
  emoji?: string;
  members: TribeMember[];
}

model TribesResponse {
  at: utcDateTime;
  tribes: Tribe[];
}

model TribeAssignment {
  name: string;
  /** Required for a tribe that doesn't exist yet; sets the color of an existing one. */
  color?: string;
  participant_ids: string[];
}

model SetTribesRequest {
  effective_at: utcDateTime;
  tribes: TribeAssignment[];
}

model RecordTribeChallengeRequest {
  @minLength(1)
  @maxLength(64)
  key: string;
  kind: "immunity" | "reward";
  winning_tribes: string[];
  effective_at: utcDateTime;
}

model RecordTribeChallengeResponse {
  occurrence_id: string;
  awarded_count: int32;
}

model CreateWordleRoundRequest {
  @minLength(1)
  @maxLength(64)
  round_key: string;
  name: string;
  opens_at: utcDateTime;
  cutoff_at: utcDateTime;
}

model CreateWordleRoundResponse {
  round: WordleRound;
}

model GetWordleRoundResponse {
  round: WordleRound;
  participants: WordleRoundParticipant[];
}

model PutWordleParticipantRequest {
  participant_group_id: string;
  @minValue(1)
  guess_count: int32;
}

model PutWordleParticipantResponse {
  participant_result: OccurrenceParticipantResult;
}

model CloseWordleRoundResponse {
  round: WordleRound;
}

model ActivityGroupAssignment {
  id: int64;
  activity_id: string;
  participant_group_id: string;
  participant_group_name: string;
  role: string;
  starts_at: utcDateTime;
  ends_at?: utcDateTime;
  configuration: JsonObject;
  created_at: utcDateTime;
}

model ActivityParticipantAssignment {
  id: int64;
  activity_id: string;
  participant_id: string;
  participant_name: string;
  participant_group_id?: string;
  participant_group_name?: string;
  role: string;
  starts_at: utcDateTime;
  ends_at?: utcDateTime;
  configuration: JsonObject;
  created_at: utcDateTime;
}

model OccurrenceParticipantDetail {
  id: int64;
  activity_occurrence_id: string;
  participant_id: string;
  participant_name: string;
  participant_group_id?: string;
  participant_group_name?: string;
  role: string;
  result: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model OccurrenceGroupDetail {
  id: int64;
  activity_occurrence_id: string;
  participant_group_id: string;
  participant_group_name: string;
  role: string;
  result: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model OccurrenceLedgerEntry {
  id: string;
  instance_id: string;
  participant_id: string;
  participant_name: string;
  activity_occurrence_id: string;
  occurrence_type: string;
  occurrence_name: string;
  activity_id: string;
  activity_type: string;
  activity_name: string;
  source_group_id?: string;
  source_group_name?: string;
  entry_kind: string;
  points: int32;
  visibility: string;
  reason: string;
  effective_at: utcDateTime;
  award_key?: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model GetActivityResponse {
  activity: Activity;
  group_assignments: ActivityGroupAssignment[];
  participant_assignments: ActivityParticipantAssignment[];
}

model GetOccurrenceResponse {
  activity: Activity;
  occurrence: Occurrence;
  participants: OccurrenceParticipantDetail[];
  groups: OccurrenceGroupDetail[];
  ledger: OccurrenceLedgerEntry[];
}

model ParticipantOccurrenceInvolvement {
  id: int64;
  activity_occurrence_id: string;
  participant_id: string;
  participant_group_id?: string;
  participant_group_name?: string;
  role: string;
  result: string;
  metadata: JsonObject;
  created_at: utcDateTime;
}

model ParticipantActivityHistoryOccurrence {
  occurrence: Occurrence;
  involvement?: ParticipantOccurrenceInvolvement;
  ledger: OccurrenceLedgerEntry[];
}

model ParticipantActivityHistoryActivity {
  activity: Activity;
  occurrences: ParticipantActivityHistoryOccurrence[];
}

model ParticipantActivityHistoryResponse {
  participant: Participant;
  instance: Instance;
  activities: ParticipantActivityHistoryActivity[];
}

model AdminSessionResponse {
  principal: string;
  discord_user_id: string;
  authentication: string;
}

model BootstrapAdminResponse {
  instance_id: string;
  discord_user_id: string;
  admin: boolean;
}

model DiscordChannelBinding {
  guild_id: string;
  channel_id: string;
  instance_id: string;
}

model DiscordChannelBindingResponse {
  binding: DiscordChannelBinding;
}

model SetDiscordChannelBindingRequest {
  instance_id: string;
  replace?: boolean;
}

model Announcement {
  id: string;
  instance_id: string;
  guild_id: string;
  channel_id: string;
  request_key: string;
  body: string;
  scheduled_at: utcDateTime | null;
  due_at: utcDateTime;
  status: string;
  message_id: string | null;
  notify_users: boolean;
  /** Post inside this shared bot thread; `id` is absent until the thread exists. */
  thread?: AnnouncementThread;
}

model AnnouncementThread {
  key: string;
  name: string;
  starter: string;
  id?: string;
}

model CreateAnnouncementRequest {
  guild_id: string;
  channel_id: string;
  request_key: string;
  body: string;
  scheduled_at?: utcDateTime;
  /** Save without sending; schedule it later. Cannot be combined with scheduled_at. */
  draft?: boolean;
  /** Let <@user> mentions notify (never @everyone or roles). Defaults to false. */
  notify_users?: boolean;
}

model AnnouncementResponse { announcement: Announcement; }
model AnnouncementListResponse { announcements: Announcement[]; }
model ClaimAnnouncementRequest { guild_ids: string[]; }
model ClaimAnnouncementResponse { announcement: Announcement | null; }
model FinishAnnouncementRequest { message_id?: string; failed?: boolean; thread_id?: string; }
model FinishAnnouncementResponse { status: string; message_id?: string; }

@route("/admin/session")
@get
op adminSession(): AdminSessionResponse;

@route("/instances/{instanceID}/admins/bootstrap")
@post
op bootstrapInstanceAdmin(@path instanceID: string): BootstrapAdminResponse;

model AddInstanceAdminRequest { discord_user_id: string; }
model AddInstanceAdminResponse { instance_id: string; discord_user_id: string; }

@route("/instances/{instanceID}/admins")
@post
op addInstanceAdmin(@path instanceID: string, @body body: AddInstanceAdminRequest): AddInstanceAdminResponse;

model AccessRequest { discord_user_id: string; discord_username: string; requested_at: utcDateTime; }
model ClaimAccessRequestResponse { access_request: AccessRequest | null; }
model ListAccessRequestsResponse { access_requests: AccessRequest[]; }

@route("/access-requests/claim")
@post
op claimAccessRequest(): ClaimAccessRequestResponse;

@route("/access-requests")
@get
op listAccessRequests(): ListAccessRequestsResponse;

@route("/access-requests/{discordUserID}")
@delete
op deleteAccessRequest(@path discordUserID: string): void;

@route("/discord/guilds/{guildID}/channels/{channelID}")
@get
op getDiscordChannelBinding(@path guildID: string, @path channelID: string): DiscordChannelBindingResponse;

@route("/discord/guilds/{guildID}/channels/{channelID}")
@put
op setDiscordChannelBinding(@path guildID: string, @path channelID: string, @body body: SetDiscordChannelBindingRequest): DiscordChannelBindingResponse;

@route("/discord/guilds/{guildID}/channels/{channelID}")
@delete
op deleteDiscordChannelBinding(@path guildID: string, @path channelID: string): { unbound: boolean; };

@route("/instances/{instanceID}/announcements")
@post
op createAnnouncement(@path instanceID: string, @body body: CreateAnnouncementRequest): AnnouncementResponse;

@route("/instances/{instanceID}/announcements")
@get
op listAnnouncements(@path instanceID: string): AnnouncementListResponse;

/** Omit scheduled_at to send now. The bot reads the current text when it sends. */
model ScheduleAnnouncementRequest {
  scheduled_at?: utcDateTime;
}

model EditAnnouncementRequest {
  body: string;
}

@route("/instances/{instanceID}/announcements/{announcementID}/body")
@put
op editAnnouncement(
  @path instanceID: string,
  @path announcementID: string,
  @body body: EditAnnouncementRequest,
): AnnouncementResponse | ErrorResponse;

@route("/instances/{instanceID}/announcements/{announcementID}/schedule")
@put
op scheduleAnnouncement(
  @path instanceID: string,
  @path announcementID: string,
  @body body: ScheduleAnnouncementRequest,
): AnnouncementResponse | ErrorResponse;

/** Turn a scheduled announcement back into a draft. */
@route("/instances/{instanceID}/announcements/{announcementID}/schedule")
@delete
op unscheduleAnnouncement(@path instanceID: string, @path announcementID: string): AnnouncementResponse | ErrorResponse;

/** Record an unsent announcement as already posted (by hand) so the bot never sends it. */
@route("/instances/{instanceID}/announcements/{announcementID}/sent")
@post
op markAnnouncementSent(
  @path instanceID: string,
  @path announcementID: string,
  @body body: { message_id?: string },
): AnnouncementResponse | ErrorResponse;

@route("/instances/{instanceID}/announcements/{announcementID}")
@delete
op deleteAnnouncement(@path instanceID: string, @path announcementID: string): { deleted: string } | ErrorResponse;

@route("/announcements/claim")
@post
op claimAnnouncement(@body body: ClaimAnnouncementRequest): ClaimAnnouncementResponse;

@route("/announcements/{announcementID}/finish")
@post
op finishAnnouncement(@path announcementID: string, @body body: FinishAnnouncementRequest): FinishAnnouncementResponse;

@route("/healthz")
@get
op healthz(): HealthResponse;

@route("/metrics")
@get
op metrics(): string;

@route("/instances")
@get
op listInstances(@query season?: int32, @query name?: string): ListInstancesResponse;

@route("/instances")
@post
op createInstance(@body body: CreateInstanceRequest): {
  @statusCode statusCode: 201;
  ...CreateInstanceResponse;
};

@route("/instances/import")
@post
op importInstance(@body body: ImportInstanceRequest): {
  @statusCode statusCode: 201;
  ...CreateInstanceResponse;
} | ErrorResponse;

@route("/instances/{instanceID}")
@get
op getInstance(@path instanceID: string): GetInstanceResponse | ErrorResponse;

model RenameContestantRequest { name: string; }

@route("/instances/{instanceID}/contestants/{contestantID}")
@patch
op renameContestant(
  @path instanceID: string,
  @path contestantID: string,
  @body body: RenameContestantRequest,
): CreateContestantResponse | ErrorResponse;

@route("/instances/{instanceID}/contestants")
@post
op createContestant(
  @path instanceID: string,
  @body body: CreateContestantRequest,
): {
  @statusCode statusCode: 201;
  ...CreateContestantResponse;
} | ErrorResponse;

@route("/instances/{instanceID}/contestants")
@get
op listContestants(@path instanceID: string): ListContestantsResponse | ErrorResponse;

@route("/instances/{instanceID}/participants")
@post
op createParticipant(
  @path instanceID: string,
  @body body: CreateParticipantRequest,
): {
  @statusCode statusCode: 201;
  ...CreateParticipantResponse;
} | ErrorResponse;

@route("/instances/{instanceID}/participants")
@get
op listParticipants(
  @path instanceID: string,
  @query name?: string,
): ListParticipantsResponse | ErrorResponse;

@route("/instances/{instanceID}/participants/me")
@get
op getLinkedParticipant(@path instanceID: string): CreateParticipantResponse | ErrorResponse;

@route("/instances/{instanceID}/participants/{participantID}/discord-link")
@put
op linkParticipantDiscordUser(
  @path instanceID: string,
  @path participantID: string,
): CreateParticipantResponse | ErrorResponse;

@route("/instances/{instanceID}/participants/{participantID}/discord-link")
@delete
op unlinkParticipantDiscordUser(
  @path instanceID: string,
  @path participantID: string,
): CreateParticipantResponse | ErrorResponse;

@route("/instances/{instanceID}/participants/{participantID}/bonus-ledger")
@get
op getParticipantBonusLedger(
  @path instanceID: string,
  @path participantID: string,
): ParticipantBonusLedgerResponse | ErrorResponse;

@route("/instances/{instanceID}/stir-the-pot/me")
@get
op getStirThePotStatus(@path instanceID: string): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/stir-the-pot/tribes/show")
@get
op getStirThePotTribeStatus(
  @path instanceID: string,
  @query name: string,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/stir-the-pot/start")
@post
op startStirThePotRound(
  @path instanceID: string,
  @body body: StartStirThePotRoundRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/stir-the-pot/close")
@post
op closeStirThePotRound(@path instanceID: string): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/stir-the-pot/me/contributions")
@post
op addStirThePotContribution(
  @path instanceID: string,
  @body body: AddStirThePotContributionRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/auction/me")
@get
op getAuctionStatus(@path instanceID: string): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/auction/lots/start")
@post
op startAuctionLot(
  @path instanceID: string,
  @body body: StartAuctionLotRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/auction/lots/{contestantID}/stop")
@post
op stopAuctionLot(
  @path instanceID: string,
  @path contestantID: string,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/auction/contestants/{contestantID}/bid/me")
@put
op setAuctionBid(
  @path instanceID: string,
  @path contestantID: string,
  @body body: SetAuctionBidRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/ponies/me")
@get
op getMyPonies(@path instanceID: string): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/loan-shark/me")
@get
op getLoanSharkStatus(@path instanceID: string): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/loan-shark/me/borrow")
@post
op borrowFromLoanShark(
  @path instanceID: string,
  @body body: LoanSharkRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/loan-shark/me/repay")
@post
op repayLoanShark(
  @path instanceID: string,
  @body body: LoanSharkRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/individual-pony/immunity")
@post
op recordIndividualPonyImmunity(
  @path instanceID: string,
  @body body: RecordIndividualPonyImmunityRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/merge-auction/record")
@post
op recordMergeAuctionResults(
  @path instanceID: string,
  @body body: RecordMergeAuctionRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/finale-bingo/loan-sharks")
@post
op recordFinaleBingoLoanSharks(
  @path instanceID: string,
  @body body: RecordFinaleBingoLoanSharksRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/finale-bingo/scores/preview")
@post
op previewFinaleBingoScores(
  @path instanceID: string,
  @body body: RecordFinaleBingoScoresRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/finale-bingo/scores")
@post
op recordFinaleBingoScores(
  @path instanceID: string,
  @body body: RecordFinaleBingoScoresRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/drafts/{participantID}")
@put
op replaceDraft(
  @path instanceID: string,
  @path participantID: string,
  @body body: ReplaceDraftRequest,
): ReplaceDraftResponse | ErrorResponse;

@route("/instances/{instanceID}/drafts/{participantID}")
@get
op getDraft(@path instanceID: string, @path participantID: string): GetDraftResponse | ErrorResponse;

@route("/instances/{instanceID}/drafts/{participantID}/late")
@post
op acceptLateDraft(
  @path instanceID: string,
  @path participantID: string,
  @body body: ReplaceDraftRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/progression/draft/open")
@post
op openDraft(
  @path instanceID: string,
  @body body: ProgressionCommandRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/progression/draft/close")
@post
op closeDraft(
  @path instanceID: string,
  @body body: ProgressionCommandRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/progression/episodes/{episodeNumber}/start")
@post
op startEpisode(
  @path instanceID: string,
  @path episodeNumber: int32,
  @body body: ProgressionCommandRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/progression/episodes/{episodeNumber}/complete")
@post
op completeEpisode(
  @path instanceID: string,
  @path episodeNumber: int32,
  @body body: ProgressionCommandRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/progression/episodes/{episodeNumber}/score")
@post
op scoreEpisode(
  @path instanceID: string,
  @path episodeNumber: int32,
  @body body: ProgressionCommandRequest,
): JsonObject | ErrorResponse;

@route("/instances/{instanceID}/outcomes/{position}")
@put
op upsertOutcome(
  @path instanceID: string,
  @path position: int32,
  @body body: UpsertOutcomeRequest,
): UpsertOutcomeResponse | ErrorResponse;

@route("/instances/{instanceID}/outcomes")
@get
op listOutcomes(@path instanceID: string): ListOutcomesResponse | ErrorResponse;

@route("/instances/{instanceID}/leaderboard")
@get
op leaderboard(
  @path instanceID: string,
  @query participant_id?: string,
): LeaderboardResponse | ErrorResponse;

// --- Activities ---

@route("/instances/{instanceID}/activities")
@get
op listActivities(@path instanceID: string): ListActivitiesResponse | ErrorResponse;

@route("/instances/{instanceID}/participants/{participantID}/activity-history")
@get
op getParticipantActivityHistory(
  @path instanceID: string,
  @path participantID: string,
): ParticipantActivityHistoryResponse | ErrorResponse;

@route("/instances/{instanceID}/activities")
@post
op createActivity(
  @path instanceID: string,
  @body body: CreateActivityRequest,
): {
  @statusCode statusCode: 201;
  ...CreateActivityResponse;
} | ErrorResponse;

@route("/activities/{activityID}")
@get
op getActivity(@path activityID: string): GetActivityResponse | ErrorResponse;

// --- Occurrences ---

@route("/activities/{activityID}/occurrences")
@get
op listOccurrences(@path activityID: string): ListOccurrencesResponse | ErrorResponse;

@route("/activities/{activityID}/occurrences")
@post
op createOccurrence(
  @path activityID: string,
  @body body: CreateOccurrenceRequest,
): {
  @statusCode statusCode: 201;
  ...CreateOccurrenceResponse;
} | ErrorResponse;

@route("/activities/{activityID}/wordle-rounds")
@post
op createWordleRound(
  @path activityID: string,
  @body body: CreateWordleRoundRequest,
): {
  @statusCode statusCode: 201;
  ...CreateWordleRoundResponse;
} | {
  @statusCode statusCode: 200;
  ...CreateWordleRoundResponse;
} | ErrorResponse;

// --- Tribes ---

@route("/instances/{instanceID}/tribes")
@get
op getTribes(@path instanceID: string, @query at?: utcDateTime): TribesResponse | ErrorResponse;

@route("/instances/{instanceID}/tribes")
@put
op setTribes(@path instanceID: string, @body body: SetTribesRequest): TribesResponse | ErrorResponse;

model SetTribeColorRequest {
  color: string;
}

model SetTribeColorResponse {
  id: string;
  name: string;
  color: string;
  emoji: string;
}

/** Admin: set an existing tribe's color without changing its membership history. */
@route("/instances/{instanceID}/tribes/{name}")
@patch(#{implicitOptionality: false})
op setTribeColor(
  @path instanceID: string,
  @path name: string,
  @body body: SetTribeColorRequest,
): SetTribeColorResponse | ErrorResponse;

@route("/instances/{instanceID}/tribe-challenges")
@post
op recordTribeChallenge(
  @path instanceID: string,
  @body body: RecordTribeChallengeRequest,
): {
  @statusCode statusCode: 201;
  ...RecordTribeChallengeResponse;
} | {
  @statusCode statusCode: 200;
  ...RecordTribeChallengeResponse;
} | ErrorResponse;

// --- Occurrence results ---

@route("/occurrences/{occurrenceID}")
@get
op getOccurrence(@path occurrenceID: string): GetOccurrenceResponse | ErrorResponse;

@route("/occurrences/{occurrenceID}/participants")
@post
op createOccurrenceParticipant(
  @path occurrenceID: string,
  @body body: CreateOccurrenceParticipantRequest,
): {
  @statusCode statusCode: 201;
  ...CreateOccurrenceParticipantResponse;
} | ErrorResponse;

@route("/occurrences/{occurrenceID}/groups")
@post
op createOccurrenceGroup(
  @path occurrenceID: string,
  @body body: CreateOccurrenceGroupRequest,
): {
  @statusCode statusCode: 201;
  ...CreateOccurrenceGroupResponse;
} | ErrorResponse;

// --- Resolve ---

@route("/occurrences/{occurrenceID}/resolve")
@post
op resolveOccurrence(@path occurrenceID: string): ResolveOccurrenceResponse | ErrorResponse;

@route("/wordle-rounds/{roundID}")
@get
op getWordleRound(@path roundID: string): GetWordleRoundResponse | ErrorResponse;

@route("/wordle-rounds/{roundID}/participants/{participantID}")
@put
op putWordleParticipant(
  @path roundID: string,
  @path participantID: string,
  @body body: PutWordleParticipantRequest,
): PutWordleParticipantResponse | ErrorResponse;

@route("/wordle-rounds/{roundID}/close")
@post
op closeWordleRound(@path roundID: string): CloseWordleRoundResponse | ErrorResponse;

@route("/wordle-rounds/{roundID}/resolve")
@post
op resolveWordleRound(@path roundID: string): ResolveOccurrenceResponse | ErrorResponse;

/** Open draft submissions: each player's first saved draft is recorded in order, earns +2/+1 for 1st/2nd, gets a balanced random tribe, and queues a pinged announcement. */
@route("/instances/{instanceID}/draft-submissions")
@post
op openDraftSubmissions(
  @path instanceID: string,
  @body body: { tribes: string[]; guild_id: string; channel_id: string },
): { @statusCode statusCode: 201; status: string; tribes: string[] } | ErrorResponse;

/** Close draft submissions: later drafts get a tribe but no bonus; queues a post for the last player. */
@route("/instances/{instanceID}/draft-submissions/close")
@post
op closeDraftSubmissions(@path instanceID: string): { status: string; last?: Record<unknown> } | ErrorResponse;

/** Watch a Discord thread for draft posts (requires draft submissions to be open). */
@route("/instances/{instanceID}/draft-submissions/thread")
@put
op watchDraftThread(@path instanceID: string, @body body: { thread_id: string }): { thread_id: string } | ErrorResponse;

/** Drop a player's pending (incomplete) draft submission; they lose their submission order. */
@route("/instances/{instanceID}/draft-submissions/{participantID}/reject")
@post
op rejectDraftSubmission(@path instanceID: string, @path participantID: string): { status: string; order: int32 } | ErrorResponse;

/** Threads the bot watches for draft posts. */
@route("/draft-threads")
@get
op listDraftThreads(): { threads: { instance_id: string; thread_id: string }[] } | ErrorResponse;

/** One post (version) from a watched thread: ignored, duplicate, saved, unchanged, or problem (DM the admin). */
@route("/draft-threads/{threadID}/messages")
@post
op receiveDraftThreadMessage(
  @path threadID: string,
  @body body: { message_id: string; version: string; author_discord_user_id: string; content: string },
): {
  status: string;
  instance_id: string;
  player?: string;
  participant_id?: string;
  problems?: string[];
  submission?: Record<unknown>;
} | ErrorResponse;
