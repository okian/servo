package lg

import (
	"errors"
	"reflect"
	"sync"

	"github.com/okian/servo/lg/internal/logger"
)

type Interface interface {
	Name() string
	Info(args ...interface{})
	Debug(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Panic(args ...interface{})
	Fatal(args ...interface{})
	Infof(template string, args ...interface{})
	Debugf(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Errorf(template string, args ...interface{})
	Panicf(template string, args ...interface{})
	Fatalf(template string, args ...interface{})
	Infow(msg string, keysAndValues ...interface{})
	Debugw(msg string, keysAndValues ...interface{})
	Warnw(msg string, keysAndValues ...interface{})
	Errorw(msg string, keysAndValues ...interface{})
	Panicw(msg string, keysAndValues ...interface{})
	Fatalw(msg string, keysAndValues ...interface{})
}

var (
	once    sync.Once
	lock    sync.RWMutex
	loggers = map[string]Interface{"default_logger": &logger.Logger{}}
)

func removeDefault() {
	loggers = nil
}

func Register(i Interface) {
	lock.Lock()
	defer lock.Unlock()
	once.Do(removeDefault)
	if loggers == nil {
		loggers = map[string]Interface{
			i.Name(): i,
		}
		return
	}
	if _, ok := loggers[i.Name()]; ok {
		panic("lg: Register called twice for " + i.Name())
	}
	loggers[i.Name()] = i
}

func Info(args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Info(args...)
	}
}

func Debug(args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Debug(args...)
	}
}

func Warn(args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Warn(args...)
	}
}

func Error(args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Error(args...)
	}
}

func Panic(args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Panic(args...)
	}
}

func Fatal(args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Fatal(args...)
	}
}

// TODO: NOT TESTED, it's all guess work ;)
func call(name string, params ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for _, v := range loggers {
		f := reflect.ValueOf(v).MethodByName(name)
		if len(params) != f.Type().NumIn() {
			panic(errors.New("The number of params is not adapted."))
		}
		in := make([]reflect.Value, len(params))
		for k, param := range params {
			in[k] = reflect.ValueOf(param)
		}
		go f.Call(in)
	}
}

func Infof(template string, args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Infof(template, args...)
	}
}

func Debugf(template string, args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Debugf(template, args...)
	}
}

func Warnf(template string, args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Warnf(template, args...)
	}
}

func Errorf(template string, args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Errorf(template, args...)
	}
}

func Panicf(template string, args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Panicf(template, args...)
	}
}

func Fatalf(template string, args ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Fatalf(template, args...)
	}
}

func Infow(template string, keysAndValues ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Infow(template, keysAndValues...)
	}
}

func Debugw(template string, keysAndValues ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Debugw(template, keysAndValues...)
	}
}

func Warnw(template string, keysAndValues ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Warnw(template, keysAndValues...)
	}
}
func Errorw(template string, keysAndValues ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Errorw(template, keysAndValues...)
	}
}

// TODO it should/can't panic more that one time. fix this
func Panicw(template string, keysAndValues ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Panicw(template, keysAndValues...)
	}
}

func Fatalw(template string, keysAndValues ...interface{}) {
	lock.RLock()
	defer lock.RUnlock()
	for i := range loggers {
		loggers[i].Fatalw(template, keysAndValues...)
	}
}
