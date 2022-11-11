package transport

import (
	"context"
	"time"
)

func retryWithBackoff(ctx context.Context, do func(context.Context) error) error {
	for numAttempt := 0; ; numAttempt++ {
		// calculate timeout
		timeoutSecs := 1 << numAttempt
		if timeoutSecs <= 0 || 60 < timeoutSecs {
			timeoutSecs = 60
		}
		timeout := time.Second * time.Duration(timeoutSecs)

		// run do()
		doTimeout := false
		err := func() error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			ch := make(chan struct{})
			var err error
			go func() {
				defer close(ch)
				err = do(ctx)
			}()

			timer := time.NewTimer(timeout)
			defer timer.Stop()

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ch:
				return err
			case <-timer.C:
				doTimeout = true
				return nil
			}
		}()
		if !doTimeout {
			return err
		}
	}
}
