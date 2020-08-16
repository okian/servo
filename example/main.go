package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/okian/servo"
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
func main() {

	ctx, cl := context.WithCancel(context.Background())
	defer cl()

	defer servo.Initialize(ctx)()
	rp, err := servo.Health(ctx)
	if err != nil {
		panic(err.Error())
	}
	b, err := json.Marshal(rp)
	if err != nil {
		panic(err.Error())
	}
	fmt.Println(string(b))
}
