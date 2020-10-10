package util

import (
	"errors"

	"context"
	"time"

	"github.com/hashicorp/go-multierror"
)

// MaxRetries is the maximum number of retries before bailing.
var MaxRetries = 10

var errMaxRetriesReached = errors.New("exceeded retry limit")

// Func represents functions that can be retried.
type Func func(attempt int) (retry bool, err error)

// Do keeps trying the function until the second argument returns false, or no
// error is returned, attempt is started at 1.
//
// A *multierror.Error combining all attempt errors on failure.
// If the function does not return true before MaxRetries then the combination
// of all errors that occurred will be returned which IsMaxRetries() will return
// true for.
func Do(fn Func) error {
	err := do(fn)
	if merr, ok := err.(*multierror.Error); ok {
		return merr.ErrorOrNil()
	}
	return err
}

func do(fn Func) error {
	var errs error
	attempt := 1
	for {
		cont, err := fn(attempt)
		if err == nil {
			return nil
		}

		errs = multierror.Append(errs, err)
		if !cont {
			return errs
		}

		attempt++
		if attempt > MaxRetries {
			return multierror.Append(errs, errMaxRetriesReached)
		}
	}
}

// IsMaxRetries checks whether the error is due to hitting the
// maximum number of retries or not.
func IsMaxRetries(err error) bool {
	if merr, ok := err.(*multierror.Error); ok {
		if len(merr.Errors) == 0 {
			return false
		}
		return merr.Errors[len(merr.Errors)-1] == errMaxRetriesReached
	}
	return err == errMaxRetriesReached
}

// Limited try to do fn in limit times and will sleep in (logarithmic) duration of unit (millisecond, second,etc) and
// not more than unit * maxSleep.
// The return channel stream's fn error in each iteration and can be use for blocking or/and if caller is interested
func Limited(limit int, unit time.Duration, maxSleep uint, fn func() error) <-chan error {
	r := make(chan error)
	go func() {
		ctx, cl := context.WithCancel(context.Background())
		t := WithCancel(ctx, unit, maxSleep, fn)
		defer cl()
		defer close(r)
		for i := 0; i < limit; i++ {
			r <- <-t
		}
	}()
	return r
}

// WithCancel try to do fn until context will cancel or fn return nil and will sleep in (logarithmic) duration of
// unit (millisecond, second,etc) and not more than unit * maxSleep.
// The return channel stream's fn error in each iteration and can be use for blocking or/and if caller is interested
func WithCancel(ctx context.Context, unit time.Duration, maxSleep uint, fn func() error) <-chan error {
	f := fibonacci(maxSleep)
	c := time.After(time.Duration(f()) * unit)
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
					c = time.After(time.Duration(f()) * unit)
					continue
				}
				return
			}
		}
	}()
	return r
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
