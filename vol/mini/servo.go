package mini

import (
	"context"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/okian/servo/v2/config"
)

type service struct {
	mi *minio.Client
}

func (s *service) Name() string {
	return "vol"
}

func (s *service) Initialize(ctx context.Context) error {

	c, err := minio.New(config.GetString("vol_server"), &minio.Options{
		Creds: credentials.NewStaticV4(config.GetString("vol_id"),
			config.GetString("vol_secret"),
			config.GetString("vol_token")),
		Secure: config.GetBool("vol_secure"),
	})
	ok, err := c.BucketExists(ctx, config.GetString("vol_bucket"))
	if err != nil {
		return err
	}

	if !ok {
		if err = c.MakeBucket(ctx, config.GetString("vol_bucket"), minio.MakeBucketOptions{}); err != nil {
			return err
		}
	}

	s.mi = c
	return nil
}

func (s *service) Finalize() error {
	return nil
}
