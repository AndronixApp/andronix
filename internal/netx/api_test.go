package netx

import (
	"context"
	"errors"
	"testing"
)

func TestAPIs(t *testing.T) {
	t.Setenv("ANDRONIX_API", "")
	if a := APIs(); len(a) != 2 || a[0] != "https://api.andronix.app" || a[1] != "https://products.andronix.xyz" {
		t.Errorf("default: %v", a)
	}
	t.Setenv("ANDRONIX_API", "http://127.0.0.1:8787/")
	if a := APIs(); len(a) != 1 || a[0] != "http://127.0.0.1:8787" {
		t.Errorf("pinned: %v", a)
	}
	t.Setenv("ANDRONIX_API", "http://a, http://b/")
	if a := APIs(); len(a) != 2 || a[1] != "http://b" {
		t.Errorf("list: %v", a)
	}
}

func TestUnreachable(t *testing.T) {
	ctx := context.Background()
	if Unreachable(ctx, nil) || Unreachable(ctx, &StatusError{Code: 401}) {
		t.Error("no error or an HTTP answer: not unreachable")
	}
	if !Unreachable(ctx, errors.New("dial tcp: lookup api.andronix.app: no such host")) {
		t.Error("DNS failure: unreachable")
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	if Unreachable(done, errors.New("context canceled")) {
		t.Error("caller gave up: not unreachable")
	}
}
