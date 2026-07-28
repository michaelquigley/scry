// Package ingest owns scry's isolated passive-report HTTP surface.
package ingest

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/scry/internal/model"
)

const (
	maxBodyBytes   = 4 * 1024
	maxDetailBytes = 512
	reportPrefix   = "/report/"
)

var unknownTokenHash = sha256.Sum256([]byte("scry-ingest-unknown-check"))

// Reporter durably applies one passive report.
type Reporter interface {
	Report(context.Context, string, model.Result) (*model.Transition, error)
}

// Check identifies one passive report endpoint and its credential.
type Check struct {
	ID    string
	Token string
}

type registeredCheck struct {
	tokenHash [sha256.Size]byte
}

// Handler serves only passive report endpoints from its private mux.
type Handler struct {
	reporter Reporter
	checks   map[string]registeredCheck
	mux      *http.ServeMux
}

// NewHandler builds an isolated report handler from passive checks.
func NewHandler(checks []Check, reporter Reporter) (*Handler, error) {
	if reporter == nil {
		return nil, fmt.Errorf("reporter is required")
	}

	handler := &Handler{
		reporter: reporter,
		checks:   make(map[string]registeredCheck, len(checks)),
		mux:      http.NewServeMux(),
	}
	tokens := make(map[[sha256.Size]byte]string, len(checks))
	for i, check := range checks {
		if check.ID == "" {
			return nil, fmt.Errorf("passive check %d: id is required", i)
		}
		if check.Token == "" {
			return nil, fmt.Errorf("passive check %q: token is required", check.ID)
		}
		if _, found := handler.checks[check.ID]; found {
			return nil, fmt.Errorf("duplicate passive check id %q", check.ID)
		}
		tokenHash := sha256.Sum256([]byte(check.Token))
		if other, found := tokens[tokenHash]; found {
			return nil, fmt.Errorf("passive check %q: token is already used by check %q", check.ID, other)
		}
		tokens[tokenHash] = check.ID
		handler.checks[check.ID] = registeredCheck{tokenHash: tokenHash}
	}
	handler.mux.HandleFunc("/", handler.route)
	return handler, nil
}

// ServeHTTP serves the ingest-only handler tree.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	handler.mux.ServeHTTP(writer, request)
}

func (handler *Handler) route(writer http.ResponseWriter, request *http.Request) {
	id, found := reportID(request.URL.Path)
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	check, known := handler.checks[id]
	expected := unknownTokenHash
	if known {
		expected = check.tokenHash
	}
	authenticated := authorized(request.Header.Get("Authorization"), expected)
	if !known || !authenticated {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}

	result := model.Result{Status: model.StatusOK}
	if request.Method == http.MethodPost {
		parsed, status := parseReport(request)
		if status != 0 {
			writer.WriteHeader(status)
			return
		}
		result = parsed
	}

	if _, err := handler.reporter.Report(request.Context(), id, result); err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func reportID(path string) (string, bool) {
	if !strings.HasPrefix(path, reportPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, reportPrefix)
	if id == "" || strings.ContainsRune(id, '/') {
		return "", false
	}
	return id, true
}

func authorized(header string, expected [sha256.Size]byte) bool {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	actual := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

type reportBody struct {
	Status model.Status   `dd:"status"`
	Detail string         `dd:"detail"`
	Extra  map[string]any `dd:",+extra"`
}

func parseReport(request *http.Request) (model.Result, int) {
	if request.ContentLength > maxBodyBytes {
		return model.Result{}, http.StatusRequestEntityTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	if err != nil {
		return model.Result{}, http.StatusBadRequest
	}
	if len(data) > maxBodyBytes {
		return model.Result{}, http.StatusRequestEntityTooLarge
	}
	if len(data) == 0 {
		return model.Result{Status: model.StatusOK}, 0
	}

	body := reportBody{Status: model.StatusOK}
	if err := dd.BindJSON(&body, data, dd.Strict()); err != nil {
		return model.Result{}, http.StatusBadRequest
	}
	if !body.Status.Valid() {
		return model.Result{}, http.StatusBadRequest
	}
	return model.Result{
		Status: body.Status,
		Detail: truncateUTF8(body.Detail, maxDetailBytes),
	}, 0
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
