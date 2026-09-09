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
const maxCacheBytes int64 = 256 << 20

// pruneCaches trims everything this plugin writes to the cache.
//
// The attachment cache and the capture directory are different places: the
// panel writes webcam photos and voice recordings to the cache root, while
// downloaded attachments live in media/ underneath it. Trimming only the
// latter left the former growing without bound.
func (d *Daemon) pruneCaches() (int64, error) {
	freed, err := d.media.prune()
	capFreed, capErr := pruneDir(d.paths.Cache, maxCaptureBytes, isOwnCapture)
	freed += capFreed
	if err == nil {
		err = capErr
	}
	return freed, err
}

// maxCaptureBytes caps webcam photos and voice recordings. These are the
// user's own, and a sent one is deleted immediately, so this only has to catch
// what a crash or an unclean shutdown leaves behind.
const maxCaptureBytes = 64 << 20

// prune deletes cached attachments, oldest first, until the cache is under the
// cap. Errors are returned for logging rather than failing the download that
// triggered them: a cache that could not be trimmed is a housekeeping problem,
// not a reason to refuse the user their attachment.
func (m *mediaCache) prune() (int64, error) {
	// Everything in the media directory is ours, so nothing is filtered out.
	return pruneDir(m.dir, maxCacheBytes, func(string) bool { return true })
}

// pruneDir trims dir to limit, oldest first, considering only files that mine
// accepts. Anything else in the directory is left alone: the capture directory
// is shared with files this function has no business deleting.
func pruneDir(dir string, limit int64, mine func(name string) bool) (freed int64, err error) {
	entries, err := os.ReadDir(dir)
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
		if e.IsDir() || !mine(e.Name()) {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		files = append(files, file{
			path: filepath.Join(dir, e.Name()),
			size: info.Size(),
			mod:  info.ModTime().UnixNano(),
		})
		total += info.Size()
	}
	if total <= limit {
		return 0, nil
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files {
		if total <= limit {
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
