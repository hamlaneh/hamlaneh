// Package blobstore keeps uploaded file bytes on the filesystem, addressed
// only by the server-generated UUID of the attachment row that describes
// them (ADR 003). PostgreSQL holds the metadata; this holds the bytes.
//
// Every path is built from a uuid.UUID and a fixed variant suffix. The
// client's filename is display data stored in the database and never reaches
// this package, which is what makes path traversal structurally impossible
// here rather than something a filter has to catch: a uuid.UUID has no
// spelling that contains a separator, a dot, or anything else a path walker
// reacts to.
//
// Files are written 0600 under 0700 directories: on a home-mode install the
// data directory sits in somebody's home, and a chat's files are not world-
// readable.
package blobstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// EnvDataDir names the data root. The default keeps the zero-config install
// working with no environment at all; compose mounts a volume and sets it.
const EnvDataDir = "HAMLANEH_DATA_DIR"

// DefaultDataDir is where blobs go when EnvDataDir is unset.
const DefaultDataDir = "./data"

// Permissions for what this package creates. The blobs are one instance's
// private data; nothing outside the server process has any business reading
// them from disk.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// Variant is which rendition of an attachment a path names.
type Variant string

const (
	// Original is the stored upload — for an image, the stripped original.
	Original Variant = ""
	// Thumbnail is the bounded preview generated at ingest.
	Thumbnail Variant = ".thumb"
)

// Store is a data root on the filesystem.
type Store struct {
	root string
}

// New returns a Store rooted at dir, creating it if it does not exist.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("blobstore: empty data directory")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("blobstore: create data directory: %w", err)
	}
	return &Store{root: dir}, nil
}

// FromEnv returns the Store the environment configures, or the default one.
func FromEnv() (*Store, error) {
	dir := os.Getenv(EnvDataDir)
	if dir == "" {
		dir = DefaultDataDir
	}
	return New(dir)
}

// Create opens a new blob for writing and fails if one already exists under
// that id and variant — an id collision must be a loud error, never a silent
// overwrite of somebody else's file. The caller closes it.
func (s *Store) Create(id uuid.UUID, v Variant) (*os.File, error) {
	path := s.path(id, v)
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("blobstore: create blob directory: %w", err)
	}
	// #nosec G304 -- path is built from a server-generated UUID and a fixed
	// suffix; no caller-supplied string reaches it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return nil, fmt.Errorf("blobstore: create blob: %w", err)
	}
	return f, nil
}

// Open opens a stored blob for reading. A missing one reports fs.ErrNotExist,
// which is how the serving side answers 404 for an attachment whose row is
// gone. The caller closes it.
func (s *Store) Open(id uuid.UUID, v Variant) (*os.File, error) {
	// #nosec G304 -- see Create.
	f, err := os.Open(s.path(id, v))
	if err != nil {
		return nil, fmt.Errorf("blobstore: open blob: %w", err)
	}
	return f, nil
}

// Delete removes an attachment's blobs — the original and the thumbnail, if
// it has one. A blob that is already gone is not an error: the orphan sweep
// and a failed upload's cleanup both run against whatever is actually there.
func (s *Store) Delete(id uuid.UUID) error {
	for _, v := range []Variant{Original, Thumbnail} {
		if err := os.Remove(s.path(id, v)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("blobstore: delete blob: %w", err)
		}
	}
	return nil
}

// path is the one place a blob's location is decided: a two-hex-character
// fan-out directory, then the id itself plus the variant suffix. The fan-out
// keeps a busy instance from putting a million entries in one directory.
func (s *Store) path(id uuid.UUID, v Variant) string {
	name := id.String()
	return filepath.Join(s.root, name[:2], name+string(v))
}
