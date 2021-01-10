package zap

func (s *service) Info(args ...interface{}) {
	s.logger.Info(args...)
}

func (s *service) Debug(args ...interface{}) {
	s.logger.Debug(args...)
}

func (s *service) Warn(args ...interface{}) {
	s.logger.Warn(args...)
}

func (s *service) Error(args ...interface{}) {
	s.logger.Error(args...)
}

func (s *service) Panic(args ...interface{}) {
	s.logger.Panic(args...)
}

func (s *service) Fatal(args ...interface{}) {
	s.logger.Fatal(args...)
}

func (s *service) Infof(template string, args ...interface{}) {
	s.logger.Infof(template, args...)
}

func (s *service) Debugf(template string, args ...interface{}) {
	s.logger.Debugf(template, args...)
}

func (s *service) Warnf(template string, args ...interface{}) {
	s.logger.Warnf(template, args...)
}

func (s *service) Errorf(template string, args ...interface{}) {
	s.logger.Errorf(template, args...)
}

func (s *service) Panicf(template string, args ...interface{}) {
	s.logger.Panicf(template, args...)
}

func (s *service) Fatalf(template string, args ...interface{}) {
	s.logger.Fatalf(template, args...)
}

func (s *service) Infow(template string, keysAndValues ...interface{}) {
	s.logger.Infow(template, keysAndValues...)
}

func (s *service) Debugw(template string, keysAndValues ...interface{}) {
	s.logger.Debugw(template, keysAndValues...)
}

func (s *service) Warnw(template string, keysAndValues ...interface{}) {
	s.logger.Warnw(template, keysAndValues...)
}

func (s *service) Errorw(template string, keysAndValues ...interface{}) {
	s.logger.Errorw(template, keysAndValues...)
}

func (s *service) Panicw(template string, keysAndValues ...interface{}) {
	s.logger.Panicw(template, keysAndValues...)
}

func (s *service) Fatalw(template string, keysAndValues ...interface{}) {
	s.logger.Fatalw(template, keysAndValues...)
}
