package agentimport

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyStats reports what an import copied and deliberately skipped.
type CopyStats struct {
	Files           int
	Skills          int
	SkippedSymlinks int
}

// CopyWorkspace merges regular files from src into dst without overwriting any
// existing path. The source repository's .git administration directory is not
// portable agent context and is omitted. Symlinks and special files are skipped.
func CopyWorkspace(src, dst string) (CopyStats, error) {
	return copyTree(src, dst, map[string]bool{".git": true})
}

// CopySkills copies each direct <name>/SKILL.md bundle from src into dst. A
// bundle includes its supporting files. Existing skill names are never replaced.
func CopySkills(src, dst string) (CopyStats, error) {
	return CopySkillSources([]string{src}, dst)
}

// CopySkillSources imports bundles from all sources after checking cross-source
// and destination name collisions, so a duplicate is reported before any bundle
// is written.
func CopySkillSources(sources []string, dst string) (CopyStats, error) {
	var total CopyStats
	type bundle struct{ name, path string }
	var bundles []bundle
	seen := make(map[string]string)
	for _, src := range sources {
		entries, err := os.ReadDir(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return total, fmt.Errorf("read skills directory %q: %w", src, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(src, entry.Name())
			manifest, err := os.Lstat(filepath.Join(path, "SKILL.md"))
			if err != nil || !manifest.Mode().IsRegular() {
				continue
			}
			if previous, ok := seen[entry.Name()]; ok {
				return total, fmt.Errorf("skill %q exists in both %s and %s; pass explicit --skills-dir values without collisions", entry.Name(), previous, src)
			}
			if _, err := os.Lstat(filepath.Join(dst, entry.Name())); err == nil {
				return total, fmt.Errorf("destination skill already exists: %s", filepath.Join(dst, entry.Name()))
			} else if !os.IsNotExist(err) {
				return total, fmt.Errorf("inspect destination skill %q: %w", entry.Name(), err)
			}
			seen[entry.Name()] = src
			bundles = append(bundles, bundle{name: entry.Name(), path: path})
		}
	}
	for _, bundle := range bundles {
		stats, err := copyTree(bundle.path, filepath.Join(dst, bundle.name), nil)
		if err != nil {
			return total, fmt.Errorf("import skill %q: %w", bundle.name, err)
		}
		stats.Skills = 1
		total.Files += stats.Files
		total.Skills += stats.Skills
		total.SkippedSymlinks += stats.SkippedSymlinks
	}
	return total, nil
}

type copyEntry struct {
	src  string
	dst  string
	mode fs.FileMode
	dir  bool
}

func copyTree(src, dst string, skipTopLevel map[string]bool) (CopyStats, error) {
	var stats CopyStats
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return stats, fmt.Errorf("resolve source %q: %w", src, err)
	}
	srcAbs, err = filepath.EvalSymlinks(srcAbs)
	if err != nil {
		return stats, fmt.Errorf("resolve source symlinks %q: %w", src, err)
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return stats, fmt.Errorf("resolve destination %q: %w", dst, err)
	}
	info, err := os.Stat(srcAbs)
	if err != nil {
		return stats, fmt.Errorf("stat source %q: %w", srcAbs, err)
	}
	if !info.IsDir() {
		return stats, fmt.Errorf("source %q is not a directory", srcAbs)
	}
	if dstAbs == srcAbs || strings.HasPrefix(dstAbs, srcAbs+string(filepath.Separator)) {
		return stats, fmt.Errorf("destination %q must not be inside source %q", dstAbs, srcAbs)
	}

	var plan []copyEntry
	err = filepath.WalkDir(srcAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcAbs, path)
		if err != nil || rel == "." {
			return err
		}
		if entry.IsDir() && !strings.ContainsRune(rel, filepath.Separator) && skipTopLevel[entry.Name()] {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			stats.SkippedSymlinks++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		target := filepath.Join(dstAbs, rel)
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("destination path already exists: %s", target)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect destination %q: %w", target, err)
		}
		plan = append(plan, copyEntry{src: path, dst: target, mode: info.Mode(), dir: info.IsDir()})
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("plan copy from %q: %w", srcAbs, err)
	}
	if err := os.MkdirAll(dstAbs, 0o700); err != nil {
		return stats, fmt.Errorf("create destination %q: %w", dstAbs, err)
	}
	for _, item := range plan {
		if item.dir {
			if err := os.Mkdir(item.dst, item.mode.Perm()); err != nil {
				return stats, fmt.Errorf("create directory %q: %w", item.dst, err)
			}
			continue
		}
		if err := copyRegularFile(item.src, item.dst, item.mode.Perm()); err != nil {
			return stats, err
		}
		stats.Files++
	}
	return stats, nil
}

func copyRegularFile(src, dst string, mode fs.FileMode) (err error) {
	in, err := os.Open(src) //nolint:gosec // operator-selected local import source
	if err != nil {
		return fmt.Errorf("open source file %q: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // destination was confined by filepath.Rel/Join and preflighted
	if err != nil {
		return fmt.Errorf("create destination file %q: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %q to %q: %w", src, dst, err)
	}
	return nil
}
