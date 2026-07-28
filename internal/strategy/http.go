package strategy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/michaelquigley/scry/internal/model"
)

// HTTP evaluates one URL without following redirects.
type HTTP struct {
	url      string
	expected map[int]struct{}
	client   *http.Client
}

// NewHTTP returns an HTTP strategy with explicit codes or the default 2xx range.
func NewHTTP(url string, expected []int, insecure bool) *HTTP {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tlsConfig := &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit per-check self-signed escape hatch
		if transport.TLSClientConfig != nil {
			tlsConfig = transport.TLSClientConfig.Clone()
			tlsConfig.InsecureSkipVerify = true // #nosec G402 -- explicit per-check self-signed escape hatch
		}
		transport.TLSClientConfig = tlsConfig
	}

	codes := make(map[int]struct{}, len(expected))
	for _, code := range expected {
		codes[code] = struct{}{}
	}
	return &HTTP{
		url:      url,
		expected: codes,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Evaluate requests the configured URL and judges its first response status.
func (strategy *HTTP) Evaluate(ctx context.Context) model.Result {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strategy.url, nil)
	if err != nil {
		return model.Result{Status: model.StatusFailed, Detail: err.Error()}
	}
	response, err := strategy.client.Do(request)
	if err != nil {
		return model.Result{Status: model.StatusFailed, Detail: err.Error()}
	}
	_ = response.Body.Close()

	if strategy.accepts(response.StatusCode) {
		return model.Result{Status: model.StatusOK}
	}
	return model.Result{
		Status: model.StatusFailed,
		Detail: fmt.Sprintf("http status %d", response.StatusCode),
	}
}

func (strategy *HTTP) accepts(code int) bool {
	if len(strategy.expected) == 0 {
		return code >= 200 && code <= 299
	}
	_, found := strategy.expected[code]
	return found
}
