package main

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/debugbundlegin"
)

func main() {
	client := debugbundle.New(debugbundle.Config{
		ProjectToken:   "dbundle_proj_example",
		Service:        "checkout-api",
		Environment:    "development",
		ProjectMode:    debugbundle.ProjectModeLocalOnly,
		LocalEventsDir: ".debugbundle/local/events",
	})
	defer func() {
		_ = client.Flush(context.Background())
	}()

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(debugbundlegin.Middleware(client))
	router.GET("/checkout/:id", func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "payment provider unavailable"})
	})

	_ = router.Run(":8081")
}
