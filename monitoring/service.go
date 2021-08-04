package monitoring

import (
	"net/http"

	"github.com/okian/servo/v3/cfg"
)

type service struct {
	server *http.Server
}

func Namespace() string {
	return cfg.GetString("monitoring_namespace")

}
