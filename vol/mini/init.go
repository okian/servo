package mini

import (
	"github.com/okian/servo/v3"
	"github.com/okian/servo/v3/vol"
)

func init() {
	sv := &service{}
	vol.Register(sv)
	servo.Register(sv, 100)
}
