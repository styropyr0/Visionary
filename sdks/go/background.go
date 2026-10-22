package visionary

import (
	"context"
	"fmt"
)

// Go runs fn in a new goroutine as background work of the request in ctx:
// the handler does not wait for it, and it is not cut short when the
// response is sent (ctx's values pass on, its cancellation does not). The
// work is recorded as one span of kind background named name, from start to
// finish, and the calls fn makes stay in the request's breakdown even after
// the response, so the dashboard shows what the request left running:
//
//	visionary.Go(r.Context(), "send welcome email", func(ctx context.Context) error {
//		return mailer.Send(ctx, user.Email, welcome)
//	})
//
// A panic in fn is recorded as the span's error and then re-panics, as it
// would in a plain goroutine.
func Go(ctx context.Context, name string, fn func(ctx context.Context) error) {
	ctx = context.WithoutCancel(ctx)
	end := StartSpan(ctx, KindBackground, name)
	go func() {
		var err error
		defer func() {
			if p := recover(); p != nil {
				end(fmt.Errorf("panic: %v", p))
				panic(p)
			}
			end(err)
		}()
		err = fn(ctx)
	}()
}
