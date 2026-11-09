package netx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// ErrSourceChanged: a resumed download could not be spliced onto what was
// already read — the server returned the whole file from the start (a 200,
// not a 206) because the object changed under us (an image swap mid-stream)
// or the server ignores Range. The caller restarts the download from zero
// (#243: without If-Range a changed object was resumed as a 206 of the new
// object's bytes, splicing garbage that only the final checksum caught).
var ErrSourceChanged = errors.New("download could not be resumed (source changed); restart")

// Stream reads url from the start as one continuous body, for unpacking
// while downloading (no copy on disk). A dropped or stalled connection is
// resumed with a Range request from the byte it stopped at, up to 5 times
// in a row; the caller only sees an error when that fails. Read reports
// the bytes read so far to progress.
type Stream struct {
	ctx      context.Context
	url      string
	total    int64
	progress func(cur, total int64)

	body      io.ReadCloser
	cur       int64
	tries     int
	validator string // ETag (else Last-Modified) from the first 200, for If-Range
}

// OpenStream starts the download. total (0 if unknown) is only for progress.
func OpenStream(ctx context.Context, url string, total int64, progress func(cur, total int64)) (*Stream, error) {
	s := &Stream{ctx: ctx, url: url, total: total, progress: progress}
	if err := s.open(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Stream) open() error {
	req, _ := http.NewRequestWithContext(s.ctx, "GET", s.url, nil)
	req.Header.Set("User-Agent", UserAgent)
	if s.cur > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(s.cur, 10)+"-")
		// If-Range ties the resume to the object we started on: an unchanged
		// object answers 206 (resume), a changed one answers 200 (full body,
		// handled below) instead of 206-ing the new object's bytes.
		if s.validator != "" {
			req.Header.Set("If-Range", s.validator)
		}
	}
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	switch {
	case s.cur == 0 && resp.StatusCode == 200:
		if s.total <= 0 && resp.ContentLength > 0 {
			s.total = resp.ContentLength
		}
		// Remember the validator so a resume can prove it's the same object.
		if et := resp.Header.Get("ETag"); et != "" {
			s.validator = et
		} else if lm := resp.Header.Get("Last-Modified"); lm != "" {
			s.validator = lm
		}
	case s.cur > 0 && resp.StatusCode == 206:
	case s.cur > 0 && resp.StatusCode == 200:
		// If-Range not honoured, the object changed, or the server ignores
		// Range: a full body from the start can't splice onto what we read.
		resp.Body.Close()
		return ErrSourceChanged
	default:
		resp.Body.Close()
		return &StatusError{resp.StatusCode, s.url}
	}
	s.body = resp.Body
	return nil
}

func (s *Stream) Read(p []byte) (int, error) {
	for {
		if s.body == nil {
			if s.tries >= 5 {
				return 0, io.ErrUnexpectedEOF
			}
			if !sleep(s.ctx, s.tries) {
				return 0, s.ctx.Err()
			}
			s.tries++
			if err := s.open(); err != nil {
				if errors.Is(err, ErrSourceChanged) {
					return 0, err
				}
				if se, ok := err.(*StatusError); ok && se.Code < 500 && se.Code != 429 {
					return 0, err
				}
				continue
			}
		}
		n, err := readWithTimeout(s.body, p, stallTimeout)
		if n > 0 {
			s.cur += int64(n)
			s.tries = 0
			if s.progress != nil {
				s.progress(s.cur, s.total)
			}
		}
		if err == io.EOF && (s.total <= 0 || s.cur >= s.total) {
			return n, io.EOF
		}
		if err != nil {
			// Dropped early: resume from s.cur on the next Read.
			s.body.Close()
			s.body = nil
			if n > 0 {
				return n, nil
			}
			if s.ctx.Err() != nil {
				return 0, s.ctx.Err()
			}
			continue
		}
		return n, nil
	}
}

// Read so far.
func (s *Stream) Bytes() int64 { return s.cur }

func (s *Stream) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
