package pkgcontext

import (
	"context"
	"errors"
)

func IsContextError(ctx context.Context, err error) bool {
	return ctx.Err() != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}
