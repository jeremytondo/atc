package server

import (
	"bytes"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

const unstagedWebAssetsMessage = "embedded web assets have not been staged; run mise run build or mise run assets:stage"

const webIndex = "index.html"
const webFallback = "fallback.html"

func webAssetRoutes(fsys fs.FS) http.Handler {
	if fsys == nil {
		return webAssetError(http.StatusInternalServerError, "embedded web assets are unavailable")
	}
	return embeddedWebAssets{fsys: fsys}
}

type embeddedWebAssets struct {
	fsys fs.FS
}

func (assets embeddedWebAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	isRoot := name == "." || name == ""
	if isRoot {
		name = webIndex
	}
	if hasHiddenPathSegment(name) {
		http.NotFound(w, r)
		return
	}

	if data, contentType, ok := assets.readFile(name); ok {
		serveWebAsset(w, r, name, contentType, data)
		return
	}
	if !isRoot && isWebAssetRequest(name) {
		http.NotFound(w, r)
		return
	}

	fallbackName := webFallback
	data, contentType, ok := assets.readFile(fallbackName)
	if !ok {
		fallbackName = webIndex
		data, contentType, ok = assets.readFile(fallbackName)
	}
	if !ok {
		http.Error(w, unstagedWebAssetsMessage, http.StatusServiceUnavailable)
		return
	}
	serveWebAsset(w, r, fallbackName, contentType, data)
}

func (assets embeddedWebAssets) readFile(name string) ([]byte, string, bool) {
	info, err := fs.Stat(assets.fsys, name)
	if err != nil {
		return nil, "", false
	}
	if info.IsDir() {
		name = path.Join(name, "index.html")
		info, err = fs.Stat(assets.fsys, name)
		if err != nil || info.IsDir() {
			return nil, "", false
		}
	}

	data, err := fs.ReadFile(assets.fsys, name)
	if err != nil {
		return nil, "", false
	}
	return data, mime.TypeByExtension(path.Ext(name)), true
}

func isWebAssetRequest(name string) bool {
	return strings.HasPrefix(name, "_app/") || path.Ext(name) != ""
}

func hasHiddenPathSegment(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func serveWebAsset(w http.ResponseWriter, r *http.Request, name string, contentType string, data []byte) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func webAssetError(status int, message string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, message, status)
	})
}
