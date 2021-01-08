package monitoring

import (
	"strings"

	"github.com/okian/servo/v2"
)

func init() {
	servo.Register(&service{}, 100)
}

func snakeCase(s string) string {
	r := strings.NewReplacer("-", "_", ".", "_")
	s = r.Replace(s)
	return strings.ToLower(s)
}
