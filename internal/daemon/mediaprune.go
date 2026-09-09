package daemon

import (
	"os"
	"path/filepath"
	"sort"
)

// maxCacheBytes caps the whole attachment cache.
//
// Individual downloads are already bounded, but nothing bounded their sum: a
// thread of large attachments, re-fetched across restarts, grows the cache
// until the disk fills. Eviction is oldest-first by modification time, which
// for a read-through cache of immutable files approximates least-recently-added
// and costs one stat per file.
const maxCacheBytes = 256 << 20

// prune deletes cached attachments, oldest first, until the cache is under the
// cap. Errors are returned for logging rather than failing the download that
// triggered them: a cache that could not be trimmed is a housekeeping problem,
// not a reason to refuse the user their attachment.
func (m *mediaCache) prune() (freed int64, err error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	type file struct {
		path string
		size int64
		mod  int64
	}
	files := make([]file, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		files = append(files, file{
			path: filepath.Join(m.dir, e.Name()),
			size: info.Size(),
			mod:  info.ModTime().UnixNano(),
		})
		total += info.Size()
	}
	if total <= maxCacheBytes {
		return 0, nil
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files {
		if total <= maxCacheBytes {
			break
		}
		if rmErr := os.Remove(f.path); rmErr != nil {
			if err == nil {
				err = rmErr
			}
			continue
		}
		total -= f.size
		freed += f.size
	}
	return freed, err
}
