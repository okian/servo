package main

import (
	"strings"
	"testing"
)

func TestRunNewComponent(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runNew("component", []string{"Widget"}); err != nil {
			t.Fatalf("runNew: %v", err)
		}
	})
	for _, want := range []string{
		"type Widget struct{}",
		"func NewWidget() *Widget {",
		"return &Widget{}",
		"func (c *Widget) Init(ctx context.Context) error",
		"func (c *Widget) Ready(ctx context.Context) error",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("component scaffold missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunNewAdapter(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runNew("adapter", []string{"redis"}); err != nil {
			t.Fatalf("runNew: %v", err)
		}
	})
	for _, want := range []string{
		"package redis",
		"type Config struct",
		"func New(cfg Config) (*Client, func(), error)",
		"func (c *Client) Stop(ctx context.Context) error",
		"func (c *Client) Health(ctx context.Context) error",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("adapter scaffold missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunNewUnknownKind(t *testing.T) {
	err := runNew("bogus", []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("got err=%v, want an 'unknown kind' error", err)
	}
}

func TestRunNewComponentWrongArgCount(t *testing.T) {
	if err := runNew("component", nil); err == nil || !strings.Contains(err.Error(), "usage: servo new component") {
		t.Errorf("runNew(component, no args): got %v, want a usage error", err)
	}
	if err := runNew("component", []string{"A", "B"}); err == nil || !strings.Contains(err.Error(), "usage: servo new component") {
		t.Errorf("runNew(component, 2 args): got %v, want a usage error", err)
	}
}

func TestRunNewAdapterWrongArgCount(t *testing.T) {
	if err := runNew("adapter", nil); err == nil || !strings.Contains(err.Error(), "usage: servo new adapter") {
		t.Errorf("runNew(adapter, no args): got %v, want a usage error", err)
	}
}
