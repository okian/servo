package safe

import (
	"context"
	"errors"
	"time"

	log "github.com/okian/servo/v3/log"
)

// WithCancel try to do fn until context will cancel or fn return nil and will sleep in (logarithmic) duration of
// unit (millisecond, second,etc) and not more than unit * maxSleep.
// The return channel stream's fn error in each iteration and can be use for blocking or/and if needed
func Try(ctx context.Context, maxSleep uint, fn func() error) <-chan error {
	f := fibonacci(maxSleep)
	c := time.After(time.Duration(f()) * time.Millisecond)
	r := make(chan error)
	go func() {
		defer close(r)
		for {
			select {
			case <-ctx.Done():
				return
			case <-c:
				if err := fn(); err != nil {
					r <- err
					c = time.After(time.Duration(f()) * time.Millisecond)
					continue
				}
				return
			}
		}
	}()
	return r
}

func DoWithError(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(r)
			err = errors.New("paniced")
		}
	}()

	err = f()
	return
}

func Do(f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(r)
		}
	}()

	f()
	return
}

func fibonacci(max uint) func() uint {
	var x, y uint
	return func() uint {
		if x >= max {
			return max
		} else if x < 1 {
			x++
			return y
		}
		x, y = x+y, x
		return y
	}
}
