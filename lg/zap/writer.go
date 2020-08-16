package zap

import (
	"os"
)

type writeSyncer struct {
	file *os.File
}

func (w *writeSyncer) Write(p []byte) (n int, err error) {
	o, err := os.Stdout.Write(p)
	if err != nil || w.file == nil {
		return o, err
	}
	return w.file.WriteString(string(p))
}

func (w *writeSyncer) Sync() error {
	_ = os.Stdout.Sync()
	if w.file == nil {
		return nil
	}
	return w.file.Sync()

}
