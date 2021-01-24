package safe

import (
	"context"

	"github.com/okian/servo/v2/lg"
)

// GoRoutine is a safe go routine system with recovery and a way to inform finish of the routine
func GoRoutine(c context.Context, f func(), extra ...interface{}) context.Context {
	ctx, cl := context.WithCancel(c)
	go func() {
		defer cl()
		defer func() {
			if e := recover(); e != nil {
				lg.Error(e)
			}
		}()

		f()
	}()

	return ctx
}
