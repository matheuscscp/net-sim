package transport

import (
	"context"
	"sync"
	"time"

	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
)

type (
	// deadline implements an advanced deadline mechanism allowing
	// for a deadline time point to be set() dynamically and cancel
	// all the contexts created with newContext() regardless of the
	// order with which these two methods were called.
	deadline struct {
		ctx       context.Context
		cancelCtx context.CancelFunc
		t         time.Time
		mu        sync.Mutex
		cond      *sync.Cond
	}
)

func newDeadline() *deadline {
	ctx, cancel := context.WithCancel(context.Background())
	d := &deadline{
		ctx:       ctx,
		cancelCtx: cancel,
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	d.t = t
	d.mu.Unlock()

	go func() {
		// wait
		ctx, cancel := context.WithDeadline(d.ctx, t)
		defer cancel()
		<-ctx.Done()

		// notify
		d.cond.Broadcast()
	}()
}

func (d *deadline) newContext(ctx context.Context) (context.Context, context.CancelFunc, *bool) {
	ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, d.ctx)
	exceeded := new(bool)

	d.mu.Lock()
	if d.exceeded() {
		d.mu.Unlock()
		cancel()
		*exceeded = true
		return ctx, cancel, exceeded
	}
	d.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()

		d.mu.Lock()
		for ctx.Err() == nil && !d.exceeded() {
			d.cond.Wait()
		}
		d.mu.Unlock()

		if ctx.Err() == nil {
			*exceeded = true
		}
	}()

	return ctx, func() {
		cancel()
		d.mu.Lock()
		d.cond.Broadcast()
		d.mu.Unlock()
		wg.Wait()
	}, exceeded
}

func (d *deadline) exceeded() bool {
	return !d.t.IsZero() && d.t.Before(time.Now())
}

func (d *deadline) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, d.cancelCtx = d.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// notify
	d.mu.Lock()
	d.cond.Broadcast()
	d.mu.Unlock()

	return nil
}
