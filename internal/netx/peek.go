package netx

import (
	"context"
	"io"
	"net/http"
	"strconv"
)

// Peek returns the first n bytes of url, in one try. It asks for that range
// only, so looking at the start of a large file costs little; a server that
// ignores Range is read up to n bytes and hung up on.
func Peek(ctx context.Context, url string, n int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Range", "bytes=0-"+strconv.FormatInt(n-1, 10))
	resp, err := Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return nil, &StatusError{resp.StatusCode, url}
	}
	return io.ReadAll(io.LimitReader(resp.Body, n))
}
