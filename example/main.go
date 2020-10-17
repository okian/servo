package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/okian/servo"
	"github.com/okian/servo/kv"
	_ "github.com/okian/servo/kv"
	_ "github.com/okian/servo/kv/redis"
	"github.com/okian/servo/lg"
	_ "github.com/okian/servo/lg/zap"
	// DONT FORGET THIS (needed for invoking init function)
	//	_ "github.com/okian/servo/example/broker"
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
	key := "sss"

	var d kk
	err := kv.Get(key, &d)
	if err != nil {
		lg.Error(err)
	}

	fmt.Println(d)
	ee := kk{
		Name: "Ali",
		Age:  22,
	}
	err = kv.SetWithTTL(key, ee, time.Second*60)

	if err != nil {
		lg.Error(err)
	}
	err = kv.Get(key, &d)
	if err != nil {
		lg.Error(err)
	}
	fmt.Println(d)
	t, err := kv.TTL(key)
	fmt.Println(t, err)
	v, err := kv.Incr("ssss", 10, time.Second*00)
	if err != nil {
		panic(err)
	}
	fmt.Println(v)
	ts, err := kv.TTL("ssss")
	fmt.Println(ts, err)
	kv.Delete("ssss")
	ts2, err := kv.TTL("ssss")
	fmt.Println(ts2, err)
	rp, err := servo.Health(ctx)
	if err != nil {
		panic(err.Error())
	}
	b, err := json.Marshal(rp)
	if err != nil {
		panic(err.Error())
	}

	err = kv.SetWithTTL("user", "bambo", time.Second*10)
	if err != nil {
		panic(err)
	}

	var vl string
	err = kv.Get("user", &vl)

	if err != nil {
		panic(err)
	}

	fmt.Println(vl)

	fmt.Println(string(b))
}
