package aws

import (
	"context"
	"io"

	"github.com/aws/aws-sdk-go/aws"
	_ "github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	_ "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	_ "github.com/aws/aws-sdk-go/service/s3"
	_ "github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type service struct {
}

func (s *service) Load(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, nil
}

func (s *service) Save(_ context.Context, _ string, _ io.Reader) error {
	return nil
}

func (s *service) Delete(_ context.Context, _ string) error {
	panic("not implemented") // TODO: Implement
}

func (s *service) Exist(_ context.Context, _ string) (bool, error) {
	panic("not implemented") // TODO: Implement
}

func (s *service) Name() string {
	return "s3 cloud storage"
}

func (s *service) Initialize(ctx context.Context) error {

	// All clients require a Session. The Session provides the client with
	// shared configuration such as region, endpoint, and credentials. A
	// Session should be shared where possible to take advantage of
	// configuration and credential caching. See the session package for
	// more information.
	sess := session.Must(session.NewSession())

	// Create a new instance of the service's client with a Session.
	// Optional aws.Config values can also be provided as variadic arguments
	// to the New function. This option allows you to provide service
	// specific configuration.
	svc := s3.New(sess)

	_, err := svc.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(""),
		Key:    aws.String(""),
		Body:   nil,
	})
	return err
}

func (s *service) Finalize() error {
	panic("not implemented") // TODO: Implement
}
