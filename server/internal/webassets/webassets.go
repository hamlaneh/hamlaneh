// Package webassets carries the built React web application inside the Go
// binary.
//
// Home mode is a single static binary with nothing in front of it
// (CLAUDE.md, "Packaging"), so the web build has to travel inside the
// binary; serving it from Go is then the one code path both deployment
// modes share, instead of two that drift.
//
// The committed dist directory is a PLACEHOLDER, not build output.
// Embedding a missing or empty directory is a compile error, and the Go CI
// job never runs `npm run build`, so a stand-in has to exist for
// `go build ./...` to work in a plain checkout. deploy/Dockerfile deletes it
// and copies the real webapp/dist in its place before building the release
// binary — the shipped binary never contains the placeholder.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// dist is the build rooted at its own directory, so a request path maps to
// an entry directly: /assets/index-abc.js is "assets/index-abc.js".
var dist = mustSub(embedded, "dist")

// FS returns the built web application.
func FS() fs.FS {
	return dist
}

// mustSub is fs.Sub for a directory that is embedded at compile time: the
// only way it can fail is a broken build, so failing at init beats
// answering requests from an empty filesystem.
func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("webassets: embedded " + dir + ": " + err.Error())
	}
	return sub
}
