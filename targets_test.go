package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTargetSourceConditionalRefresh(t *testing.T) {
	status, body, etag := http.StatusOK, "127.0.0.1:80\n", `"v1"`
	wantValidator := ""
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if got := r.Header.Get("If-None-Match"); got != wantValidator {
			t.Errorf("If-None-Match = %q, want %q", got, wantValidator)
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	source := targetSource{url: server.URL, client: server.Client()}
	if targets, err := source.fetch(); err != nil || len(targets) != 1 || targets[0] != "127.0.0.1:80" {
		t.Fatalf("initial fetch = %v, %v", targets, err)
	}
	mu.Lock()
	wantValidator = `"v1"`
	status, body = http.StatusNotModified, ""
	mu.Unlock()
	if targets, err := source.fetch(); err != nil || targets != nil {
		t.Fatalf("304 fetch = %v, %v", targets, err)
	}
	// Rejected responses must not replace validators for the accepted list.
	for _, invalid := range []struct {
		status int
		body   string
	}{
		{http.StatusOK, ""},
		{http.StatusOK, "invalid"},
		{http.StatusNotFound, "not found"},
		{http.StatusOK, strings.Repeat("x", (1<<20)+1)},
	} {
		mu.Lock()
		status, body, etag = invalid.status, invalid.body, `"rejected"`
		mu.Unlock()
		if _, err := source.fetch(); err == nil {
			t.Fatalf("accepted invalid response: status=%d size=%d", status, len(body))
		}
		if source.etag != wantValidator {
			t.Fatalf("rejected response changed validator to %q", source.etag)
		}
	}
	mu.Lock()
	status, body, etag = http.StatusOK, "127.0.0.1:81\n", `"v2"`
	mu.Unlock()
	if targets, err := source.fetch(); err != nil || len(targets) != 1 || targets[0] != "127.0.0.1:81" {
		t.Fatalf("recovery fetch = %v, %v", targets, err)
	}
}

func TestTargetSourceLastModified(t *testing.T) {
	modified := "Wed, 21 Oct 2015 07:28:00 GMT"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			if r.Header.Get("If-Modified-Since") != modified {
				t.Error("missing Last-Modified validator")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", modified)
		fmt.Fprint(w, "127.0.0.1:80")
	}))
	defer server.Close()
	source := targetSource{url: server.URL, client: server.Client()}
	for range 2 {
		if _, err := source.fetch(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTargetReplacementRemovesMetrics(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target := listener.Addr().String()
	targets := &targetSet{targets: []string{target}}
	targets.check(time.Second)
	targets.replace([]string{"127.0.0.1:81"})
	if connectivityUp.DeleteLabelValues(target) ||
		connectivityLatencySeconds.DeleteLabelValues(target) ||
		connectivityChecksTotal.DeleteLabelValues(target, "success") {
		t.Fatal("removed target still has exported metrics")
	}
}
