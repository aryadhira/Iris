package httpapi

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// registerWeb mounts the production SPA. Hashed build output is cached for a
// year; index.html is never cached so a deploy is picked up on the next load.
// Unknown non-API paths fall back to index.html so /r/<id> survives a reload.
// /api/* never reaches this handler: it has its own JSON 404.
func (s *Server) registerWeb(e *echo.Echo, webDir string) {
	dir, err := filepath.Abs(webDir)
	if err != nil {
		s.logger.Error("cannot resolve IRIS_WEB_DIR; the API stays up but the UI will not load", "dir", webDir, "error", err)
		dir = webDir
	}
	info, statErr := os.Stat(dir)
	switch {
	case statErr != nil:
		s.logger.Warn("web UI not found; the API keeps serving, the UI will not load (run `npm run build` in web/ or set IRIS_WEB_DIR)", "dir", dir, "error", statErr)
	case !info.IsDir():
		s.logger.Warn("IRIS_WEB_DIR is not a directory; the API keeps serving, the UI will not load", "dir", dir)
	default:
		index := filepath.Join(dir, "index.html")
		if _, indexErr := os.Stat(index); indexErr != nil {
			s.logger.Warn("web UI has no index.html; the API keeps serving, the UI will not load", "dir", dir, "error", indexErr)
		}
	}

	serve := func(c echo.Context) error { return s.serveWeb(c, dir) }
	e.GET("/*", serve)
	e.HEAD("/*", serve)
}

// apiNotFound is the only answer under /api for an unknown path. It is JSON
// on purpose: an API client must never receive index.html.
func apiNotFound(c echo.Context) error {
	return c.JSON(http.StatusNotFound, errorBody("That API path is not here."))
}

func (s *Server) serveWeb(c echo.Context, webDir string) error {
	requested := c.Request().URL.Path

	rel := path.Clean("/" + strings.TrimPrefix(requested, "/"))
	if strings.Contains(rel, "..") {
		return c.String(http.StatusNotFound, "Not found.")
	}

	indexPath := filepath.Join(webDir, "index.html")
	if rel == "/" {
		return serveIndex(c, indexPath)
	}

	clean := strings.TrimPrefix(rel, "/")
	abs := filepath.Join(webDir, filepath.FromSlash(clean))

	// Only regular files are served. A directory never gets a listing and a
	// symlink is resolved by os.Stat; the stat check keeps a missing build from
	// turning into an HTML 200.
	info, err := os.Stat(abs)
	if err == nil && info.Mode().IsRegular() {
		if strings.HasPrefix(rel, "/assets/") {
			c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Response().Header().Set("Cache-Control", "public, max-age=3600")
		}
		return c.File(abs)
	}

	// A missing hashed asset is a 404, not a full page. Returning HTML here
	// would make a broken build look like a working app with a blank screen.
	if strings.HasPrefix(rel, "/assets/") {
		return c.String(http.StatusNotFound, "Asset not found.")
	}

	return serveIndex(c, indexPath)
}

func serveIndex(c echo.Context, indexPath string) error {
	if info, err := os.Stat(indexPath); err != nil || !info.Mode().IsRegular() {
		return c.String(http.StatusNotFound, "Iris UI is not built on this server. The API is still available under /api.")
	}
	c.Response().Header().Set("Cache-Control", "no-cache, must-revalidate")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.File(indexPath)
}
