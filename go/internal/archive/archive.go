package archive

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packer/internal/filter"
	"packer/internal/progress"
)

// Options controls archive creation.
type Options struct {
	SkipSymlinks bool
	Filter       *filter.Filter
	Progress     *progress.Meter
}

// Create writes a tar stream of the given directories to w. Symlinks are stored
// as links (never followed). Each input dir is rooted under its base name.
func Create(w io.Writer, dirs []string, opt Options) error {
	tw := tar.NewWriter(w)
	seen := map[string]string{}
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return err
		}
		base := filepath.Clean(abs)
		root := filepath.Base(base)
		if root == "." || root == string(filepath.Separator) || root == "" {
			return fmt.Errorf("cannot derive an archive name for input %q", d)
		}
		if prev, ok := seen[root]; ok {
			return fmt.Errorf("duplicate archive root %q from inputs %q and %q", root, prev, d)
		}
		seen[root] = d

		if err := walkDir(tw, base, root, d, opt); err != nil {
			return err
		}
	}
	return tw.Close()
}

// joinDisplay builds the path shown in verbose mode: the input argument as the
// user typed it (trailing slashes trimmed) plus the child name. Kept identical
// to the Python implementation for cross-impl parity.
func joinDisplay(disp, name string) string {
	return strings.TrimRight(disp, "/") + "/" + name
}

func walkDir(tw *tar.Writer, base, root, disp string, opt Options) error {
	prog := opt.Progress
	// Top-level bookkeeping for verbose mode. WalkDir is a sorted DFS pre-order,
	// so all nodes of one top-level entry are visited consecutively; a change of
	// the first path component means the previous top-level subtree is complete.
	var (
		curTop     string
		curTopDir  bool
		curTopPath string
		startFiles int64
		startBytes int64
	)
	flushTop := func() {
		if curTopDir {
			f, b := prog.Snapshot()
			prog.LogDir(curTopPath, f-startFiles, b-startBytes)
		}
		curTop, curTopDir = "", false
	}
	err := filepath.WalkDir(base, func(path string, de fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		flt := opt.Filter

		if rel == "." {
			return writeDir(tw, path, de, root+"/")
		}

		top := slashRel
		if i := strings.IndexByte(slashRel, '/'); i >= 0 {
			top = slashRel[:i]
		}
		isTop := top == slashRel // no separator => this node is the top-level entry
		if prog.Verbose() && top != curTop {
			flushTop()
			curTop = top
			curTopPath = joinDisplay(disp, top)
			startFiles, startBytes = prog.Snapshot()
		}

		isDir := de.IsDir()
		if isDir && flt != nil && flt.ExcludesDir(slashRel) {
			return fs.SkipDir
		}

		mode := de.Type()
		switch {
		case mode&fs.ModeSymlink != 0:
			if opt.SkipSymlinks {
				return nil
			}
			if flt != nil && !flt.Keep(slashRel, false) {
				return nil
			}
			if err := writeSymlink(tw, path, de, root+"/"+slashRel); err != nil {
				return err
			}
			prog.AddFile()
			if isTop {
				prog.LogFile(curTopPath)
			}
			return nil
		case isDir:
			if flt == nil || flt.Keep(slashRel, true) {
				if err := writeDir(tw, path, de, root+"/"+slashRel+"/"); err != nil {
					return err
				}
			}
			if isTop {
				curTopDir = true // logged (with subtree delta) once its subtree completes
			}
			return nil
		case mode.IsRegular():
			if flt != nil && !flt.Keep(slashRel, false) {
				return nil
			}
			if err := writeReg(tw, path, de, root+"/"+slashRel, prog); err != nil {
				return err
			}
			prog.AddFile()
			if isTop {
				prog.LogFile(curTopPath)
			}
			return nil
		default:
			fmt.Fprintf(os.Stderr, "packer: skipping special file %s\n", slashRel)
			return nil
		}
	})
	if err != nil {
		return err
	}
	flushTop() // finalize the last top-level directory of this root
	return nil
}

func writeDir(tw *tar.Writer, path string, de fs.DirEntry, name string) error {
	info, err := de.Info()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	return tw.WriteHeader(hdr)
}

func writeSymlink(tw *tar.Writer, path string, de fs.DirEntry, name string) error {
	info, err := de.Info()
	if err != nil {
		return err
	}
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, target)
	if err != nil {
		return err
	}
	hdr.Name = name
	return tw.WriteHeader(hdr)
}

func writeReg(tw *tar.Writer, path string, de fs.DirEntry, name string, prog *progress.Meter) error {
	info, err := de.Info()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, prog.Reader(f))
	return err
}

// Extract writes the tar stream in r into dest, guarding against path traversal
// and extraction through symlinks. prog (which may be nil) reports live progress.
func Extract(r io.Reader, dest string, overwrite bool, prog *progress.Meter) error {
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	type dirMode struct {
		path string
		mode os.FileMode
	}
	var dirs []dirMode
	loggedTop := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean("/" + filepath.FromSlash(hdr.Name))
		target := filepath.Join(destAbs, clean)
		if !withinRoot(destAbs, target) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		// Verbose: report each first-level entry (a direct child of the
		// destination, i.e. an archive root) once, path only.
		if prog.Verbose() {
			if name := strings.TrimRight(filepath.ToSlash(hdr.Name), "/"); name != "" && !strings.Contains(name, "/") && !loggedTop[name] {
				loggedTop[name] = true
				prog.LogFile(joinDisplay(dest, name))
			}
		}
		parent := filepath.Dir(target)
		if err := ensureNoSymlinkParent(destAbs, parent); err != nil {
			return err
		}
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{target, os.FileMode(hdr.Mode).Perm()})
		case tar.TypeSymlink:
			if err := removeIfExists(target, overwrite); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			prog.AddFile()
		case tar.TypeReg, tar.TypeRegA:
			if err := removeIfExists(target, overwrite); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(prog.Writer(f), tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if err := os.Chmod(target, os.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
			os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			prog.AddFile()
		default:
			fmt.Fprintf(os.Stderr, "packer: skipping unsupported entry %q (type %d)\n", hdr.Name, hdr.Typeflag)
		}
	}
	// Apply directory modes deepest-first so restrictive perms do not block
	// writing their contents during extraction.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].path) > len(dirs[j].path) })
	for _, d := range dirs {
		if err := os.Chmod(d.path, d.mode); err != nil {
			return err
		}
	}
	return nil
}

func withinRoot(root, target string) bool {
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func ensureNoSymlinkParent(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to extract through symlink %q", cur)
		}
	}
	return nil
}

func removeIfExists(target string, overwrite bool) error {
	fi, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("cannot overwrite directory with file/symlink: %q", target)
	}
	if !overwrite {
		return fmt.Errorf("destination exists (use --overwrite): %q", target)
	}
	return os.Remove(target)
}
