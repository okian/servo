package servo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type report int

const (
	ALL report = iota
	NONE
	NOTNIL
)

var (
	ErrorTimeout        = errors.New("timeout")
	ErrorInitialized    = errors.New("services are already initialized")
	ErrorNotInitialized = errors.New("services are not initialized")
	ErrorCancelled      = errors.New("context canceled")
	ErrorFinalized      = errors.New("services are already finalized")
)
var (
	timeout      = time.Second * 10
	register     = make(map[int][]Service)
	registerLock = sync.RWMutex{}
	initialized  = false
	finalized    = false
	flatOnce     = sync.Once{}
	services     []Service
	serviceNames = make(map[string]bool)
)

type checkType int

const (
	health checkType = iota
	ready
)

type runMode int

const (
	Start runMode = 1 << iota
	Stop
)

func Register(service Service, order int) {
	registerLock.Lock()
	defer registerLock.Unlock()
	fmt.Println(fmt.Sprintf("registring service  %q", service.Name()))
	if initialized {
		panic(ErrorInitialized)
	}
	if _, ok := serviceNames[service.Name()]; ok {
		panic(fmt.Errorf("service with name: %q has been registered", service.Name()))
	}
	serviceNames[service.Name()] = false
	if k, ok := register[order]; ok {
		register[order] = append(k, service)
	} else {
		register[order] = []Service{service}
	}
}

func flatServices() {
	flatOnce.Do(func() {
		services = make([]Service, 0)
		for _, v := range register {
			services = append(services, v...)
		}
	})
}

var mode report = NOTNIL

func check(ctx context.Context, rt checkType) (map[string]interface{}, error) {
	registerLock.RLock()
	defer registerLock.RUnlock()
	if !initialized {
		return nil, ErrorNotInitialized
	}
	if finalized {
		return nil, ErrorFinalized
	}
	flatServices()

	res := make(map[string]interface{})
	l := sync.Mutex{}
	wg := sync.WaitGroup{}
	wg.Add(len(services))
	done := make(chan bool, 0)
	erc := make(chan error, 0)
	go func() {
		wg.Wait()
		close(done)
	}()
	for _, sv := range services {
		go func(s Service) {
			defer wg.Done()

			var r interface{}
			var e error

			switch rt {
			case health:
				r, e = s.Healthy(ctx)
			case ready:
				r, e = s.Ready(ctx)
			default:
				panic(fmt.Sprintf("[BUG]: unknown report type %v", rt))
			}

			l.Lock()
			defer l.Unlock()
			switch mode {
			case ALL:
				res[s.Name()] = r
			case NOTNIL:
				if r != nil {
					res[s.Name()] = r
				}
			case NONE:
				break
			default:
				panic("[BUG]")
			}

			if e != nil {
				erc <- e
			}
		}(sv)
	}

	select {
	case err := <-erc:
		return res, err
	case <-done:
		return res, nil
	case <-ctx.Done():
		return nil, ErrorCancelled
	}
}

func Ready(ctx context.Context) (map[string]interface{}, error) {
	return check(ctx, ready)
}

func Health(ctx context.Context) (map[string]interface{}, error) {
	return check(ctx, health)
}

func Initialize(ctx context.Context) func() {
	fmt.Fprintf(os.Stdout, "starting initializition\n")
	registerLock.Lock()
	defer registerLock.Unlock()
	if initialized {
		panic(ErrorInitialized.Error())
	}
	initialized = true
	var ks = make([]int, 0)
	for k := range register {
		ks = append(ks, k)
	}
	sort.Ints(ks)

	for _, i := range ks {
		fmt.Fprintf(os.Stdout, "initializing services with order %d\n", i)

		if e := run(ctx, Start, register[i]); e != nil {
			finalize()
			panic(e.Error())
		}
	}
	return finalize
}

func finalize() {
	registerLock.Lock()
	defer registerLock.Unlock()
	if finalized {
		return
	}
	finalized = true
	var ks = make([]int, len(register))
	for k := range register {
		ks = append(ks, k)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ks)))
	for _, i := range ks {
		_ = run(context.Background(), Stop, register[i])
	}
}

func run(ctx context.Context, mode runMode, svc []Service) error {
	wg := sync.WaitGroup{}
	wg.Add(len(svc))
	t := time.After(timeout)
	done := make(chan bool)
	go func() {
		wg.Wait()
		close(done)
	}()
	ers := make(chan error)
	for _, s := range svc {
		go func(c Service) {
			defer wg.Done()
			var err error
			if mode == Start {
				fmt.Fprintf(os.Stdout, "initializing %s\n", c.Name())

				if err = c.Initialize(ctx); err == nil {
					fmt.Fprintf(os.Stdout, "%s failed to initialized\n", c.Name())
					serviceNames[c.Name()] = true
				}

			} else if mode == Stop {
				if !serviceNames[c.Name()] {
					return
				}
				fmt.Fprintf(os.Stdout, "finalizing %s\n", c.Name())
				err = c.Finalize()
			}
			if err != nil {
				ers <- err
			}
		}(s)
	}
	select {
	case err := <-ers:
		return err
	case <-ctx.Done():
		return ErrorCancelled
	case <-t:
		return ErrorTimeout
	case <-done:
		return nil
	}
}

func HealthHandler(w http.ResponseWriter, _ *http.Request) {
	rep, err := Health(context.Background())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rep)
}
func ReadinessHandler(w http.ResponseWriter, _ *http.Request) {
	rep, err := Ready(context.Background())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rep)
}
