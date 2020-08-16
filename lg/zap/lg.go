package zap

func (l *log) Info(args ...interface{}) {
	l.z.Info(args...)
}

func (l *log) Debug(args ...interface{}) {
	l.z.Debug(args...)
}

func (l *log) Warn(args ...interface{}) {
	l.z.Warn(args...)
}

func (l *log) Error(args ...interface{}) {
	l.z.Error(args...)
}

func (l *log) Panic(args ...interface{}) {
	l.z.Panic(args...)
}

func (l *log) Fatal(args ...interface{}) {
	l.z.Fatal(args...)
}

func (l *log) Infof(template string, args ...interface{}) {
	l.z.Infof(template, args...)
}

func (l *log) Debugf(template string, args ...interface{}) {
	l.z.Debugf(template, args...)
}

func (l *log) Warnf(template string, args ...interface{}) {
	l.z.Warnf(template, args...)
}

func (l *log) Errorf(template string, args ...interface{}) {
	l.z.Errorf(template, args...)
}

func (l *log) Panicf(template string, args ...interface{}) {
	l.z.Panicf(template, args...)
}

func (l *log) Fatalf(template string, args ...interface{}) {
	l.z.Fatalf(template, args...)
}

func (l *log) Infow(template string, keysAndValues ...interface{}) {
	l.z.Infow(template, keysAndValues...)
}

func (l *log) Debugw(template string, keysAndValues ...interface{}) {
	l.z.Debugw(template, keysAndValues...)
}

func (l *log) Warnw(template string, keysAndValues ...interface{}) {
	l.z.Warnw(template, keysAndValues...)
}

func (l *log) Errorw(template string, keysAndValues ...interface{}) {
	l.z.Errorw(template, keysAndValues...)
}

func (l *log) Panicw(template string, keysAndValues ...interface{}) {
	l.z.Panicw(template, keysAndValues...)
}

func (l *log) Fatalw(template string, keysAndValues ...interface{}) {
	l.z.Fatalw(template, keysAndValues...)
}
