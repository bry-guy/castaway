package httpapi_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/httpapi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestPublicListener covers Discord login (browser and probst), session-only /api, player/admin route
// limits, CSRF, internal-only routes, and access requests, against a fake Discord.
func TestPublicListener(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Public site", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	const admin, player, stranger = "pub-admin-1", "pub-player-1", "pub-stranger-1"
	p := createParticipantForTest(t, ctx, q, instance.ID, "Pat")
	if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: p.ID, DiscordUserID: pgtype.Text{String: player, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: instance.ID, DiscordUserID: admin}); err != nil {
		t.Fatal(err)
	}

	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		switch r.URL.Path {
		case "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			body = map[string]string{"access_token": r.Form.Get("code")}
		case "/users/@me":
			id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			body = map[string]string{"id": id, "username": id}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer discord.Close()
	const base = "https://castaway.example"
	router := httpapi.New(pool, httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"svc"}}),
		httpapi.WithPublic(httpapi.PublicConfig{BaseURL: base, DiscordClientID: "cid", DiscordClientSecret: "secret", DiscordAPIBaseURL: discord.URL, InstanceID: instanceID})).PublicRouter()

	serve := func(method, path string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	parse := func(raw string) *url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	// login runs /auth/login (or /auth/cli) → Discord → /auth/callback, with the fake Discord treating the
	// authorization code as the user ID.
	login := func(start, discordUserID string) *httptest.ResponseRecorder {
		rec := serve("GET", start, nil)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s: %d %s", start, rec.Code, rec.Body)
		}
		state := parse(rec.Header().Get("Location")).Query().Get("state")
		return serve("GET", "/auth/callback?code="+discordUserID+"&state="+state, nil, rec.Result().Cookies()...)
	}
	sessionOf := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == "castaway_session" && c.Value != "" {
				return c
			}
		}
		t.Fatalf("no session cookie: %d %s", rec.Code, rec.Body)
		return nil
	}
	want := func(rec *httptest.ResponseRecorder, code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body)
		}
	}
	leaderboard := "/api/instances/" + instanceID + "/leaderboard"

	// The service token and a forged actor header get nothing on the public listener.
	want(serve("GET", leaderboard, map[string]string{"Authorization": "Bearer svc", "X-Discord-User-ID": admin}), 401)
	want(serve("GET", "/auth/callback?code="+player+"&state=forged", nil), 400)

	playerCookie := sessionOf(login("/auth/login", player))

	// Mobile: Discord finishes in another browser without the OAuth cookie; a signed browser state still works
	// and returns to the requested puzzle, but a tampered state or a cookieless CLI login does not.
	start := serve("GET", "/auth/login?next=/castawordle", nil)
	state := parse(start.Header().Get("Location")).Query().Get("state")
	confirm := serve("GET", "/auth/callback?code="+player+"&state="+url.QueryEscape(state), nil)
	want(confirm, 200)
	for _, c := range confirm.Result().Cookies() {
		if c.Name == "castaway_session" && c.Value != "" {
			t.Fatal("cookieless callback created a session without confirmation")
		}
	}
	m := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(confirm.Body.String())
	if m == nil || !strings.Contains(confirm.Body.String(), "Continue as "+player) {
		t.Fatalf("confirm page: %s", confirm.Body)
	}
	post := func(origin, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth/confirm", strings.NewReader(url.Values{"token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	want(post("https://evil.example", html.UnescapeString(m[1])), 403)
	want(post(base, html.UnescapeString(m[1])+"x"), 400)
	mobile := post(base, html.UnescapeString(m[1]))
	if sessionOf(mobile); mobile.Header().Get("Location") != "/castawordle" {
		t.Fatalf("confirmed cookieless login: %d %s", mobile.Code, mobile.Header().Get("Location"))
	}
	want(serve("GET", "/auth/callback?code="+player+"&state="+url.QueryEscape(state[:len(state)-2]+"AA"), nil), 400)
	cliStart := serve("GET", "/auth/cli?port=45678&state=xyz", nil)
	want(serve("GET", "/auth/callback?code="+admin+"&state="+url.QueryEscape(parse(cliStart.Header().Get("Location")).Query().Get("state")), nil), 400)
	want(serve("GET", leaderboard, nil, playerCookie), 200)
	want(serve("GET", "/api/instances/"+instanceID+"/participants/me", map[string]string{"X-Discord-User-ID": admin}, playerCookie), 200)
	want(serve("GET", "/api/instances/"+instanceID+"/announcements", nil, playerCookie), 403)
	want(serve("GET", "/api/access-requests", nil, playerCookie), 403)
	want(serve("POST", "/api/instances/"+instanceID+"/tribe-challenges", map[string]string{"Origin": base}, playerCookie), 403)
	home := serve("GET", "/", nil, playerCookie)
	if want(home, 200); !strings.Contains(home.Body.String(), "<table>") || home.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("home page: %s", home.Body)
	}

	// probst: /auth/cli redirects to the loopback with a one-time code, exchanged once for a bearer token.
	back := login("/auth/cli?port=45678&state=xyz", admin)
	loc := parse(back.Header().Get("Location"))
	if loc.Host != "127.0.0.1:45678" || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("cli redirect: %s", loc)
	}
	body := fmt.Sprintf(`{"code":%q}`, loc.Query().Get("code"))
	exchange := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/auth/cli/exchange", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	first := exchange()
	want(first, 200)
	want(exchange(), 401)
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok.Token}
	want(serve("GET", "/api/instances/"+instanceID+"/announcements", bearer), 200)
	want(serve("POST", "/api/announcements/claim", bearer), 404)
	want(serve("POST", "/api/auth/logout", bearer), 204)
	want(serve("GET", leaderboard, bearer), 401)

	// Cookie writes need a same-origin Origin header.
	adminCookie := sessionOf(login("/auth/login", admin))
	want(serve("POST", "/api/instances/"+instanceID+"/tribe-challenges", map[string]string{"Origin": "https://evil.example"}, adminCookie), 403)

	// Strangers can sign in but only request access; probst login refuses them.
	strangerCookie := sessionOf(login("/auth/login", stranger))
	want(serve("GET", leaderboard, nil, strangerCookie), 403)
	if h := serve("GET", "/", nil, strangerCookie); !strings.Contains(h.Body.String(), "Request access") {
		t.Fatalf("stranger home: %s", h.Body)
	}
	want(serve("POST", "/access-request", map[string]string{"Origin": base}, strangerCookie), 303)
	if h := serve("GET", "/", nil, strangerCookie); !strings.Contains(h.Body.String(), "Access requested") {
		t.Fatalf("after request: %s", h.Body)
	}
	internal := httpapi.New(pool, httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"svc"}})).Router()
	claim := wordleServe(internal, "POST", "/access-requests/claim", "", "svc", "")
	if want(claim, 200); !strings.Contains(claim.Body.String(), stranger) {
		t.Fatalf("claim: %s", claim.Body)
	}
	if again := wordleServe(internal, "POST", "/access-requests/claim", "", "svc", ""); !strings.Contains(again.Body.String(), `"access_request":null`) {
		t.Fatalf("claimed twice: %s", again.Body)
	}
	want(serve("POST", "/api/access-requests/claim", map[string]string{"Origin": base}, adminCookie), 404)
	want(serve("GET", "/api/access-requests", nil, playerCookie), 403)
	if list := serve("GET", "/api/access-requests", nil, adminCookie); !strings.Contains(list.Body.String(), stranger) {
		t.Fatalf("list: %s", list.Body)
	}
	want(serve("DELETE", "/api/access-requests/"+stranger, map[string]string{"Origin": base}, adminCookie), 204)
	if loc := parse(login("/auth/cli?port=45678&state=s", stranger).Header().Get("Location")); loc.Query().Get("error") != "not_linked" {
		t.Fatalf("stranger cli login: %s", loc)
	}

	// A viewer sees scores, seasons and players, but no games and no API.
	want(serve("POST", "/access-request", map[string]string{"Origin": base}, strangerCookie), 303)
	viewers := "/instances/" + instanceID + "/viewers"
	want(wordleServe(internal, "POST", viewers, `{"discord_user_id":"`+stranger+`"}`, "svc", player), 403)
	want(wordleServe(internal, "POST", viewers, `{"discord_user_id":"`+stranger+`"}`, "svc", admin), 200)
	if b := serve("GET", "/", nil, strangerCookie).Body.String(); !strings.Contains(b, "<h1>Scores</h1>") || strings.Contains(b, "Request access") || strings.Contains(b, `href="/games"`) {
		t.Fatalf("viewer home: %s", b)
	}
	if list := serve("GET", "/api/access-requests", nil, adminCookie); strings.Contains(list.Body.String(), stranger) {
		t.Fatalf("viewer request not cleared: %s", list.Body)
	}
	want(serve("GET", "/seasons", nil, strangerCookie), 200)
	want(serve("GET", "/games", nil, strangerCookie), 302)
	want(serve("GET", leaderboard, nil, strangerCookie), 403)
}
