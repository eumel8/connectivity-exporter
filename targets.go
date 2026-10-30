package main

import (
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// targetSource is owned by a single polling goroutine after the startup fetch.
// Validators are retained only for fully read and accepted target lists.
type targetSource struct {
	url          string
	client       *http.Client
	etag         string
	lastModified string
}

func (s *targetSource) fetch() ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	if s.etag != "" {
		req.Header.Set("If-None-Match", s.etag)
	} else if s.lastModified != "" {
		req.Header.Set("If-Modified-Since", s.lastModified)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if s.etag == "" && s.lastModified == "" {
			return nil, fmt.Errorf("received 304 without an accepted target list")
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("target source returned HTTP %d", resp.StatusCode)
	}
	// Bound memory usage and reject oversized or truncated downloads as a whole.
	const maxSize = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("target list exceeds 1 MiB")
	}
	targets := parseTargets(string(data))
	if len(targets) == 0 {
		return nil, fmt.Errorf("target source contains no valid targets")
	}
	s.etag = resp.Header.Get("ETag")
	s.lastModified = resp.Header.Get("Last-Modified")
	return targets, nil
}

func (s *targetSource) poll(targets *targetSet, interval time.Duration) {
	for {
		// Spread recurring requests across clusters, including retries after errors.
		time.Sleep(time.Duration(float64(interval) * (0.8 + rand.Float64()*0.4)))
		updated, err := s.fetch()
		if err != nil {
			slog.Warn("target refresh failed, keeping current targets", "error", err)
			continue
		}
		if updated != nil {
			targets.replace(updated)
			slog.Info("target list refreshed", "targets", updated)
		}
	}
}
