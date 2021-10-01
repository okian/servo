package prometheus

import (
	"net/http"

	"github.com/okian/servo/v2/config"
)

type service struct {
	server *http.Server
}

func Namespace() string {
	return config.GetString("monitoring_namespace")
}
