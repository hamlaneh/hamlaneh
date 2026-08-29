package httpserver

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// indexFile is the SPA document: one HTML file that boots the React app and
// then routes client-side.
const indexFile = "index.html"

// Cache-Control for the two kinds of file a Vite build emits.
//
// Everything under /assets/ carries a content hash in its name, so a given
// URL's bytes can never change: cache it for a year and never revalidate.
// index.html is the file that points at the current hashes, so caching it
// the same way would pin every browser to the release before last, forever.
// It is revalidated on every load instead — that is what makes an upgrade
// take effect.
const (
	immutableCacheControl = "public, max-age=31536000, immutable"
	documentCacheControl  = "no-cache"
)

// contentTypes maps the extensions the web build emits to the exact type to
// send. The operating system's MIME database is deliberately not consulted:
// it is absent from the distroless runtime image, and on Windows the
// registry can map .js to text/plain, which a nosniff browser then refuses
// to execute. Anything unrecognised is served as an opaque download rather
// than guessed at, because guessing is exactly what nosniff forbids.
var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".md":    "text/markdown; charset=utf-8",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

const defaultContentType = "application/octet-stream"

// webapp serves the built React application from an embedded filesystem
// rooted at the build's own directory.
type webapp struct {
	files fs.FS
}

// routeWebapp registers the web application's routes on mux.
//
// The client-side routes are enumerated rather than covered by a catch-all,
// and that is the point: a catch-all would also answer for /api paths the
// contract says are 404, and it would turn every typo into a 200 page. The
// list below is the whole set the app can reach today —
//
//	/         webapp/src/App.tsx — sign-in, two-step, and the reset screens,
//	          which are chosen by state rather than by URL
//	/reset    the emailed password-reset link. Its token rides in the URL
//	          fragment (internal/passwordreset), which no browser ever sends,
//	          so the server only ever sees this bare path
//	/c/...    webapp/src/screens/ChatApp.tsx — channel and message permalinks
//	/invite   the redemption screen an invitation link lands on. Like /reset,
//	          its token rides in the URL fragment (invite_handlers.go), so
//	          the server only ever sees this bare path
//	/meet/... the guest page a conference link opens. The token is a path
//	          segment here rather than a fragment, unlike /reset and /invite:
//	          it is not a credential somebody emailed to one person, it is a
//	          standing link meant to be pasted into a calendar entry, and it
//	          must survive a reload. The server sees it and does not read it
//	          — the redemption endpoint is what checks it
//	/admin    webapp/src/screens/AdminApp.tsx — the account menu links to it
//	          with a plain anchor, so it is a real navigation the server has
//	          to answer, and the pane routes below it (invites, settings,
//	          audit) are bookmarkable, so the subtree has to answer too.
//	          Serving the document is not an authorization decision: the
//	          dashboard's data comes from /api/v1/admin, which refuses
//	          non-admins, and the shell renders nothing without it
//
// — and adding a route to the app means adding it here in the same change.
func routeWebapp(mux *http.ServeMux, files fs.FS) {
	a := &webapp{files: files}

	mux.HandleFunc("GET /{$}", a.serveIndex)
	mux.HandleFunc("GET /reset", a.serveIndex)
	mux.HandleFunc("GET /invite", a.serveIndex)
	mux.HandleFunc("GET /c/", a.serveIndex)
	mux.HandleFunc("GET /admin", a.serveIndex)
	mux.HandleFunc("GET /meet/", a.serveIndex)
	mux.HandleFunc("GET /admin/", a.serveIndex)

	mux.HandleFunc("GET /assets/", a.serveHashedAsset)
	mux.HandleFunc("GET /brand/", a.servePublicFile)

	// An unknown path under /api is a client talking to an endpoint that
	// does not exist; it gets the contract's JSON error, never an HTML page.
	// Every real contract route is registered with both a method and a path,
	// so it is strictly more specific than this pattern and still wins.
	mux.HandleFunc("/api/", handleUnknownAPIPath)
}

// serveIndex answers a client-side route with the SPA document.
func (a *webapp) serveIndex(w http.ResponseWriter, r *http.Request) {
	a.serveFile(w, r, indexFile, documentCacheControl)
}

// serveHashedAsset serves the content-hashed bundle Vite writes to /assets/.
func (a *webapp) serveHashedAsset(w http.ResponseWriter, r *http.Request) {
	a.serveFile(w, r, requestedFile(r), immutableCacheControl)
}

// servePublicFile serves the files copied verbatim from webapp/public (the
// brand marks the document references as favicons). Their names carry no
// content hash, so they are revalidated like the document rather than
// pinned for a year.
func (a *webapp) servePublicFile(w http.ResponseWriter, r *http.Request) {
	a.serveFile(w, r, requestedFile(r), documentCacheControl)
}

// requestedFile maps a request path to a name in the build. ServeMux has
// already cleaned the path, and fs.FS rejects anything outside its root, so
// this is only the leading-slash trim.
func requestedFile(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Path, "/")
}

// serveFile writes one regular file from the build.
//
// Directories are refused outright, which is what makes a directory listing
// impossible rather than merely unlinked. Cache-Control is set only once the
// file is known to exist: an immutable 404 under /assets/ would outlive by a
// year the deploy that caused it.
//
// name comes from the request, so gosec reports G703 (path traversal) here.
// It cannot escape, and the reason is worth stating rather than assuming:
// a.files is the compile-time embedded build, and embed.FS resolves names
// through fs.ValidPath, which refuses any "..", any leading slash and any "."
// element. There is no path from a request into the host filesystem because
// the filesystem being read has no host paths in it. The worst a crafted name
// could reach is another file of the same bundle, all of which are already
// served at public URLs.
//
// That guarantee is a property of the FS, not of this function: pointing
// a.files at os.DirFS, or at anything else backed by real directories, makes
// G703 a live finding again and this suppression wrong.
// TestWebappRefusesPathTraversal holds the behaviour to that claim.
func (a *webapp) serveFile(w http.ResponseWriter, r *http.Request, name, cacheControl string) {
	info, err := fs.Stat(a.files, name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(name))
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeFileFS(w, r, a.files, name) // #nosec G703 -- a.files is the embedded build; embed.FS has no host paths to traverse into (see above)
}

// contentTypeFor resolves a file's Content-Type from the allowlist above.
func contentTypeFor(name string) string {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return defaultContentType
}

// handleUnknownAPIPath answers the contract's JSON error shape for any /api
// path no contract route claims. Handing a JSON client an HTML page instead
// is a genuinely miserable thing to debug.
func handleUnknownAPIPath(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeNotFound, msgNotFound)
}
