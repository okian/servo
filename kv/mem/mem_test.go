package mem_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/okian/servo/v2/kv/mem"
)

func TestBits(t *testing.T) {
	Convey("incr", t, func() {
		ctx, cl := context.WithTimeout(context.Background(), time.Second*60)
		defer cl()
		kv := mem.New(ctx)
		v, err := kv.Incr("a", 1, time.Second*2)
		So(err, ShouldBeNil)
		So(v, ShouldEqual, 1)
		v, err = kv.Incr("a", 1, time.Second*2)
		So(err, ShouldBeNil)
		So(v, ShouldEqual, 2)
		time.Sleep(time.Second * 6)
		v, err = kv.Incr("a", 1, time.Second*2)
		So(err, ShouldBeNil)
		So(v, ShouldEqual, 1)
	})
}

type Person struct {
	Name     string
	LastName string
	Age      int
	Email    string
}

func TestMSet(t *testing.T) {
	Convey("test mset", t, func() {

		ctx, cl := context.WithTimeout(context.Background(), time.Second*60)
		defer cl()
		kv := mem.New(ctx)
		err := kv.MSet("john", Person{
			Name:     "John",
			LastName: "Doe",
			Age:      45,
			Email:    "John@Doe.com",
		}, time.Second*2)
		So(err, ShouldBeNil)
		p := new(Person)
		err = kv.MGet("john", p)
		So(err, ShouldBeNil)

		So(p.Name, ShouldEqual, "John")
		So(p.LastName, ShouldEqual, "Doe")
		So(p.Age, ShouldEqual, 45)
		So(p.Email, ShouldEqual, "John@Doe.com")
	})

}

func BenchmarkMSet(b *testing.B) {
	ctx, cl := context.WithTimeout(context.Background(), time.Second*60)
	defer cl()
	kv := mem.New(ctx)
	for n := 0; n < b.N; n++ {
		_ = kv.MSet(fmt.Sprintf("%d", n), Person{
			Name:     "John",
			LastName: "Doe",
			Age:      n,
			Email:    "John@Doe.com",
		}, time.Second*2)
	}
}

func BenchmarkBits(b *testing.B) {
	ctx, cl := context.WithTimeout(context.Background(), time.Second*60)
	defer cl()
	kv := mem.New(ctx)
	var v int
	for n := 0; n < b.N; n++ {
		v, _ = kv.Incr("a", 1, time.Second*2)
	}
	fmt.Println(v)

}
