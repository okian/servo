package mini

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"

	"github.com/okian/servo/v3/cfg"
)

func (s *service) Load(ctx context.Context, path string) (io.ReadCloser, error) {
	return s.mi.GetObject(ctx, cfg.GetString("vol_bucket"), path, minio.GetObjectOptions{})
}

func (s *service) Save(ctx context.Context, path string, file io.Reader) error {
	buf := &bytes.Buffer{}
	nRead, err := io.Copy(buf, file)
	if err != nil {
		fmt.Println(err)
	}
	_, err = s.mi.PutObject(ctx, cfg.GetString("vol_bucket"), path, buf, nRead, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return err
}

func (s *service) Delete(ctx context.Context, path string) error {
	return s.mi.RemoveObject(ctx, cfg.GetString("vol_bucket"), path, minio.RemoveObjectOptions{})
}

func (s *service) Exist(ctx context.Context, path string) (bool, error) {
	if _, err := s.mi.StatObject(ctx, cfg.GetString("vol_bucket"), path, minio.GetObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
