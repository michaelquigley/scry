package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexMarkup = `<!doctype html><html><body><div id="root"></div></body></html>`

// dashboardFS stands in for the embedded dist tree a frontend build produces.
func dashboardFS() fs.FS {
	return fstest.MapFS{
		indexFile:                 &fstest.MapFile{Data: []byte(indexMarkup)},
		"assets/index-abc123.js":  &fstest.MapFile{Data: []byte("console.log('scry')")},
		"assets/index-abc123.css": &fstest.MapFile{Data: []byte(".rollup{}")},
	}
}

func serveDashboard(t *testing.T, files fs.FS, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	newAssets(files).ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func TestRootServesTheDashboardIndex(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		response := serveDashboard(t, dashboardFS(), http.MethodGet, path)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="root"`) {
			t.Fatalf("%s: %d %q", path, response.Code, response.Body.String())
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("%s content type: %q", path, contentType)
		}
	}
}

func TestBuiltAssetsAreServed(t *testing.T) {
	response := serveDashboard(t, dashboardFS(), http.MethodGet, "/assets/index-abc123.js")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "console.log") {
		t.Fatalf("asset: %d %q", response.Code, response.Body.String())
	}
}

// there is no single-page fallback: the dashboard has no router, so an unknown
// path is a 404 whether or not it looks like a file. this is what keeps the
// status surface from answering for paths that belong to another listener.
func TestUnknownPathsAre404WithoutASinglePageFallback(t *testing.T) {
	for _, path := range []string{
		"/assets/missing.js",
		"/checks/nas-snapshot",
		"/report/nas-snapshot",
		"/dashboard",
	} {
		response := serveDashboard(t, dashboardFS(), http.MethodGet, path)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %q", path, response.Code, response.Body.String())
		}
	}
}

func TestDashboardServesOnlyReadMethods(t *testing.T) {
	response := serveDashboard(t, dashboardFS(), http.MethodHead, "/")
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("head: %d %q", response.Code, response.Body.String())
	}
	response = serveDashboard(t, dashboardFS(), http.MethodPost, "/")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", response.Code)
	}
	if allow := response.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("allow header: %q", allow)
	}
}

// method policing applies only to paths this listener actually serves. an
// unserved path answers 404 whatever the verb, so a write method can never
// reveal through a 405 that some other surface owns the path.
func TestUnknownPathsAre404ForEveryMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut} {
		response := serveDashboard(t, dashboardFS(), method, "/report/nas-snapshot")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", method, response.Code)
		}
	}
}

// a headless build embeds no dist tree; the API still serves and every
// dashboard path answers 404 rather than a partially rendered page.
func TestHeadlessBuildServesNoDashboard(t *testing.T) {
	response := serveDashboard(t, fstest.MapFS{}, http.MethodGet, "/")
	if response.Code != http.StatusNotFound {
		t.Fatalf("headless root: %d", response.Code)
	}
	if newAssets(fstest.MapFS{}).embedded() {
		t.Fatal("an empty tree should not report an embedded dashboard")
	}
}
