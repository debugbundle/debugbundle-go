package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/debugbundlehttp"
	"github.com/debugbundle/debugbundle-go/debugbundleslog"
	"github.com/debugbundle/debugbundle-go/relay"
)

func main() {
	client := debugbundle.New(debugbundle.Config{
		ProjectToken: "dbundle_proj_example",
		Service:      "checkout-api",
		Environment:  "production",
	})
	defer func() {
		_ = client.Flush(context.Background())
	}()

	logger := slog.New(debugbundleslog.NewHandler(client, slog.NewJSONHandler(os.Stdout, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("/checkout", func(writer http.ResponseWriter, request *http.Request) {
		client.Probe(request.Context(), "checkout.state", map[string]any{
			"phase":      "authorize",
			"cart_items": 3,
		})
		logger.ErrorContext(request.Context(), "checkout failed", "route", request.URL.Path)
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	})
	mux.Handle("/debugbundle/browser", debugbundlehttp.RelayHandler(client, relay.Options{}))

	handler := debugbundlehttp.Middleware(client, debugbundlehttp.Options{RecoverPanics: true})(mux)
	_ = http.ListenAndServe(":8080", handler)
}