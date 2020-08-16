package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/okian/servo"
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
func main() {

	ctx, cl := context.WithCancel(context.Background())
	defer cl()

	//z, err := zap.NewDevelopment()
	//if err != nil {
	//	panic(err)
	//}
	//z.Sugar().Debugw("eellow ", "num2", 2, "num4", 4, "ctx", ctx)
	//os.Exit(0)
	servo.Initialize(ctx)

	lg.Debugw("eellow ", "num2", 2, "num4", 4, "ctx", ctx)
	lg.Errorw("name iswww", "test", 33)
	fmt.Println("eee")
	lg.Infow("name iswww", "test", 33)
	lg.Warnw("name iswww", "test", 33)
	lg.Debugw("name iswww", "test", 33)

	rp, err := servo.Health(ctx)
	if err != nil {
		panic(err.Error())
	}
	b, err := json.Marshal(rp)
	fmt.Println(string(b))
}
