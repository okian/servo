package mini

import (
	"context"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

type service struct {
	mi *minio.Client
}

func (s *service) Name() string {
	return "vol"
}

func (s *service) Initialize(ctx context.Context) error {

	c, err := minio.New(viper.GetString("vol_server"), &minio.Options{
		Creds: credentials.NewStaticV4(viper.GetString("vol_id"),
			viper.GetString("vol_secret"),
			viper.GetString("vol_token")),
		Secure: viper.GetBool("vol_secure"),
	})
	if err != nil {
		return err
	}
	ok, err := c.BucketExists(ctx, viper.GetString("vol_bucket"))
	if err != nil {
		return err
	}

	if !ok {
		if err = c.MakeBucket(ctx, viper.GetString("vol_bucket"), minio.MakeBucketOptions{}); err != nil {
			return err
		}
	}

	s.mi = c
	return nil
}

func (s *service) Finalize() error {
	return nil
}
