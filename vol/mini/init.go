package mini

import (
	"github.com/okian/servo/v2"
	"github.com/okian/servo/v2/vol"
)

func init() {
	sv := &service{}
	vol.Register(sv)
	servo.Register(sv, 100)
}
