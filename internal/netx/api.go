package netx

import (
	"context"
	"errors"
	"os"
	"strings"
)

// apiHosts are products-api's addresses, in order: api.andronix.app, then
// the old products.andronix.xyz, used only when the first can't be reached
// (owner: moving off andronix.xyz).
var apiHosts = []string{"https://api.andronix.app", "https://products.andronix.xyz"}

// APIs is where to reach products-api, in the order to try. ANDRONIX_API
// replaces the list: one address (a local products-api) or several,
// comma-separated.
func APIs() []string {
	var out []string
	for _, a := range strings.Split(os.Getenv("ANDRONIX_API"), ",") {
		if a = strings.TrimRight(strings.TrimSpace(a), "/"); a != "" {
			out = append(out, a)
		}
	}
	if len(out) > 0 {
		return out
	}
	return append([]string(nil), apiHosts...)
}

// Unreachable reports whether err means the server couldn't be reached
// (DNS, connect, TLS, a timeout), so the next address is worth a try. An
// HTTP answer (any status) isn't, and neither is the caller giving up
// (parent, the caller's own context, cancelled or past its deadline).
func Unreachable(parent context.Context, err error) bool {
	if err == nil || parent.Err() != nil {
		return false
	}
	var se *StatusError
	return !errors.As(err, &se)
}
