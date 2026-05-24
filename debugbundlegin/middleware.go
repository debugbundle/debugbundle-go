package debugbundlegin

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	debugbundle "github.com/debugbundle/debugbundle-go"
)

func Middleware(client *debugbundle.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		ctx := client.ContextForRequest(c.Request)
		c.Request = c.Request.WithContext(ctx)
		defer func() {
			response := debugbundle.ResponseInfo{
				StatusCode: c.Writer.Status(),
				Duration:   time.Since(startedAt),
				Route:      c.FullPath(),
			}
			if recovered := recover(); recovered != nil {
				client.CaptureException(ctx, fmt.Errorf("panic recovered: %v", recovered))
				client.CaptureRequest(ctx, c.Request, response)
				panic(recovered)
			}
			if len(c.Errors) > 0 {
				client.CaptureException(ctx, c.Errors.Last())
			}
			client.CaptureRequest(ctx, c.Request, response)
		}()
		c.Next()
	}
}
