package sqs

import (
	"context"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/okian/servo/v2/broker"
	"github.com/spf13/viper"
)

type message struct {
	b   []byte
	com func() error
}

func (m *message) Payload() []byte {
	return m.b
}

func (m *message) Commit() error {
	return m.com()
}

type service struct {
	sqs *sqs.SQS
}

func (s *service) Name() string {
	return "sqs"
}

func (s *service) Initialize(ctx context.Context) error {
	sn, err := newSession()
	if err != nil {
		return err
	}
	s.sqs = sqs.New(sn)

	return broker.Register(s)
}

func (s *service) Finalize() error {
	return nil
}

func (s *service) Publish(ctx context.Context, topic string, msg []byte) (string, error) {
	send_resp, err := s.sqs.SendMessage(&sqs.SendMessageInput{
		MessageBody:    aws.String(string(msg)), // Required
		QueueUrl:       aws.String(topic),       // Required
		MessageGroupId: aws.String("a"),
	})
	if err != nil {
		return "", err
	}
	return *send_resp.MessageId, nil
}

func (s *service) Consume(ctx context.Context, topic string) <-chan broker.Message {
	msg := make(chan broker.Message)
	go func() {
		parm := &sqs.ReceiveMessageInput{
			AttributeNames:          nil,
			MaxNumberOfMessages:     aws.Int64(1),
			MessageAttributeNames:   nil,
			QueueUrl:                aws.String(topic),
			ReceiveRequestAttemptId: nil,
			VisibilityTimeout:       aws.Int64(90),
		}
		for {
			out, err := s.sqs.ReceiveMessage(parm)
			if err != nil {
				panic(err.Error())
			}
			for _, m := range out.Messages {
				msg <- &message{
					com: func() error {
						delete_params := &sqs.DeleteMessageInput{
							QueueUrl:      aws.String(topic), // Required
							ReceiptHandle: m.ReceiptHandle,   // Required
						}
						if _, err := s.sqs.DeleteMessage(delete_params); err != nil {
							return err
						}
						return nil
					},
					b: []byte(*m.Body),
				}
			}
		}
	}()
	return msg
}

func newSession() (*session.Session, error) {
	reg := viper.GetString("aws_region")
	cfg := &aws.Config{
		Region: &reg,
		Credentials: credentials.NewStaticCredentials(
			viper.GetString("aws_access_id"),
			viper.GetString("aws_access_secret"),
			viper.GetString("aws_access_token"),
		),
	}
	return session.NewSession(cfg)
}
