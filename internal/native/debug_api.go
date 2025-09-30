package native

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// AttachDebugHandler attaches the debug handler to the router
func (n *Native) AttachDebugHandler(router *gin.RouterGroup) {
	router.GET("/ui/screen/current", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"screen": uiGetCurrentScreen()})
	})

	router.POST("/ui/screen/switch", func(c *gin.Context) {
		var req struct {
			Screen          string   `json:"screen"`
			IfDifferentFrom []string `json:"ifDifferentFrom,omitempty"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		n.SwitchToScreenIf(req.Screen, req.IfDifferentFrom)
		c.JSON(http.StatusOK, gin.H{"screen": req.Screen})
	})
}
