package main

import (
	"context"
	"time"

	"github.com/okian/servo/v2"
	"github.com/okian/servo/v2/kv"
	_ "github.com/okian/servo/v2/kv"
	_ "github.com/okian/servo/v2/kv/redis"
	"github.com/okian/servo/v2/lg"
	_ "github.com/okian/servo/v2/lg/zap"
	// DONT FORGET THIS (needed for invoking init function)
	//	_ "github.com/okian/servo/v2/v2/example/broker"
)

type msg string
type local string

const (
	fa local = "fa"
	en local = "en"
)

func (m msg) in(l local) string {
	return string(m)
}

func (m msg) with(ctx context.Context) msg {
	return m
}

type kk struct {
	Name string
	Age  int
}

func main() {
	ctx, cl := context.WithCancel(context.Background())
	defer cl()

	defer servo.Initialize(ctx)()
	err := kv.BitSet("ss", 10, true, time.Second*20)
	if err != nil {
		lg.Panic(err)
	}
	b, err := kv.BitGet("ss", 10)
	if err != nil {
		lg.Panic(err)
	}
	lg.Info(b)

	t, err := kv.TTL("ss")
	if err != nil {
		lg.Panic(err)
	}
	lg.Info(t)

	err = kv.MSet("tmset", kk{"Kian", 37}, time.Second*10)
	if err != nil {
		lg.Panic(err)
	}
	var k = &kk{}
	err = kv.MGet("tmset", k)

	if err != nil {
		lg.Panic(err)
	}
	lg.Info(k)

	err = kv.Set("tset", "234", time.Second*10)

	if err != nil {
		lg.Panic(err)
	}
	var m string
	err = kv.Get("tset", &m)

	if err != nil {
		lg.Panic(err)
	}
	lg.Info(m)

}
