package debugbundleecho

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	debugbundle "github.com/debugbundle/debugbundle-go/v3"
)

func Middleware(client *debugbundle.Client) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			startedAt := time.Now()
			ctx := client.ContextForRequest(c.Request())
			ctx, endOperation := client.BeginOperation(ctx, debugbundle.OperationOptions{Kind: "request"})
			defer endOperation()
			c.SetRequest(c.Request().WithContext(ctx))
			defer func() {
				if recovered := recover(); recovered != nil {
					statusCode := c.Response().Status
					if statusCode < http.StatusInternalServerError {
						statusCode = http.StatusInternalServerError
					}
					response := debugbundle.ResponseInfo{
						StatusCode: statusCode,
						Duration:   time.Since(startedAt),
						Route:      c.Path(),
					}
					client.CapturePanic(ctx, recovered)
					client.CaptureRequest(ctx, c.Request(), response)
					panic(recovered)
				}
				statusCode := c.Response().Status
				if err != nil {
					client.CaptureException(ctx, err)
					if httpError, ok := err.(*echo.HTTPError); ok {
						statusCode = httpError.Code
					} else if statusCode == 0 {
						statusCode = http.StatusInternalServerError
					}
				} else if statusCode == 0 {
					statusCode = http.StatusOK
				}
				response := debugbundle.ResponseInfo{
					StatusCode: statusCode,
					Duration:   time.Since(startedAt),
					Route:      c.Path(),
				}
				client.CaptureRequest(ctx, c.Request(), response)
			}()
			err = next(c)
			return err
		}
	}
}
