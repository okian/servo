package vol

import (
	"context"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/spf13/viper"
)

var mi *minio.Client

func Load(ctx context.Context, path string) (io.ReadCloser, error) {
	return mi.GetObject(ctx, viper.GetString("vol_bucket"), path, minio.GetObjectOptions{})
}

func Save(ctx context.Context, path string, file io.Reader) (string, error) {
	info, err := mi.PutObject(ctx, viper.GetString("vol_bucket"), path, file, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return info.Key, err
}

func Delete(ctx context.Context, path string) error {
	return mi.RemoveObject(ctx, viper.GetString("vol_bucket"), path, minio.RemoveObjectOptions{})
}

func Exist(ctx context.Context, path string) (bool, error) {
	if _, err := mi.StatObject(ctx, viper.GetString("vol_bucket"), path, minio.GetObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
