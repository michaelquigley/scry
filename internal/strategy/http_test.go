package strategy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/scry/internal/model"
)

func TestHTTPDefaultAndExplicitStatusJudgment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthy" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	healthy := NewHTTP(server.URL+"/healthy", nil, false, "").Evaluate(context.Background())
	if healthy.Status != model.StatusOK {
		t.Fatalf("healthy: %+v", healthy)
	}

	failed := NewHTTP(server.URL+"/failed", nil, false, "").Evaluate(context.Background())
	if failed.Status != model.StatusFailed || failed.Detail != "http status 503" {
		t.Fatalf("failed: %+v", failed)
	}

	expected := NewHTTP(server.URL+"/failed", []int{http.StatusServiceUnavailable}, false, "").Evaluate(context.Background())
	if expected.Status != model.StatusOK {
		t.Fatalf("explicit expected status: %+v", expected)
	}
}

func TestHTTPNeverFollowsRedirects(t *testing.T) {
	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, destination.URL, http.StatusFound)
	}))
	defer redirect.Close()

	failed := NewHTTP(redirect.URL, nil, false, "").Evaluate(context.Background())
	if failed.Status != model.StatusFailed || failed.Detail != "http status 302" {
		t.Fatalf("default redirect judgment: %+v", failed)
	}
	if destinationRequests.Load() != 0 {
		t.Fatal("strategy followed the redirect")
	}

	expected := NewHTTP(redirect.URL, []int{http.StatusFound}, false, "").Evaluate(context.Background())
	if expected.Status != model.StatusOK {
		t.Fatalf("expected redirect: %+v", expected)
	}
	if destinationRequests.Load() != 0 {
		t.Fatal("strategy followed an expected redirect")
	}
}

func TestHTTPHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan model.Result, 1)
	go func() {
		resultCh <- NewHTTP(server.URL, nil, false, "").Evaluate(ctx)
	}()
	<-started
	cancel()
	result := <-resultCh
	if result.Status != model.StatusFailed || !strings.Contains(result.Detail, "context canceled") {
		t.Fatalf("result: %+v", result)
	}
}

func TestHTTPInsecureIsExplicit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	verified := NewHTTP(server.URL, nil, false, "").Evaluate(context.Background())
	if verified.Status != model.StatusFailed {
		t.Fatalf("untrusted certificate passed verification: %+v", verified)
	}
	insecure := NewHTTP(server.URL, nil, true, "").Evaluate(context.Background())
	if insecure.Status != model.StatusOK {
		t.Fatalf("explicit insecure probe: %+v", insecure)
	}
}

// TestHTTPDialAddressOverride dials an address the URL's host never
// resolves to, so an ok result can only mean the override carried the
// connection while the URL's host kept the Host header.
func TestHTTPDialAddressOverride(t *testing.T) {
	const host = "virtual.example.test"

	var observedHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observedHost = request.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	port := listenerPort(t, server.Listener)

	result := NewHTTP("http://"+host+"/", nil, false, "127.0.0.1:"+port).Evaluate(context.Background())
	if result.Status != model.StatusOK {
		t.Fatalf("override probe: %+v", result)
	}
	if observedHost != host {
		t.Fatalf("host header %q, want %q", observedHost, host)
	}
}

// TestHTTPDialAddressOverrideTLS proves the TLS server name and the Host
// header track the URL's host even while the dial lands on the override
// address. the probe runs insecure because the test certificate is
// self-signed, and SNI selection is independent of verification.
func TestHTTPDialAddressOverrideTLS(t *testing.T) {
	const host = "virtual.example.test"
	certificate := selfSignedCert(t, host)

	var observedSNI, observedHost string
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listenerPort(t, plain)
	server := tls.NewListener(plain, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			observedSNI = hello.ServerName
			return nil, nil
		},
	})
	handler := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observedHost = request.Host
		w.WriteHeader(http.StatusNoContent)
	})}
	go handler.Serve(server)
	t.Cleanup(func() { _ = handler.Close() })

	result := NewHTTP("https://"+host+"/", nil, true, "127.0.0.1:"+port).Evaluate(context.Background())
	if result.Status != model.StatusOK {
		t.Fatalf("override probe: %+v", result)
	}
	if observedSNI != host {
		t.Fatalf("tls server name %q, want %q", observedSNI, host)
	}
	if observedHost != host {
		t.Fatalf("host header %q, want %q", observedHost, host)
	}
}

// TestHTTPDialAddressOverrideBypassesProxy pins the override's documented
// meaning: proxy environment variables steer ordinary probes, but an
// explicitly named dial target goes straight to the endpoint. the probe is
// https, because with a mis-routed dial the client would send CONNECT
// through the TLS listener and fail, which the test detects.
func TestHTTPDialAddressOverrideBypassesProxy(t *testing.T) {
	const host = "virtual.example.test"
	certificate := selfSignedCert(t, host)

	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)

	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listenerPort(t, plain)
	server := tls.NewListener(plain, &tls.Config{Certificates: []tls.Certificate{certificate}})

	var observedHost string
	handler := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observedHost = request.Host
		w.WriteHeader(http.StatusNoContent)
	})}
	go handler.Serve(server)
	t.Cleanup(func() { _ = handler.Close() })

	result := NewHTTP("https://"+host+"/", nil, true, "127.0.0.1:"+port).Evaluate(context.Background())
	if result.Status != model.StatusOK {
		t.Fatalf("override probe: %+v", result)
	}
	if observedHost != host {
		t.Fatalf("host header %q, want %q", observedHost, host)
	}
	if proxyRequests.Load() != 0 {
		t.Fatal("override probe was routed through the proxy")
	}
}

func TestHTTPDialAddressRefused(t *testing.T) {
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listenerPort(t, plain)
	_ = plain.Close()

	result := NewHTTP("http://virtual.example.test/", nil, false, "127.0.0.1:"+port).Evaluate(context.Background())
	if result.Status != model.StatusFailed || !strings.Contains(result.Detail, "connect") {
		t.Fatalf("refused probe: %+v", result)
	}
}

func listenerPort(t *testing.T, listener net.Listener) string {
	t.Helper()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("listener address: %v", err)
	}
	return port
}

func selfSignedCert(t *testing.T, host string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
