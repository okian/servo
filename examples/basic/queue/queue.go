// Package queue stands in for a real queue client (e.g. *sqs.Client from
// the AWS SDK) — sending is simulated with a formatted string so the
// example stays dependency-free.
package queue

import "fmt"

type Client struct{ Account string }

func (c *Client) Send(msg string) string {
	return fmt.Sprintf("[account %s] %s", c.Account, msg)
}

// OrdersAccount and AuditAccount are distinct types precisely so servo's
// type-based identity treats them as two separate, unambiguous graph
// nodes — both wrap the same underlying Client, each constructed against
// a different AWS account's credentials. See relay.Relay for why one
// component legitimately needs both at once.
type OrdersAccount struct{ *Client }
type AuditAccount struct{ *Client }

func NewOrdersAccount() *OrdersAccount {
	return &OrdersAccount{Client: &Client{Account: "111111111111"}}
}

func NewAuditAccount() *AuditAccount {
	return &AuditAccount{Client: &Client{Account: "222222222222"}}
}
