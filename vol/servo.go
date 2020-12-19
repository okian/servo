package vol

import (
	"context"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

type service struct {
}

func (s *service) Name() string {
	return "vol"
}

func (s *service) Initialize(_ context.Context) error {

	c, err := minio.New(viper.GetString("vol_server"), &minio.Options{
		Creds: credentials.NewStaticV4(viper.GetString("vol_id"),
			viper.GetString("vol_secret"),
			viper.GetString("vol_token")),
		Secure: viper.GetBool("vol_secure"),
	})
	mi = c
	return err
}

func (s *service) Finalize() error {
	return nil
}
