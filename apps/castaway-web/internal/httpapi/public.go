package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// PublicConfig configures the internet-facing listener: the website, Discord login, and /api.
type PublicConfig struct {
	BaseURL             string // e.g. https://castaway.bry-guy.net
	DiscordClientID     string
	DiscordClientSecret string
	DiscordAPIBaseURL   string   // default https://discord.com/api/v10
	DiscordAuthorizeURL string   // default https://discord.com/oauth2/authorize
	InstanceID          string   // the season the site shows
	LeagueName          string   // shown beside season names, e.g. NowThisIsPodracing
	LeagueInstanceIDs   []string // past seasons of the same league; profiles show only these and InstanceID
}

func WithPublic(cfg PublicConfig) Option {
	return func(s *Server) {
		cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
		if cfg.DiscordAPIBaseURL == "" {
			cfg.DiscordAPIBaseURL = "https://discord.com/api/v10"
		}
		if cfg.DiscordAuthorizeURL == "" {
			cfg.DiscordAuthorizeURL = "https://discord.com/oauth2/authorize"
		}
		s.public = cfg
	}
}

const (
	sessionCookie   = "castaway_session"
	oauthCookie     = "castaway_oauth"
	webSessionActor = "castaway-web-session"
	browserTTL      = 30 * 24 * time.Hour
	cliTTL          = 90 * 24 * time.Hour
	cliCodeTTL      = 2 * time.Minute
)

// playerRoutes are what a signed-in, non-admin player may call. Everything else needs an instance admin.
var playerRoutes = map[string]bool{
	"GET /instances/:instanceID":                                              true,
	"GET /instances/:instanceID/contestants":                                  true,
	"GET /instances/:instanceID/participants/me":                              true,
	"GET /instances/:instanceID/leaderboard":                                  true,
	"GET /instances/:instanceID/tribes":                                       true,
	"GET /instances/:instanceID/outcomes":                                     true,
	"GET /instances/:instanceID/drafts/:participantID":                        true,
	"GET /instances/:instanceID/participants/:participantID/bonus-ledger":     true,
	"GET /instances/:instanceID/participants/:participantID/activity-history": true,
	"GET /admin/session":                                                      true,
	"POST /auth/logout":                                                       true,
	"GET /castawordle/:gameID/play":                                           true,
	"POST /castawordle/:gameID/play/guesses":                                  true,
}

// internalOnlyRoutes are the bot's work queues and first-admin bootstrap; the public listener never serves them.
var internalOnlyRoutes = map[string]bool{
	"POST /announcements/claim":                    true,
	"POST /access-requests/claim":                  true,
	"POST /announcements/:announcementID/finish":   true,
	"GET /draft-threads":                           true,
	"POST /draft-threads/:threadID/messages":       true,
	"POST /instances/:instanceID/admins/bootstrap": true,
}

// PublicRouter serves the website, Discord login, and the API under /api with session auth.
// It never accepts the service token or a caller-supplied X-Discord-User-ID.
func (s *Server) PublicRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), structuredRequestLogger(), metricsMiddleware(), securityHeaders())
	r.GET("/healthz", s.health)
	r.GET("/assets/:file", serveSiteAsset)
	pages := r.Group("/")
	pages.Use(s.requirePageSession())
	pages.GET("/", s.home)
	pages.GET("/me", s.mePage)
	pages.POST("/me/pronouns", s.requireSameOrigin(), s.setPronouns)
	pages.GET("/seasons", s.seasonsPage)
	pages.GET("/seasons/:instanceID", s.seasonPage)
	pages.GET("/players/:participantID", s.playerPage)
	pages.GET("/games", s.gamesPage)
	pages.GET("/games/castawordle/:gameID", s.castawordlePage)
	pages.GET("/games/button", s.buttonPage)
	pages.POST("/games/button/tests", s.requireSameOrigin(), s.createButtonTest)
	pages.GET("/games/button/:gameID", s.buttonPage)
	pages.GET("/games/scramble", s.scramblePage)
	pages.GET("/games/inside-track", s.insideTrackPage)
	pages.GET("/games/inside-track/:gameID", s.insideTrackPage)
	pages.POST("/games/inside-track/:gameID/progress", s.requireSameOrigin(), s.trackProgress)
	pages.GET("/games/free-the-block", s.freeTheBlockPage)
	pages.GET("/games/free-the-block/:gameID", s.freeTheBlockPage)
	pages.POST("/games/free-the-block/:gameID/progress", s.requireSameOrigin(), s.freeTheBlockProgress)
	pages.GET("/games/champion", s.championPage)
	pages.POST("/games/champion", s.requireSameOrigin(), s.pickChampion)
	pages.POST("/games/scramble/tests", s.requireSameOrigin(), s.createScrambleTest)
	pages.GET("/games/scramble/:gameID", s.scramblePage)
	pages.POST("/games/scramble/:gameID/start", s.requireSameOrigin(), s.startScramble)
	pages.POST("/games/scramble/:gameID/check", s.requireSameOrigin(), s.checkScramble)
	pages.POST("/games/button/:gameID", s.requireSameOrigin(), s.pressButton)
	// Old links (already posted in Discord) redirect to /games.
	for _, old := range []string{"/castawordle", "/castawordle/:gameID", "/button", "/button/:gameID"} {
		pages.GET(old, legacyGameRedirect)
	}
	r.GET("/auth/login", s.startLogin)
	r.GET("/auth/cli", s.startLogin)
	r.GET("/auth/callback", s.loginCallback)
	r.POST("/auth/confirm", s.requireSameOrigin(), s.confirmLogin)
	r.POST("/auth/logout", s.requireSameOrigin(), s.logout)
	r.POST("/access-request", s.requireSameOrigin(), s.requestAccess)
	r.POST("/api/auth/cli/exchange", s.exchangeCLICode)
	api := r.Group("/api")
	api.Use(s.requireSession())
	api.POST("/auth/logout", s.logout)
	s.registerAPI(api)
	s.publicEngine = r
	return r
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		c.Next()
	}
}

// requireSameOrigin blocks cross-site form posts (CSRF) on cookie-authenticated writes.
func (s *Server) requireSameOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Origin") != s.public.BaseURL {
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{Error: "cross-origin request rejected"})
			return
		}
		c.Next()
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.public.BaseURL, "https://") }

// startLogin sends the browser to Discord. /auth/cli?port=N&state=S is probst's variant: the result goes
// back to probst's loopback listener instead of a browser session.
func (s *Server) startLogin(c *gin.Context) {
	mode := "browser|" + loginReturnTo(c.Query("next"))
	if c.FullPath() == "/auth/cli" {
		port, err := strconv.Atoi(c.Query("port"))
		cliState := c.Query("state")
		if err != nil || port < 1024 || port > 65535 || cliState == "" || len(cliState) > 128 || strings.ContainsAny(cliState, "| ") {
			c.String(http.StatusBadRequest, "invalid probst login request")
			return
		}
		mode = fmt.Sprintf("cli|%d|%s", port, cliState)
	}
	state := s.signLoginState(mode)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oauthCookie, state+"|"+mode, 600, "/auth", "", s.secureCookies(), true)
	q := url.Values{"response_type": {"code"}, "client_id": {s.public.DiscordClientID}, "scope": {"identify"}, "state": {state}, "redirect_uri": {s.public.BaseURL + "/auth/callback"}, "prompt": {"none"}}
	c.Redirect(http.StatusFound, s.public.DiscordAuthorizeURL+"?"+q.Encode())
}

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
}

func (s *Server) discordIdentify(ctx context.Context, code string) (discordUser, error) {
	var user discordUser
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.public.BaseURL + "/auth/callback"}, "client_id": {s.public.DiscordClientID}, "client_secret": {s.public.DiscordClientSecret}}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", s.public.DiscordAPIBaseURL+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return user, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := doJSON(client, req, &token); err != nil || token.AccessToken == "" {
		return user, fmt.Errorf("discord token exchange failed: %v", err)
	}
	req, err = http.NewRequestWithContext(ctx, "GET", s.public.DiscordAPIBaseURL+"/users/@me", nil)
	if err != nil {
		return user, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	if err := doJSON(client, req, &user); err != nil || user.ID == "" {
		return user, fmt.Errorf("discord identify failed: %v", err)
	}
	if user.GlobalName == "" {
		user.GlobalName = user.Username
	}
	return user, nil
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (s *Server) loginCallback(c *gin.Context) {
	cookie, err := c.Cookie(oauthCookie)
	if err != nil {
		cookie = ""
	}
	c.SetCookie(oauthCookie, "", -1, "/auth", "", s.secureCookies(), true)
	state, mode, _ := strings.Cut(cookie, "|")
	cookieless := false
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(c.Query("state"))) != 1 {
		// Mobile Discord often finishes sign-in in a different browser than the one that started it, so
		// the cookie is missing; a server-signed browser state is still accepted there, but only after the
		// user confirms the account (anyone can obtain a signed state, so this blocks login CSRF).
		mode = s.verifyLoginState(c.Query("state"))
		cookieless = true
	}
	if mode == "" || c.Query("code") == "" {
		s.loginRetryPage(c)
		return
	}
	ctx := c.Request.Context()
	user, err := s.discordIdentify(ctx, c.Query("code"))
	if err != nil {
		c.String(http.StatusBadGateway, "Discord login failed. Try again.")
		return
	}
	if strings.HasPrefix(mode, "cli|") {
		parts := strings.SplitN(mode, "|", 3)
		back := "http://127.0.0.1:" + parts[1] + "/callback?state=" + url.QueryEscape(parts[2])
		allowed, err := s.webAccess(ctx, user.ID)
		if err != nil {
			c.String(http.StatusInternalServerError, "login failed")
			return
		}
		if !allowed {
			c.Redirect(http.StatusFound, back+"&error=not_linked")
			return
		}
		code := randomToken()
		if _, err := s.pool.Exec(ctx, `INSERT INTO web_cli_codes (code_hash, discord_user_id, discord_username, expires_at) VALUES ($1, $2, $3, $4)`, hashToken(code), user.ID, user.GlobalName, s.now().Add(cliCodeTTL)); err != nil {
			c.String(http.StatusInternalServerError, "login failed")
			return
		}
		c.Redirect(http.StatusFound, back+"&code="+url.QueryEscape(code))
		return
	}
	if cookieless {
		s.loginConfirmPage(c, user, mode)
		return
	}
	s.finishBrowserLogin(c, user.ID, user.GlobalName, mode)
}

func (s *Server) finishBrowserLogin(c *gin.Context, discordUserID, username, mode string) {
	token, err := s.createSession(c.Request.Context(), discordUserID, username, "browser", browserTTL)
	if err != nil {
		c.String(http.StatusInternalServerError, "login failed")
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookie, token, int(browserTTL.Seconds()), "/", "", s.secureCookies(), true)
	c.Redirect(http.StatusSeeOther, loginReturnTo(strings.TrimPrefix(mode, "browser|")))
}

// loginConfirmPage asks a cookieless browser to confirm the Discord account before a session is made.
func (s *Server) loginConfirmPage(c *gin.Context, user discordUser, mode string) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(user.ID)) + "." + base64.RawURLEncoding.EncodeToString([]byte(user.GlobalName)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(mode)) + "." + strconv.FormatInt(s.now().Add(loginStateTTL).Unix(), 10)
	token := payload + "." + s.loginStateMAC("confirm|"+payload)
	name := html.EscapeString(user.GlobalName)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Confirm sign-in \u00b7 Castaway</title><link rel="stylesheet" href="/assets/site.css"></head><body><main><h1>Continue as `+name+`?</h1><p>You're signing in to Castaway with the Discord account <strong>`+name+`</strong>. If this isn't you, close this page.</p><form method="post" action="/auth/confirm"><input type="hidden" name="token" value="`+html.EscapeString(token)+`"><button class="button" type="submit">Continue as `+name+`</button></form></main></body></html>`))
}

func (s *Server) confirmLogin(c *gin.Context) {
	token := c.PostForm("token")
	i := strings.LastIndex(token, ".")
	if i < 0 || !hmac.Equal([]byte(token[i+1:]), []byte(s.loginStateMAC("confirm|"+token[:i]))) {
		s.loginRetryPage(c)
		return
	}
	parts := strings.Split(token[:i], ".")
	if len(parts) != 4 {
		s.loginRetryPage(c)
		return
	}
	id, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	name, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	mode, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	expiry, err4 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || s.now().Unix() > expiry || !strings.HasPrefix(string(mode), "browser|") {
		s.loginRetryPage(c)
		return
	}
	s.finishBrowserLogin(c, string(id), string(name), string(mode))
}

const loginStateTTL = 10 * time.Minute

// signLoginState returns an OAuth state of nonce.expiry.mode.mac, verifiable without the browser cookie.
func (s *Server) signLoginState(mode string) string {
	payload := randomToken() + "." + strconv.FormatInt(s.now().Add(loginStateTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString([]byte(mode))
	return payload + "." + s.loginStateMAC(payload)
}

// verifyLoginState returns the browser mode of a validly signed, unexpired state, or "".
// CLI logins still require the cookie.
func (s *Server) verifyLoginState(state string) string {
	i := strings.LastIndex(state, ".")
	if i < 0 || !hmac.Equal([]byte(state[i+1:]), []byte(s.loginStateMAC(state[:i]))) {
		return ""
	}
	parts := strings.Split(state[:i], ".")
	if len(parts) != 3 {
		return ""
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	mode, decodeErr := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || decodeErr != nil || s.now().Unix() > expiry || !strings.HasPrefix(string(mode), "browser|") {
		return ""
	}
	return string(mode)
}

func (s *Server) loginStateMAC(payload string) string {
	mac := hmac.New(sha256.New, []byte("castaway-login-state|"+s.public.DiscordClientSecret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) loginRetryPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Sign in again · Castaway</title><link rel="stylesheet" href="/assets/site.css"></head><body><main><h1>Sign-in didn't finish</h1><p>Your Discord sign-in expired or was interrupted. Please try again.</p><p><a class="button" href="/auth/login?next=/castawordle">Sign in with Discord</a></p></main></body></html>`))
}

func (s *Server) createSession(ctx context.Context, discordUserID, username, kind string, ttl time.Duration) (string, error) {
	token := randomToken()
	_, err := s.pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, discord_user_id, discord_username, kind, expires_at) VALUES ($1, $2, $3, $4, $5)`, hashToken(token), discordUserID, username, kind, s.now().Add(ttl))
	return token, err
}

// exchangeCLICode trades a one-time code from the loopback redirect for a probst session token.
func (s *Server) exchangeCLICode(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "code is required"})
		return
	}
	ctx := c.Request.Context()
	var id, username string
	err := s.pool.QueryRow(ctx, `DELETE FROM web_cli_codes WHERE code_hash = $1 AND expires_at > $2 RETURNING discord_user_id, discord_username`, hashToken(req.Code), s.now()).Scan(&id, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "code is invalid or expired"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	token, err := s.createSession(ctx, id, username, "cli", cliTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "discord_user_id": id, "discord_username": username, "expires_at": s.now().Add(cliTTL).Format(time.RFC3339)})
}

// sessionToken reads a bearer token (probst) or the session cookie (browser).
func sessionToken(c *gin.Context) (string, bool) {
	if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")), false
	}
	cookie, err := c.Cookie(sessionCookie)
	if err != nil {
		return "", true
	}
	return cookie, true
}

type webSession struct {
	DiscordUserID string
	Username      string
}

func (s *Server) lookupSession(ctx context.Context, token string) (*webSession, error) {
	if token == "" {
		return nil, nil
	}
	var ws webSession
	err := s.pool.QueryRow(ctx, `SELECT discord_user_id, discord_username FROM web_sessions WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $2`, hashToken(token), s.now()).Scan(&ws.DiscordUserID, &ws.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &ws, err
}

// webAccess: linked players and instance admins may use the site; anyone else can only request access.
func (s *Server) webAccess(ctx context.Context, discordUserID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM participants WHERE discord_user_id = $1) OR EXISTS (SELECT 1 FROM instance_admins WHERE discord_user_id = $1)`, discordUserID).Scan(&ok)
	return ok, err
}

func (s *Server) isAnyInstanceAdmin(ctx context.Context, discordUserID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM instance_admins WHERE discord_user_id = $1)`, discordUserID).Scan(&ok)
	return ok, err
}

// requireSession authenticates /api by session, then asserts the session's Discord user to the shared
// handlers exactly as the trusted service would. Non-admins only reach playerRoutes.
// ponytail: admin of any instance may call every admin route (handlers without their own per-instance
// check trust that); scope per instance if another season gets a different admin.
func (s *Server) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		route := c.Request.Method + " " + strings.TrimPrefix(c.FullPath(), "/api")
		if internalOnlyRoutes[route] {
			c.AbortWithStatusJSON(http.StatusNotFound, errorResponse{Error: "not found"})
			return
		}
		token, fromCookie := sessionToken(c)
		if fromCookie && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.GetHeader("Origin") != s.public.BaseURL {
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{Error: "cross-origin request rejected"})
			return
		}
		ctx := c.Request.Context()
		ws, err := s.lookupSession(ctx, token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{Error: "session lookup failed"})
			return
		}
		if ws == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
			return
		}
		allowed, err := s.webAccess(ctx, ws.DiscordUserID)
		if err == nil && !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{Error: "your Discord account isn't linked to a player yet"})
			return
		}
		admin, err2 := s.isAnyInstanceAdmin(ctx, ws.DiscordUserID)
		if err != nil || err2 != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{Error: "access check failed"})
			return
		}
		if !admin && !playerRoutes[route] {
			c.AbortWithStatusJSON(http.StatusForbidden, errorResponse{Error: "admin only"})
			return
		}
		c.Request.Header.Set(discordUserIDHeader, ws.DiscordUserID)
		c.Request = c.Request.WithContext(context.WithValue(ctx, serviceAuthContextKey{}, webSessionActor))
		c.Set("service_principal", webSessionActor)
		c.Next()
	}
}

func (s *Server) logout(c *gin.Context) {
	token, _ := sessionToken(c)
	if token != "" {
		if _, err := s.pool.Exec(c.Request.Context(), `UPDATE web_sessions SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL`, hashToken(token), s.now()); err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
	}
	c.SetCookie(sessionCookie, "", -1, "/", "", s.secureCookies(), true)
	if strings.HasPrefix(c.FullPath(), "/api") {
		c.Status(http.StatusNoContent)
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (s *Server) requestAccess(c *gin.Context) {
	token, _ := sessionToken(c)
	ws, err := s.lookupSession(c.Request.Context(), token)
	if err != nil || ws == nil {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	if _, err := s.pool.Exec(c.Request.Context(), `INSERT INTO access_requests (discord_user_id, discord_username) VALUES ($1, $2) ON CONFLICT (discord_user_id) DO NOTHING`, ws.DiscordUserID, ws.Username); err != nil {
		c.String(http.StatusInternalServerError, "request failed")
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

type homeRow struct {
	Rank                int
	ID, Name, Tribe     string
	Total, Draft, Bonus int
}

func (s *Server) home(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if data.Allowed {
		rows, err := s.homeLeaderboard(c)
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		data.Rows = rows
	}
	renderSite(c, "home.html", data)
}

func (s *Server) homeLeaderboard(c *gin.Context) ([]homeRow, error) {
	if s.public.InstanceID == "" {
		return nil, nil
	}
	board, err := s.leaderboardRows(c.Request.Context(), s.public.InstanceID)
	rows := make([]homeRow, 0, len(board))
	for _, r := range board {
		rows = append(rows, homeRow{Rank: r.Rank, ID: r.ParticipantID, Name: r.Name, Tribe: r.Tribe, Total: r.Total, Draft: r.Draft, Bonus: r.Bonus})
	}
	return rows, err
}
