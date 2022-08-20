package context

import stdctx "context"

// WithCancelOnAnotherContext creates a new context from parent,
// but also cancelling upon a second context (other).
// Either the returned context or other must be done/cancelled at
// some point, otherwise the go routine created by this function
// will be blocked forever.
func WithCancelOnAnotherContext(parent stdctx.Context, other stdctx.Context) (stdctx.Context, stdctx.CancelFunc) {
	ctx, cancel := stdctx.WithCancel(parent)
	go func() {
		select {
		case <-ctx.Done():
		case <-other.Done():
			cancel()
		}
	}()
	return ctx, cancel
}
