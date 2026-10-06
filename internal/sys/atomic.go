package sys

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces p with data so that a reader, or the file after
// Android kills the app mid-write, sees either the old content or the new,
// never an empty or partial file: a temp file in the same folder, synced,
// then renamed over p (and the folder synced, best effort).
func WriteFileAtomic(p string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(p)
	f, err := os.CreateTemp(dir, "."+filepath.Base(p)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
