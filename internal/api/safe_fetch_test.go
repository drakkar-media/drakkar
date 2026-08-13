package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRejectSSRFIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"169.254.1.1", true},
		{"10.0.0.5", true},
		{"172.16.0.5", true},
		{"192.168.1.5", true},
		{"100.64.0.1", true},
		{"192.0.2.1", true},
		{"198.18.0.1", true},
		{"224.0.0.1", true},
		{"240.0.0.1", true},
		{"fc00::1", true},
		{"fec0::1", true},
		{"2001:db8::1", true},
		{"2002:0a00:0001::", true},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		err := rejectSSRFIP(net.ParseIP(c.ip))
		if c.blocked && err == nil {
			t.Errorf("expected %s to be blocked, got nil error", c.ip)
		}
		if !c.blocked && err != nil {
			t.Errorf("expected %s to be allowed, got error: %v", c.ip, err)
		}
	}
}

func TestFetchRemoteURLRejectsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should never be reached"))
	}))
	defer srv.Close()

	_, err := fetchRemoteURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected fetchRemoteURL to reject a loopback target, got nil error")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("expected SSRF rejection error, got: %v", err)
	}
}

func TestFetchRemoteURLRejectsBadScheme(t *testing.T) {
	_, err := fetchRemoteURL(context.Background(), "file:///etc/passwd")
	if err == nil {
		t.Fatal("expected fetchRemoteURL to reject a non-http(s) scheme")
	}
}

func TestFetchRemoteURLAllowsExactConfiguredPrivateUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<nzb></nzb>"))
	}))
	defer srv.Close()

	body, err := fetchRemoteURLFromUpstreams(context.Background(), srv.URL+"/download", []string{srv.URL + "/api"})
	if err != nil {
		t.Fatalf("expected configured upstream authority to be allowed: %v", err)
	}
	if string(body) != "<nzb></nzb>" {
		t.Fatalf("unexpected response body %q", body)
	}
}

func TestFetchRemoteURLBlocksPrivateRedirectOutsideConfiguredAuthority(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls++
		_, _ = w.Write([]byte("should never be reached"))
	}))
	defer target.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusFound)
	}))
	defer upstream.Close()

	_, err := fetchRemoteURLFromUpstreams(context.Background(), upstream.URL+"/download", []string{upstream.URL})
	if err == nil || !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("expected redirect to unconfigured private authority to be blocked, got %v", err)
	}
	if targetCalls != 0 {
		t.Fatalf("expected private redirect target not to be reached, got %d calls", targetCalls)
	}
}

func TestFetchRemoteURLRejectsDeclaredOversizeBeforeReading(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(safeFetchMaxBodyBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := fetchRemoteURLFromUpstreams(context.Background(), srv.URL, []string{srv.URL})
	if err == nil || !strings.Contains(err.Error(), "remote body exceeds") {
		t.Fatalf("expected declared remote size to be rejected, got %v", err)
	}
}

func TestFetchRemoteURLCapsConcurrentRequests(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, safeFetchMaxConcurrent+2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte("<nzb></nzb>"))
	}))
	defer srv.Close()

	const extraRequests = 2
	var wg sync.WaitGroup
	errs := make(chan error, safeFetchMaxConcurrent+extraRequests)
	for range safeFetchMaxConcurrent + extraRequests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fetchRemoteURLFromUpstreams(context.Background(), srv.URL, []string{srv.URL})
			errs <- err
		}()
	}
	for range safeFetchMaxConcurrent {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("timed out waiting for fetch slots to fill")
		}
	}
	select {
	case <-started:
		close(release)
		t.Fatal("request exceeded concurrent fetch cap")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("fetch failed: %v", err)
		}
	}
	if got := maximum.Load(); got > safeFetchMaxConcurrent {
		t.Fatalf("maximum concurrency = %d, cap = %d", got, safeFetchMaxConcurrent)
	}
}
