package main

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/debugbundleecho"
)

func main() {
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_example",
		Service:      "orders-api",
		Environment:  "staging",
		Endpoint:     "https://api.debugbundle.com/v1/events",
	})
	defer func() {
		_ = client.Flush(context.Background())
	}()

	app := echo.New()
	app.Use(debugbundleecho.Middleware(client))
	app.GET("/orders/:id", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusBadGateway, "warehouse API timed out")
	})

	_ = app.Start(":8082")
}