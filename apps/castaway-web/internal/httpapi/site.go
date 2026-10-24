package httpapi

import (
	"context"
	"embed"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

//go:embed site
var siteFiles embed.FS

var siteTemplates = template.Must(template.New("site").ParseFS(siteFiles, "site/*.html"))

type sitePageData struct {
	User               *webSession
	Allowed, Requested bool
	Admin              bool
	InstanceID         string
	Rows               []homeRow
	Profile            *profileView
	Seasons            []seasonSummary
	SeasonName         string // set when showing a past season's scoreboard
	NoBonus            bool   // past season with no bonus points (bonuses started in Season 50)
	NoTribe            bool   // past season without tribes (tribes started in Season 50)
	Games              []castawordleGameView
	Episodes           []castawordleEpisodeOption
	Game               *castawordleGameView
	Button             *buttonView
	Scramble           *scrambleView
	Track              *trackView
	FreeTheBlock       *freeTheBlockView
	Champion           *championView
	GameEpisodes       []gameEpisode
	TestGames          []gameEntry
	SeasonID           string // /games?season=: a past season's games
	Answer             string
	CastawordleResults []castawordleResult
	Closed             bool
	TribeColors        map[string]string // tribe name → stored color (participant_groups.color), for dots
}

func loginReturnTo(candidate string) string {
	// Old /castawordle links still return there (it redirects to /games).
	if candidate == "/games" || candidate == "/me" || candidate == "/games/button" || candidate == "/games/scramble" || candidate == "/games/champion" || candidate == "/castawordle" || candidate == "/button" {
		return candidate
	}
	for _, prefix := range []string{"/games/castawordle/", "/games/button/", "/games/scramble/", "/castawordle/", "/button/", "/players/"} {
		if suffix, ok := strings.CutPrefix(candidate, prefix); ok {
			if id, err := uuid.Parse(suffix); err == nil {
				return prefix + id.String()
			}
		}
	}
	return "/"
}

func (s *Server) requirePageSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, err := c.Cookie(sessionCookie)
		if err != nil {
			token = ""
		}
		ws, err := s.lookupSession(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if ws == nil {
			c.Redirect(http.StatusFound, "/auth/login?next="+url.QueryEscape(loginReturnTo(c.Request.URL.Path)))
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) siteData(c *gin.Context) (sitePageData, bool) {
	data := sitePageData{InstanceID: s.public.InstanceID}
	token, err := c.Cookie(sessionCookie)
	if err != nil {
		token = ""
	}
	data.User, err = s.lookupSession(c.Request.Context(), token)
	if err != nil || data.User == nil {
		c.String(http.StatusUnauthorized, "please log in with Discord")
		return data, false
	}
	id, err := uuid.Parse(s.public.InstanceID)
	if err == nil {
		data.Admin, err = s.isInstanceAdmin(c.Request.Context(), toPGUUID(id), data.User.DiscordUserID)
		if err == nil {
			err = s.pool.QueryRow(c.Request.Context(), `SELECT EXISTS (
				SELECT 1 FROM participants p JOIN instances i ON i.id = p.instance_id
				WHERE i.public_id = $1 AND p.discord_user_id = $2
			)`, id, data.User.DiscordUserID).Scan(&data.Allowed)
			data.Allowed = data.Allowed || data.Admin
			if err == nil {
				data.TribeColors, err = s.tribeColors(c.Request.Context(), id)
			}
		}
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "access check failed")
		return data, false
	}
	if !data.Allowed {
		err = s.pool.QueryRow(c.Request.Context(), `SELECT EXISTS (SELECT 1 FROM access_requests WHERE discord_user_id = $1)`, data.User.DiscordUserID).Scan(&data.Requested)
		if err != nil {
			c.String(http.StatusInternalServerError, "access check failed")
			return data, false
		}
	}
	return data, true
}

func renderSite(c *gin.Context, page string, data sitePageData) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if err := siteTemplates.ExecuteTemplate(c.Writer, page, data); err != nil {
		requestLogger.Error("render site", "error", err)
	}
}

func serveSiteAsset(c *gin.Context) {
	name := c.Param("file")
	data, err := siteFiles.ReadFile("site/assets/" + name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, mime.TypeByExtension(path.Ext(name)), data)
}

// tribeColors maps an instance's tribe names to their stored colors.
func (s *Server) tribeColors(ctx context.Context, instanceID uuid.UUID) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.name, g.color FROM participant_groups g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.kind = 'tribe' AND g.color IS NOT NULL`, toPGUUID(instanceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	colors := map[string]string{}
	for rows.Next() {
		var name, color string
		if err := rows.Scan(&name, &color); err != nil {
			return nil, err
		}
		colors[name] = color
	}
	return colors, rows.Err()
}
