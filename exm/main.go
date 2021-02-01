package main

import (
	"context"
	"time"

	"github.com/okian/servo/v2"
	_ "github.com/okian/servo/v2/config"
	_ "github.com/okian/servo/v2/kv/redis"
	_ "github.com/okian/servo/v2/lg"
	_ "github.com/okian/servo/v2/rest"
)

type Ss struct {
	Name string
}

func main() {
	ctx, cl := context.WithCancel(context.Background())
	defer cl()
	defer servo.Initialize(ctx)()
	<-time.After(time.Hour)

}
func init() {
}
