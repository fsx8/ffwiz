package media

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var extensions = []string{
	".mkv", ".mp4", ".avi", ".mov", ".webm", ".flv", ".wmv", ".m4v",
	".ts", ".m2ts", ".mpg", ".mpeg", ".ogv",
	".mka", ".mp3", ".flac", ".wav", ".ogg", ".m4a", ".aac", ".opus",
	".gif",
}

func IsMediaFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return slices.Contains(extensions, ext)
}

// ListMediaFiles returns media file names in natural order, so that
// "part2.mp4" sorts before "part10.mp4".
func ListMediaFiles(dir string) ([]string, error) {
	files, _, err := ListDir(dir)
	return files, err
}

// ListDir splits a directory's entries into media file names and
// subdirectory names, both in natural order. Hidden (dot) entries and
// regular non-media files are omitted.
func ListDir(dir string) (files, dirs []string, err error) {
	return ListDirMatching(dir, IsMediaFile)
}

// ListDirMatching splits a directory's entries like ListDir, but keeps
// only regular files the matcher accepts. Callers that browse for a
// specific file kind (e.g. the add-track browser's subtitle/audio set)
// pass their own matcher instead of the global media list, so browsing
// here never widens what the rest of the app lists.
func ListDirMatching(dir string, match func(name string) bool) (files, dirs []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isDir := e.IsDir()
		// Symlinked directories look like files to ReadDir (DirEntry uses
		// lstat); resolve them so they stay navigable.
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				isDir = fi.IsDir()
			}
		}
		if isDir {
			dirs = append(dirs, e.Name())
		} else if match(e.Name()) {
			files = append(files, e.Name())
		}
	}
	slices.SortFunc(files, naturalCompare)
	slices.SortFunc(dirs, naturalCompare)
	return files, dirs, nil
}

func naturalCompare(a, b string) int {
	less := naturalLess(a, b)
	if less {
		return -1
	}
	if naturalLess(b, a) {
		return 1
	}
	return 0
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// foldByte lowercases ASCII letters so comparisons are case-insensitive;
// filenames rarely mix scripts where full Unicode folding would matter.
func foldByte(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// naturalLess compares byte-by-byte (case-insensitively), but runs of digits
// are compared by numeric value so "file9" < "file10".
func naturalLess(a, b string) bool {
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		if isDigit(a[ia]) && isDigit(b[ib]) {
			ja, jb := ia, ib
			for ja < len(a) && isDigit(a[ja]) {
				ja++
			}
			for jb < len(b) && isDigit(b[jb]) {
				jb++
			}
			na := strings.TrimLeft(a[ia:ja], "0")
			nb := strings.TrimLeft(b[ib:jb], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			ia, ib = ja, jb
			continue
		}
		if fa, fb := foldByte(a[ia]), foldByte(b[ib]); fa != fb {
			return fa < fb
		}
		ia++
		ib++
	}
	// Whichever string still has characters left is larger. Fully
	// folded-equal strings fall back to a raw byte comparison so the
	// resulting sort order is deterministic.
	ta, tb := a[ia:], b[ib:]
	if len(ta) != len(tb) {
		return len(ta) < len(tb)
	}
	return ta < tb
}
