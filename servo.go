package servo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"sort"
	"sync"

	"github.com/jedib0t/go-pretty/v6/table"
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
	log.Println(fmt.Sprintf("registring service  %q", service.Name()))
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
				v, ok := s.(Healthiness)
				if !ok {
					return
				}
				r, e = v.Healthy(ctx)
			case ready:
				v, ok := s.(Readiness)
				if !ok {
					return
				}
				r, e = v.Ready(ctx)
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
	//	registered()
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
		log.Println(fmt.Sprintf("initializing services with order %d\n", i))
		if e := run(ctx, Start, register[i]); e != nil {
			log.Println(fmt.Sprintf("service %q returned error: %s\n", i, e.Error()))
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
				log.Println(fmt.Sprintf("initializing %s\n", c.Name()))
				if err = c.Initialize(ctx); err == nil {
					log.Println(fmt.Sprintf("%s initialized\n", c.Name()))
					serviceNames[c.Name()] = true
				} else {
					log.Println(fmt.Sprintf("%s failed to initialize: %s\n", c.Name(), err.Error()))
				}

			} else if mode == Stop {
				if !serviceNames[c.Name()] {
					return
				}
				if f, ok := c.(Finalizer); ok {
					log.Println(fmt.Sprintf("finalizing %s\n", c.Name()))
					err = f.Finalize()
				}

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

func init() {
	log.SetOutput(os.Stdout)
	log.SetFlags(1 | 2)
	log.SetPrefix("servo: ")
}

func registered() {
	t := table.NewWriter()
	t.SetStyle(table.StyleBold)
	t.SetOutputMirror(os.Stdout)
	t.AppendHeader(table.Row{"Order", "Name", "Finalizer", "Healthiness", "Readiness"})
	var rs []table.Row
	for k, ss := range register {
		for _, s := range ss {
			rs = append(rs, table.Row{
				k, s.Name(), icon(s, (*Finalizer)(nil)), icon(s, (*Healthiness)(nil)), icon(s, (*Readiness)(nil)),
			})
		}
	}
	t.AppendSeparator()
	t.Render()
}

func icon(s Service, n interface{}) string {
	ti := reflect.TypeOf(s).Elem()
	if reflect.TypeOf(n).Implements(ti) {
		return "✅"
	}
	return "❌"
}
