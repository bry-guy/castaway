package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// The bot-permission check lists an instance's bound channels to its admins only.
func TestListDiscordChannelBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	queries := db.New(pool)
	instance := createInstanceForTest(t, ctx, queries, "BrainLand", 51)
	other := createInstanceForTest(t, ctx, queries, "Elsewhere", 52)
	if _, err := queries.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: instance.ID, DiscordUserID: "2001"}); err != nil {
		t.Fatal(err)
	}
	for _, b := range []db.SetDiscordChannelBindingParams{
		{GuildID: "7001", ChannelID: "7003", InstanceID: instance.ID},
		{GuildID: "7001", ChannelID: "7002", InstanceID: instance.ID},
		{GuildID: "7001", ChannelID: "7004", InstanceID: other.ID},
	} {
		if err := queries.SetDiscordChannelBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	router := httpapi.New(pool, httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"token"}})).Router()
	path := "/instances/" + uuid.UUID(instance.ID.Bytes).String() + "/discord-channels"

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, authorizedJSONRequest(http.MethodGet, path, "", "token", "2001"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin list: got %d: %s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		Channels []struct {
			GuildID   string `json:"guild_id"`
			ChannelID string `json:"channel_id"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Channels) != 2 || got.Channels[0].ChannelID != "7002" || got.Channels[1].ChannelID != "7003" {
		t.Fatalf("bindings for this instance only, sorted: %+v", got.Channels)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, authorizedJSONRequest(http.MethodGet, path, "", "token", "2002"))
	if recorder.Code == http.StatusOK {
		t.Fatalf("non-admin list should be refused: %s", recorder.Body.String())
	}
}
