package safe

import (
	"context"

	log "github.com/okian/servo/v3/log"
)

// GoRoutine is a safe go routine system with recovery and a way to inform finish of the routine
func GoRoutine(c context.Context, f func(), extra ...interface{}) context.Context {
	ctx, cl := context.WithCancel(c)
	go func() {
		defer cl()
		defer func() {
			if e := recover(); e != nil {
				log.Error(e)
			}
		}()

		f()
	}()

	return ctx
}
