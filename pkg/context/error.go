package pkgcontext

import (
	"context"
	"errors"
)

func IsContextError(ctx context.Context, err error) bool {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return false
	}
	for _, candidate := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, candidate) && errors.Is(ctxErr, candidate) {
			return true
		}
	}
	return false
}
