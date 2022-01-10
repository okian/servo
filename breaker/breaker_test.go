package breaker

import (
	"context"
	"testing"
	"time"
)

// I know it's not proper test. I'll fix it later (hopefully) ;)
func TestBreaker(t *testing.T) {
	s := new("test",
		WithAutoTune(OPTIMISTIC, 3, 100),
		WithAutoTuneENV(),
		WithThreshold(5),
		WithThresholdENV(),
		WithBuffer(2),
		WithBufferENV(),
		WithMaxStep(10),
		WithMinStep(1),
		WithInitChance(100),
		WithUpdate(time.Millisecond*10),
		WithThreshold(5))
	ctx, cl := context.WithCancel(context.Background())
	if err := s.Initialize(ctx); err != nil {
		t.Fatal(err.Error())
	}
	a, e := s.Allow, s.Event

	var cnt, alw, nwl int
	go func(ctx context.Context) {
		for {
			select {
			case e() <- nil:
				cnt++
			case <-ctx.Done():
				return
			default:
				b := a()
				if b {
					alw++
					continue
				}
				nwl++
			}
		}
	}(ctx)
	<-time.After(time.Second * 3)
	s.Lock()
	t.Log(cnt, alw, nwl, s.chance)
	if s.chance < 0 {
		t.Errorf("chace should be 100 but it is %d", s.chance)
	}
	s.Unlock()
	cl()

}
