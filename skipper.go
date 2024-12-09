package jaeger

import "net/http"

type Skipper func(r *http.Request) bool

func DefaultSkipper(r *http.Request) bool {
	return false
}
