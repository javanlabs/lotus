package web

import (
	"io"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// secretPathPrefixes are routes whose next path segment is a bearer secret:
// a QA share link opens a plan report, an invitation link a membership.
// Only their hashes are stored, so the request log must not keep them
// either.
var secretPathPrefixes = []string{"/s/qa/", "/invite/"}

// logPath is the request path with any link token replaced.
func logPath(path string) string {
	for _, prefix := range secretPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return prefix + "[redacted]"
		}
	}
	return path
}

// requestLogger is Fiber's access log with link tokens redacted from ${path}.
func requestLogger(out io.Writer) fiber.Handler {
	if out == nil {
		out = os.Stdout
	}
	return logger.New(logger.Config{
		Output: out,
		CustomTags: map[string]logger.LogFunc{
			logger.TagPath: func(output logger.Buffer, c *fiber.Ctx, _ *logger.Data, _ string) (int, error) {
				return output.WriteString(logPath(c.Path()))
			},
		},
	})
}
