package server

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const indexFile = "index.html"

// assets serves the embedded dashboard build and nothing else.
//
// there is deliberately no single-page fallback: the dashboard has no router,
// so every path that does not name an embedded file is a genuine 404. that is
// what keeps the status surface free of the report endpoints living on the
// separate ingest listener.
type assets struct {
	files  fs.FS
	server http.Handler
	index  []byte
}

// newAssets prepares the dashboard handler over a built dist tree. a tree with
// no index is a headless build; its routes answer 404 and the API still serves.
func newAssets(files fs.FS) *assets {
	index, err := fs.ReadFile(files, indexFile)
	if err != nil {
		index = nil
	}
	return &assets{
		files:  files,
		server: http.FileServer(http.FS(files)),
		index:  index,
	}
}

// embedded reports whether a dashboard build is present.
func (handler *assets) embedded() bool {
	return handler.index != nil
}

// ServeHTTP resolves the path before it polices the method: a path this
// listener does not serve answers 404 for every method, because the
// cross-surface isolation guarantee is about the path and not the verb.
func (handler *assets) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	name := assetName(request.URL.Path)
	if name == "" || name == indexFile {
		if !handler.embedded() {
			http.NotFound(writer, request)
			return
		}
		if !readMethod(request.Method) {
			rejectMethod(writer)
			return
		}
		handler.serveIndex(writer, request)
		return
	}

	if _, err := fs.Stat(handler.files, name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(writer, request)
			return
		}
		http.Error(writer, "reading dashboard asset failed", http.StatusInternalServerError)
		return
	}
	if !readMethod(request.Method) {
		rejectMethod(writer)
		return
	}
	handler.server.ServeHTTP(writer, request)
}

func readMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func rejectMethod(writer http.ResponseWriter) {
	writer.Header().Set("Allow", "GET, HEAD")
	writer.WriteHeader(http.StatusMethodNotAllowed)
}

func (handler *assets) serveIndex(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(handler.index)
}

// assetName resolves a request path to an embedded file name. the root path
// resolves to the empty name, which the caller answers with the index.
func assetName(requestPath string) string {
	name := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if name == "." {
		return ""
	}
	return name
}
