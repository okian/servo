package limiter

import (
	"context"
	"testing"
	"time"
)

func BenchmarkLimiter(t *testing.B) {
	s := new("test", WithRPS(1), WithMaxQueue(0))
	ctx, cl := context.WithCancel(context.Background())
	defer cl()
	if err := s.Initialize(ctx); err != nil {
		t.Fatal(err.Error())
	}

	t.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Allow(ctx)
		}
	})
}

func TestLimiter(t *testing.T) {
	s := new("test", WithRPS(200), WithTimeout(time.Second*1), WithMaxQueue(100))
	ctx, cl := context.WithCancel(context.Background())
	if err := s.Initialize(ctx); err != nil {
		t.Fatal(err.Error())
	}

	var al, nl uint
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-s.AllowChan(ctx):
				if r {
					al++
					continue
				}
				nl++
			}
		}
	}()

	time.Sleep(time.Millisecond * 2002)
	cl()
	t.Log(al, nl)
}
