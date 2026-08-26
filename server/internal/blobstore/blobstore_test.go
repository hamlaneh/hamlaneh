package blobstore_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
)

func newStore(t *testing.T) (*blobstore.Store, string) {
	t.Helper()

	root := t.TempDir()
	store, err := blobstore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, root
}

// write stores data under id and variant, failing the test on any error.
func write(t *testing.T, store *blobstore.Store, id uuid.UUID, variant blobstore.Variant, data string) {
	t.Helper()

	f, err := store.Create(id, variant)
	if err != nil {
		t.Fatalf("Create(%s, %q): %v", id, variant, err)
	}
	if _, err := io.WriteString(f, data); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close blob: %v", err)
	}
}

// read returns what is stored under id and variant.
func read(t *testing.T, store *blobstore.Store, id uuid.UUID, variant blobstore.Variant) string {
	t.Helper()

	f, err := store.Open(id, variant)
	if err != nil {
		t.Fatalf("Open(%s, %q): %v", id, variant, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Errorf("close blob: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	return string(data)
}

func TestStoreRoundTripsBothVariants(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	id := uuid.New()

	write(t, store, id, blobstore.Original, "the original bytes")
	write(t, store, id, blobstore.Thumbnail, "the thumbnail bytes")

	if got := read(t, store, id, blobstore.Original); got != "the original bytes" {
		t.Errorf("original = %q", got)
	}
	// The variants must not be the same file: a thumbnail link that served
	// the full original would defeat the point of signing them separately.
	if got := read(t, store, id, blobstore.Thumbnail); got != "the thumbnail bytes" {
		t.Errorf("thumbnail = %q", got)
	}
}

func TestStorePathIsDerivedFromTheIDAlone(t *testing.T) {
	t.Parallel()

	store, root := newStore(t)
	id := uuid.New()
	write(t, store, id, blobstore.Original, "bytes")

	// Whatever the layout, the file must live under the root and be named
	// after the id: nothing a client sent can be anywhere in the path.
	found := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk data root: %v", err)
	}

	if len(found) != 1 {
		t.Fatalf("stored one blob, found %d files: %v", len(found), found)
	}
	if !strings.Contains(filepath.Base(found[0]), id.String()) {
		t.Errorf("blob is at %q, want its name to carry the id %s", found[0], id)
	}
}

func TestStoreCreateRefusesToOverwrite(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	id := uuid.New()
	write(t, store, id, blobstore.Original, "first")

	// An id collision must be loud. Silently overwriting would replace one
	// person's file with another's.
	if _, err := store.Create(id, blobstore.Original); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second Create error = %v, want fs.ErrExist", err)
	}
	if got := read(t, store, id, blobstore.Original); got != "first" {
		t.Errorf("stored bytes changed to %q", got)
	}
}

func TestStoreOpenMissingIsNotExist(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)

	// The serving side turns this into the 404 a stranger gets, so it has to
	// be recognizable rather than an opaque failure.
	if _, err := store.Open(uuid.New(), blobstore.Original); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open of a missing blob = %v, want fs.ErrNotExist", err)
	}
}

func TestStoreDeleteRemovesEveryVariantAndIsIdempotent(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	id := uuid.New()
	write(t, store, id, blobstore.Original, "original")
	write(t, store, id, blobstore.Thumbnail, "thumbnail")

	if err := store.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, variant := range []blobstore.Variant{blobstore.Original, blobstore.Thumbnail} {
		if _, err := store.Open(id, variant); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("after Delete, Open(%q) = %v, want fs.ErrNotExist", variant, err)
		}
	}
	// The orphan sweep and a failed upload's cleanup both run against
	// whatever is actually there, so deleting nothing is not an error.
	if err := store.Delete(id); err != nil {
		t.Errorf("second Delete: %v", err)
	}
	// An attachment that never had a thumbnail deletes just as cleanly.
	other := uuid.New()
	write(t, store, other, blobstore.Original, "only an original")
	if err := store.Delete(other); err != nil {
		t.Errorf("Delete without a thumbnail: %v", err)
	}
}

func TestNewCreatesTheDataRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "nested", "data")
	if _, err := blobstore.New(root); err != nil {
		t.Fatalf("New on a missing directory: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("data root was not created: %v", err)
	}

	if _, err := blobstore.New(""); err == nil {
		t.Error("New(\"\") returned no error")
	}
}

func TestFromEnvHonorsTheDataDirVariable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "from-env")
	t.Setenv(blobstore.EnvDataDir, root)

	store, err := blobstore.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	id := uuid.New()
	write(t, store, id, blobstore.Original, "bytes")

	if _, err := os.Stat(root); err != nil {
		t.Errorf("FromEnv did not use %s: %v", blobstore.EnvDataDir, err)
	}
}
