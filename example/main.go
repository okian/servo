package main

import (
	"bytes"
	"context"
	"io/ioutil"
	"os"

	"github.com/okian/servo/v2"
	_ "github.com/okian/servo/v2/kv"
	_ "github.com/okian/servo/v2/kv/redis"
	"github.com/okian/servo/v2/lg"
	_ "github.com/okian/servo/v2/lg/zap"
	"github.com/okian/servo/v2/vol"
	_ "github.com/okian/servo/v2/vol/mem"
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
	b := bytes.NewReader([]byte("ssss"))
	f := "ff.txt"
	tt, err := vol.Exist(ctx, f)
	lg.Info(tt, err)
	err = vol.Save(ctx, f, b)
	if err != nil {
		lg.Error(err)
	}
	tt, err = vol.Exist(ctx, f)
	lg.Info(tt, err)
	r, err := vol.Load(ctx, f)
	if err != nil {
		panic(err)
	}
	bb, err := ioutil.ReadAll(r)
	lg.Error(string(bb), err)
	if err = vol.Delete(ctx, f); err != nil {
		lg.Error(err)
	}
	tt, err = vol.Exist(ctx, f)
	lg.Info(tt, err)
	os.Exit(0)

}
