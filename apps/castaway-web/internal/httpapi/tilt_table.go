package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Tilt Table: tilt the phone to roll three balls, one at a time, into three holes (site/assets/tilt-table.js).
// For now it is free play only: every linked player gets the unscored board, which needs a phone's tilt sensor.

// tiltTablePage sends the standalone game page.
func (s *Server) tiltTablePage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	page, err := siteFiles.ReadFile("site/assets/tilt-table.html")
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", page)
}
