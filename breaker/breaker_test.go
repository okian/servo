package breaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBreaker(t *testing.T) {
	s := new("test",
		WithBuffer(2),
		WithPrometheus(),
		WithInitChance(100),
		WithUpdate(time.Millisecond*10),
		WithThreshold(5))
	ctx, cl := context.WithCancel(context.Background())
	a, e := s.Allow, s.Event
	if err := s.Initialize(ctx); err != nil {
		t.Fatal(err.Error())
	}
	var cnt, alw, nwl int
	go func(ctx context.Context) {
		for {
			select {
			case e() <- errors.New("tmp"):
				cnt++
			case b := <-a():
				if b {
					alw++
					continue
				}
				nwl++
			case <-ctx.Done():
				return
			}
		}
	}(ctx)
	<-time.After(time.Second * 3)
	s.Lock()
	t.Log(cnt, alw, nwl)
	if s.chance < 0 {
		t.Errorf("chace should be 100 but it is %d", s.chance)
	}
	s.Unlock()
	cl()

}
