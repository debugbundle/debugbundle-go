package debugbundlehttp

import (
	"fmt"
	"net/http"
	"time"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/relay"
)

type Options struct {
	RecoverPanics bool
	RoutePattern  func(*http.Request) string
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func Middleware(client *debugbundle.Client, options Options) func(http.Handler) http.Handler {
	if client == nil {
		client = debugbundle.Init(debugbundle.Config{})
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			startedAt := time.Now()
			ctx := client.ContextForRequest(request)
			request = request.WithContext(ctx)
			recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
			defer func() {
				if recovered := recover(); recovered != nil {
					if recorder.status < http.StatusInternalServerError {
						recorder.status = http.StatusInternalServerError
					}
					response := debugbundle.ResponseInfo{
						StatusCode: recorder.status,
						Duration:   time.Since(startedAt),
					}
					if options.RoutePattern != nil {
						response.Route = options.RoutePattern(request)
					}
					client.CaptureException(ctx, fmt.Errorf("panic recovered: %v", recovered))
					client.CaptureRequest(ctx, request, response)
					if options.RecoverPanics {
						http.Error(recorder, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
						return
					}
					panic(recovered)
				}
				response := debugbundle.ResponseInfo{
					StatusCode: recorder.status,
					Duration:   time.Since(startedAt),
				}
				if options.RoutePattern != nil {
					response.Route = options.RoutePattern(request)
				}
				client.CaptureRequest(ctx, request, response)
			}()
			next.ServeHTTP(recorder, request)
		})
	}
}

func RelayHandler(client *debugbundle.Client, options relay.Options) http.Handler {
	return relay.NewHandler(client, options)
}
