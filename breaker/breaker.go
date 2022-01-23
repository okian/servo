package breaker

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/okian/servo/v2"
	p1 "github.com/okian/servo/v2/monitoring/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/viper"
)

type (
	Mood   int
	Option func(s *service) error
)

const (
	NOOP Mood = iota
	OPTIMISTIC
	PESSIMISTIC
)

type service struct {
	name    string
	options []Option
	ticker  <-chan time.Time
	// the allowed percentage to pass for example if the value is set to 7 any
	// request with the chance of 93% or more will pass
	threshold      uint
	updateDuration time.Duration
	ignoreEvents   []error
	chance         int
	eCounter,
	nCounter int
	ctx       context.Context
	allowance chan bool
	events    chan error
	sync.RWMutex
	chanceMonitoring    func(c float64)
	statusMonitoring    func(s string)
	allowanceMonitoring func(bool)
	autoTune            func(int) int
	maxStep             int
	minStep             int
}

func (s *service) Name() string {
	return strings.ToLower(s.name) + "_breaker"
}

func (s *service) Initialize(ctx context.Context) error {
	s.ctx = ctx
	s.threshold = 0
	s.updateDuration = time.Second
	s.ignoreEvents = nil
	s.chance = 50
	s.chanceMonitoring = noopChanceMonitoring
	s.statusMonitoring = noopStatusMonitoring
	s.allowanceMonitoring = noopAllowanceMonitoring
	s.autoTune = noopautoTune
	s.minStep = 1
	s.maxStep = 5
	for i := range s.options {
		if err := s.options[i](s); err != nil {
			return err
		}
	}
	if s.maxStep < s.minStep {
		return fmt.Errorf("%s: max step (%d) should be bigger then min step (%d)", s.Name(), s.maxStep, s.minStep)
	}
	s.ticker = time.Tick(s.updateDuration)
	go s.rate()
	go s.status()
	go s.autoBalance()
	return nil
}

func (s *service) Finalize() error {
	return nil
}

func (s *service) rate() {
	for {
		s.RLock()
		var ch = s.chance
		s.RUnlock()
		select {
		case <-s.ctx.Done():
			return
		case s.allowance <- s.allow(ch):
		}
	}
}

func (s *service) status() {
BIG:
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.ticker:
			s.Lock()
			s.chance = s.calculate(s.chance, s.eCounter, s.nCounter)
			s.chanceMonitoring(float64(s.chance))
			s.Unlock()
			s.eCounter, s.nCounter = 0, 0
		case err := <-s.events:
			if err == nil {
				s.statusMonitoring("OK")
				s.nCounter++
				continue BIG
			}
			s.statusMonitoring(err.Error())
			for _, e := range s.ignoreEvents {
				if errors.Is(err, e) {
					continue BIG
				}
			}
			s.eCounter++
		}
	}
}

func new(name string, ops ...Option) *service {
	if !nameRex.Match([]byte(name)) {
		panic("name must match ^[a-zA-Z_]+$ regular expression")
	}
	return &service{
		name:      name,
		options:   ops,
		allowance: make(chan bool),
		events:    make(chan error),
	}
}

var nameRex = regexp.MustCompile("^[a-zA-Z_]+$")

type Interface interface {
	Allow() bool
	Event() chan<- error
}

func NewService(name string, ops ...Option) Interface {
	s := new(name, ops...)
	servo.Register(s, 500)
	return s
}

func (s *service) Allow() bool {
	r := <-s.allowance
	s.allowanceMonitoring(r)
	return r
}

func (s *service) Event() chan<- error {
	return s.events
}

func WithIgnoreErrors(er ...error) Option {
	return func(s *service) error {
		s.ignoreEvents = er
		return nil
	}
}

func WithMaxStep(c uint8) Option {
	return func(s *service) error {
		if c > 100 {
			return fmt.Errorf("%s: %d is invalid value for max step, it should be between 1 and 100", s.Name(), c)
		}
		s.maxStep = int(c)
		return nil
	}
}

func WithMinStep(c uint8) Option {
	return func(s *service) error {
		if c < 1 {
			return fmt.Errorf("%s: %d is invalid value for min step, it should be between 1 and 100", s.Name(), c)
		}
		s.minStep = int(c)
		return nil
	}
}

func WithInitChance(c uint8) Option {
	return func(s *service) error {
		if c > 100 {
			return fmt.Errorf("%s: %d is invalid value for chance, it should be between 0 and 100", s.Name(), c)
		}
		s.chance = int(c)
		return nil
	}
}

func WithInitChanceENV() Option {
	return func(s *service) error {
		k := fmt.Sprintf("%s_init_chance", s.Name())
		if !viper.IsSet(k) {
			return nil
		}
		v := viper.GetInt(k)
		if v < 0 || v > 100 {
			return fmt.Errorf("%s: %d is invalid value for chance, it should be between 0 and 100", s.Name(), v)
		}
		s.chance = v
		return nil
	}
}

func WithUpdate(u time.Duration) Option {
	return func(s *service) error {
		if u < time.Millisecond*10 {
			return fmt.Errorf("%s: %s is invalid update duration. it should be 10 millisecond or more", s.Name(), u)
		}
		s.updateDuration = u
		return nil
	}
}

func WithUpdateENV() Option {
	return func(s *service) error {
		k := fmt.Sprintf("%s_update", s.Name())
		if !viper.IsSet(k) {
			return nil
		}
		u := viper.GetDuration(k)
		if u < time.Millisecond*10 {
			return fmt.Errorf("%s: %s is invalid update duration. it should be 10 millisecond or more", s.Name(), u)
		}
		s.updateDuration = u
		return nil
	}
}

// WithThreshold is the allowed percentage to pass for example if the value is set to 7 any
// request with the chance of 93% or more will pass
func WithThreshold(t uint) Option {
	return func(s *service) error {
		if t < 2 || t > 99 {
			return fmt.Errorf("%s: %d is invalid value for chance, it should be between 2 and 99", s.Name(), t)
		}
		s.threshold = t
		return nil
	}
}

func WithThresholdENV() Option {
	return func(s *service) error {
		k := fmt.Sprintf("%s_threshold", s.Name())
		if !viper.IsSet(k) {
			return nil
		}
		t := viper.GetUint(k)
		if t < 2 || t > 99 {
			return nil
		}
		s.threshold = t
		return nil
	}
}

func WithBuffer(b uint) Option {
	return func(s *service) error {
		s.allowance = make(chan bool, b)
		return nil
	}
}

func WithBufferENV() Option {
	return func(s *service) error {
		k := fmt.Sprintf("%s_buffer", s.Name())
		if !viper.IsSet(k) {
			return nil
		}
		b := viper.GetInt(k)
		if b > 0 {
			s.allowance = make(chan bool, b)
		}
		return nil
	}
}

// WithAutoTune will help reduce / increase the odds in the absence of an event
func WithAutoTune(m Mood, step, until int) Option {
	return func(s *service) error {
		switch m {
		case PESSIMISTIC:
			s.autoTune = func(i int) int {
				if i < until {
					return i
				}
				if j := i - step; j < until {
					return j
				}
				return until
			}
		case OPTIMISTIC:
			s.autoTune = func(i int) int {
				if i > until {
					return i
				}
				if j := i + step; j > until {
					return j
				}
				return until
			}
		default:
			return fmt.Errorf("%s: unsupported mood", s.Name())
		}
		return nil
	}
}

// WithAutoTuneENV will help reduce / increase the odds in the absence of an event
func WithAutoTuneENV() Option {
	return func(s *service) error {
		km := fmt.Sprintf("%s_autotune_mood", s.Name())
		kt := fmt.Sprintf("%s_autotune_step", s.Name())
		ku := fmt.Sprintf("%s_autotune_until", s.Name())

		if !viper.IsSet(km) || !viper.IsSet(kt) || !viper.IsSet(ku) {
			return nil
		}

		m := Mood(viper.GetInt(km))
		t := viper.GetInt(kt)
		u := viper.GetInt(ku)

		switch m {
		case NOOP:
			s.autoTune = noopautoTune
		case PESSIMISTIC:
			s.autoTune = func(i int) int {
				if i < u {
					return i
				}
				if j := i - t; j < u {
					return j
				}
				return u
			}
		case OPTIMISTIC:
			s.autoTune = func(i int) int {
				if i > u {
					return i
				}
				if j := i + t; j > u {
					return j
				}
				return u
			}
		default:
			return fmt.Errorf("%s: unsupported mood env", s.Name())
		}
		return nil
	}
}

func WithPrometheus() Option {
	return func(s *service) error {
		ch := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: p1.Namespace(),
			Subsystem: s.Name(),
			Name:      "status",
		}, []string{"message"})
		s.statusMonitoring = func(s string) {
			ch.WithLabelValues(s).Inc()
		}

		ga := promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: p1.Namespace(),
			Subsystem: s.Name(),
			Name:      "chance",
		}, []string{})
		s.chanceMonitoring = func(c float64) {
			ga.WithLabelValues().Set(c)
		}
		al := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: p1.Namespace(),
			Subsystem: s.Name(),
			Name:      "allowance",
		}, []string{"value"})

		s.allowanceMonitoring = func(b bool) {
			al.WithLabelValues(fmt.Sprint(b)).Inc()
		}
		return nil
	}
}

func (s *service) calculate(chance int, err, none int) int {
	switch {
	case err != 0 && none != 0:
		if err > none {
			chance -= s.reach((err / none) % 100)
			break
		}
		chance += s.reach((none / err) % 100)
	case err != 0 && none == 0:
		chance -= s.reach(err)
	case err == 0 && none == 0:
		return s.autoTune(chance)
	case err == 0 && none != 0:
		chance += s.reach(none)
	}
	if chance < 0 {
		return 0
	}
	if chance >= 100 {
		return 100
	}
	return chance
}

func (s *service) reach(t int) int {
	if t > s.maxStep {
		return int(rand.Int31n(int32(s.maxStep)))
	}
	if t < s.minStep {
		return s.minStep
	}
	return t
}

// autoBalance is needed to escape 0 percent chance
func (s *service) autoBalance() {
	t := time.Tick(time.Second * 2)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t:
			s.allowance <- s.allow(-1)
		}
	}
}

func (s *service) allow(n int) bool {
	return n >= 100 || n < 0 || int(rand.Int31n(101-int32(s.threshold))) < n
}

func noopChanceMonitoring(float64) {}
func noopAllowanceMonitoring(bool) {}
func noopStatusMonitoring(string)  {}
func noopautoTune(i int) int       { return i }
