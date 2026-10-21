// Package fakes3 is an in-process stand-in for an S3-compatible bucket, for
// tests only. It speaks path-style addressing (`/{bucket}/{key}`) and the four
// calls s3store makes — PUT, GET (with Range), HEAD and DELETE — and nothing
// else. Signatures are not checked: what the tests prove is how our code uses
// the protocol, not that AWS verifies SigV4.
package fakes3

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// Server is one fake bucket behind an httptest server.
type Server struct {
	*httptest.Server
	Bucket string

	mu      sync.Mutex
	objects map[string][]byte
	gets    int
	// RejectCode, when set, answers every request with that S3 error code
	// (403) — how a wrong access key or a refused bucket looks to the client.
	RejectCode string
	// MissingDenied answers a missing key with 403 AccessDenied instead of
	// 404 NoSuchKey — what AWS does when the key lacks s3:ListBucket
	// (GetObject 문서 「객체가 없을 때」).
	MissingDenied bool
}

// New starts a fake bucket. Close it with Server.Close.
func New(bucket string) *Server {
	s := &Server{Bucket: bucket, objects: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Object returns a stored object and whether it exists.
func (s *Server) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	return b, ok
}

// Keys lists the stored object keys.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for k := range s.objects {
		out = append(out, k)
	}
	return out
}

// Gets counts GET requests so far.
func (s *Server) Gets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RejectCode != "" {
		writeError(w, http.StatusForbidden, s.RejectCode)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != s.Bucket {
		writeError(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	// **엄격한 호환 서비스처럼 굴어** 체크섬 머리글을 거부한다. SDK 의 기본값(업로드마다
	// CRC32, 트레일러)을 모르는 서비스가 실제로 있고, s3store 가 그 기본값을 끈다.
	for h := range r.Header {
		if strings.HasPrefix(h, "X-Amz-Checksum-") || h == "X-Amz-Trailer" {
			writeError(w, http.StatusBadRequest, "NotImplemented")
			return
		}
	}
	switch r.Method {
	case http.MethodPut:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "IncompleteBody")
			return
		}
		s.objects[key] = b
		w.Header().Set("ETag", `"fake"`)
	case http.MethodGet, http.MethodHead:
		b, ok := s.objects[key]
		if !ok && s.MissingDenied {
			writeError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		if r.Method == http.MethodGet {
			s.gets++
		}
		start, end := 0, len(b)-1
		status := http.StatusOK
		if rg := r.Header.Get("Range"); rg != "" {
			var err error
			if start, end, err = parseRange(rg, len(b)); err != nil {
				writeError(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
				return
			}
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(b)))
		}
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(status)
		if r.Method == http.MethodGet {
			_, _ = w.Write(b[start : end+1])
		}
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

// parseRange reads the one form the client sends: `bytes=N-` or `bytes=N-M`.
func parseRange(h string, size int) (int, int, error) {
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok {
		return 0, 0, fmt.Errorf("range %q", h)
	}
	a, b, _ := strings.Cut(spec, "-")
	start, err := strconv.Atoi(a)
	if err != nil || start >= size {
		return 0, 0, fmt.Errorf("range %q", h)
	}
	end := size - 1
	if b != "" {
		if end, err = strconv.Atoi(b); err != nil || end < start {
			return 0, 0, fmt.Errorf("range %q", h)
		}
		end = min(end, size-1)
	}
	return start, end, nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>fake</Message></Error>`, code)
}
