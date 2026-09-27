// Package blob stores uploaded files (QA attachments) outside the database.
// Keys are slash-separated relative paths chosen by the caller, e.g.
// qa/<org_id>/<yyyy>/<mm>/<uuid>; a key never contains "..", a leading
// slash, a backslash or an empty segment.
package blob

import (
	"context"
	"errors"
	"io"
	"strings"
)

var (
	ErrNotFound = errors.New("blob not found")
	ErrBadKey   = errors.New("invalid blob key")
)

type Store interface {
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

func validKey(key string) bool {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
