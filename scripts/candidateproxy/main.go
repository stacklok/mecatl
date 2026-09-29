// Command candidateproxy assembles a disposable file GOPROXY from candidate source
// modules and already-downloaded third-party artifacts. It never publishes sums.
package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

const candidate = "v0.1.0-dev"

var modules = []struct{ path, dir string }{
	{"github.com/stacklok/mecatl/internal/adaptersupport", "internal/adaptersupport"},
	{"github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver", "contracts/gen/go/mecatl/driver"},
	{"github.com/stacklok/mecatl/adapters", "adapters"},
}

func main() {
	if len(os.Args) != 4 {
		panic("usage: candidateproxy REPO SCRATCH GOMODCACHE")
	}
	repo, scratch, cache := os.Args[1], os.Args[2], os.Args[3]
	// #nosec G703 -- operator-supplied CLI paths are intentionally used to assemble a disposable proxy; the invoking script confines its output under .scratch.
	must(os.MkdirAll(scratch, 0755))
	proxy := filepath.Join(scratch, "proxy")
	// Link cached download endpoints rather than copying gigabytes of unrelated
	// archives. A file-only proxy cannot fetch missing zips over the network.
	downloads := filepath.Join(cache, "cache/download")
	// #nosec G703 -- the operator's module cache is deliberately traversed to link downloaded artifacts into the offline proxy.
	must(filepath.WalkDir(downloads, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || d.Name() != "@v" {
			return nil
		}
		rel, err := filepath.Rel(downloads, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(rel), "github.com/stacklok/mecatl/") {
			return filepath.SkipDir
		}
		dst := filepath.Join(proxy, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		// #nosec G122 -- cached download directories are intentionally symlinked, not copied, into this disposable proxy.
		if err := os.Symlink(path, dst); err != nil {
			return err
		}
		return filepath.SkipDir
	}))
	// The engine baseline is a real downloaded version; never repackage local engine.
	baseline := "v0.15.1-0.20260929125653-6142f5252a09"
	enginePath := "github.com/stacklok/mecatl/engine"
	engineSrc := filepath.Join(cache, "cache/download", enginePath, "@v")
	engineDst := filepath.Join(proxy, enginePath, "@v")
	for _, ext := range []string{".zip", ".mod", ".info"} {
		must(copyFile(filepath.Join(engineSrc, baseline+ext), filepath.Join(engineDst, baseline+ext)))
	}
	appendList(filepath.Join(engineDst, "list"), baseline)
	for _, m := range modules {
		staged := filepath.Join(scratch, "source", m.dir)
		must(copyTree(filepath.Join(repo, m.dir), staged))
		modPath := filepath.Join(staged, "go.mod")
		// #nosec G703 -- source modules are read from the operator's repo path and copied into disposable scratch staging.
		b, err := os.ReadFile(modPath)
		must(err)
		f, err := modfile.Parse(modPath, b, nil)
		must(err)
		if len(f.Replace) != 0 {
			panic("candidate module has replace directives: " + m.path)
		}
		if m.dir == "adapters" {
			must(f.AddRequire(modules[0].path, candidate))
			must(f.AddRequire(modules[1].path, candidate))
		}
		b, err = f.Format()
		must(err)
		// #nosec G703 -- the staged module manifest is written only under the script-validated scratch output.
		must(os.WriteFile(modPath, b, 0644))
		escaped, err := module.EscapePath(m.path)
		must(err)
		dst := filepath.Join(proxy, escaped, "@v")
		// #nosec G703 -- candidate module artifacts are written beneath the script-validated scratch proxy.
		must(os.MkdirAll(dst, 0755))
		must(copyFile(modPath, filepath.Join(dst, candidate+".mod")))
		// #nosec G703 -- the candidate version metadata stays under that disposable proxy directory.
		must(os.WriteFile(filepath.Join(dst, candidate+".info"), []byte(fmt.Sprintf(`{"Version":%q,"Time":%q}`, candidate, time.Unix(0, 0).UTC().Format(time.RFC3339))), 0644))
		// #nosec G703 -- the candidate version list stays under that disposable proxy directory.
		must(os.WriteFile(filepath.Join(dst, "list"), []byte(candidate+"\n"), 0644))
		// #nosec G703 -- candidate ZIPs are created only inside the disposable scratch proxy.
		out, err := os.Create(filepath.Join(dst, candidate+".zip"))
		must(err)
		err = modzip.CreateFromDir(out, module.Version{Path: m.path, Version: candidate}, staged)
		closeErr := out.Close()
		must(err)
		must(closeErr)
	}
	// The fixture is copied, never edited in the checkout; go.sum is generated only here.
	must(copyTree(filepath.Join(repo, "integration/external-persistence-consumer"), filepath.Join(scratch, "consumer")))
	engineOnly := filepath.Join(scratch, "engine-only")
	// #nosec G703 -- the engine-only proof module is created under the script-validated scratch output.
	must(os.MkdirAll(engineOnly, 0755))
	// #nosec G703 -- the generated manifest is confined to the same disposable proof directory.
	must(os.WriteFile(filepath.Join(engineOnly, "go.mod"), []byte("module example.org/mecatl-engine-closure-proof\n\ngo 1.27.0\n\nrequire github.com/stacklok/mecatl/engine "+baseline+"\n"), 0644))
}

func appendList(path, version string) {
	// #nosec G703 -- version lists are written only under the disposable scratch proxy.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(err)
	_, err = fmt.Fprintln(f, version)
	must(err)
	must(f.Close())
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func copyFile(src, dst string) error {
	// #nosec G703 -- sources are the operator-selected checkout and module cache used to construct the disposable proxy.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// #nosec G703 -- destinations are paths inside the disposable scratch proxy or staging tree.
	if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	// #nosec G703 -- copied module artifacts are created only within the disposable scratch tree.
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func copyTree(src, dst string) error {
	// #nosec G703 -- copyTree reads the operator-selected source module/fixture directories.
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".scratch" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}
