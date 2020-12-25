package mem

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"

	"github.com/okian/servo/v2/vol"
)

var _ vol.Interface = &service{}

type service struct {
	bu map[string][]byte
}

func (s *service) Load(ctx context.Context, p string) (io.ReadCloser, error) {
	if r, err := s.Exist(ctx, p); err != nil || r == false {
		return nil, err
	}
	return ioutil.NopCloser(bytes.NewReader(s.bu[p])), nil
}

func (s *service) Save(_ context.Context, p string, i io.Reader) error {
	b, err := ioutil.ReadAll(i)
	if err != nil {
		return err
	}
	s.bu[p] = b
	return nil
}

func (s *service) Delete(_ context.Context, p string) error {
	delete(s.bu, p)
	return nil
}

func (s *service) Exist(_ context.Context, p string) (bool, error) {
	if _, ok := s.bu[p]; ok {
		return true, nil
	}
	return false, vol.NotFound
}

func init() {
	vol.Register(&service{
		bu: make(map[string][]byte),
	})
}
