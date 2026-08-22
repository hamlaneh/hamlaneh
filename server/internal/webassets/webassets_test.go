package webassets_test

import (
	"io/fs"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/webassets"
)

// TestFSIsRootedAtTheBuild pins that the embedded filesystem is rooted at
// the build itself, so a request path maps straight onto an entry. Getting
// the fs.Sub root wrong compiles fine and shows up only as the server
// answering 404 for its own front page.
func TestFSIsRootedAtTheBuild(t *testing.T) {
	t.Parallel()

	info, err := fs.Stat(webassets.FS(), "index.html")
	if err != nil {
		t.Fatalf("index.html is not at the root of the embedded build: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("index.html is not a regular file (mode %v)", info.Mode())
	}
	if info.Size() == 0 {
		t.Error("index.html is embedded but empty")
	}
}
