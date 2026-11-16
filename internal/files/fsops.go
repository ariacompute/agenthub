package files

import (
	"os"
	"path/filepath"
)

// fsOps is every filesystem call the two backends make that has an error branch
// of its own.
//
// Those branches are the interesting half of a storage backend: a full disk, a
// permission denial, a sharing violation and a rename that lost a race all have
// to become ErrStore instead of leaking a bare *PathError to the HTTP layer. A
// healthy disk never produces any of them, so the only way a test can hold the
// mapping in place is to call the branch with a failing operation. The shipped
// values are the standard library calls; nothing outside a test reassigns them.
var fsOps = struct {
	abs        func(string) (string, error)
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (*os.File, error)
	sync       func(*os.File) error
	closeFile  func(*os.File) error
	chmod      func(string, os.FileMode) error
	rename     func(string, string) error
	remove     func(string) error
	open       func(string) (*os.File, error)
	stat       func(*os.File) (os.FileInfo, error)
	seek       func(*os.File, int64, int) (int64, error)
}{
	abs:        filepath.Abs,
	mkdirAll:   os.MkdirAll,
	createTemp: os.CreateTemp,
	sync:       func(f *os.File) error { return f.Sync() },
	closeFile:  func(f *os.File) error { return f.Close() },
	chmod:      os.Chmod,
	rename:     os.Rename,
	remove:     os.Remove,
	open:       os.Open,
	stat:       func(f *os.File) (os.FileInfo, error) { return f.Stat() },
	seek:       func(f *os.File, off int64, whence int) (int64, error) { return f.Seek(off, whence) },
}
