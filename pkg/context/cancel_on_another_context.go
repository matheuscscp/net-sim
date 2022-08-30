package pkgcontext

import "context"

// WithCancelOnAnotherContext creates a new context from parent,
// but also cancelling upon a second context (other).
// Either the returned context or other must be done/cancelled at
// some point, otherwise the thread created by this function will
// be blocked forever.
func WithCancelOnAnotherContext(parent context.Context, other context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-ctx.Done():
		case <-other.Done():
			cancel()
		}
	}()
	return ctx, cancel
}
