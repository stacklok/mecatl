//go:build ignore

// module-artifact constructs one exact-version file-proxy artifact from an
// immutable Git commit. Build from the trusted root module (x/mod v0.42.0):
//
//	GOWORK=off go build -o /path/to/module-artifact .github/scripts/module-artifact.go
//
// This is artifact construction, not release authorization or qualification.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

func main() {
	if err := artifact(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func artifact() error {
	repo := flag.String("repo", "", "Git worktree root")
	sha := flag.String("sha", "", "full immutable commit SHA")
	dir := flag.String("dir", "", "module directory relative to repository root (. for root)")
	identity := flag.String("module", "", "exact module identity")
	version := flag.String("version", "", "canonical exact release version")
	out := flag.String("out", "", "new file proxy root (must not exist)")
	flag.Parse()
	if flag.NArg() != 0 || *repo == "" || *out == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(*sha) {
		return fmt.Errorf("repo, full sha, dir, module, version and new out are required")
	}
	if *dir == "" || path.Clean(*dir) != *dir || strings.HasPrefix(*dir, "-") || strings.HasPrefix(*dir, "/") || *dir == ".." || strings.HasPrefix(*dir, "../") || strings.Contains(*dir, "\\") {
		return fmt.Errorf("invalid module directory: %q", *dir)
	}
	if err := module.Check(*identity, *version); err != nil {
		return err
	}
	if semver.Canonical(*version) != *version || module.IsPseudoVersion(*version) {
		return fmt.Errorf("expected canonical release version")
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		data, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %v: %w", args, err)
		}
		return data, nil
	}
	commit, err := git("rev-parse", *sha+"^{commit}")
	if err != nil || strings.TrimSpace(string(commit)) != *sha {
		return fmt.Errorf("sha must identify an exact commit: %s", *sha)
	}
	manifest, err := git("show", *sha+":"+path.Join(*dir, "go.mod"))
	if err != nil {
		return err
	}
	mod, err := modfile.Parse("go.mod", manifest, nil)
	if err != nil {
		return err
	}
	if mod.Module == nil || mod.Module.Mod.Path != *identity {
		return fmt.Errorf("go.mod module identity mismatch")
	}
	if len(mod.Replace) != 0 {
		return fmt.Errorf("release artifact contains replacements")
	}
	timestamp, err := git("show", "-s", "--format=%cI", *sha)
	if err != nil {
		return err
	}
	ep, err := module.EscapePath(*identity)
	if err != nil {
		return err
	}
	ev, err := module.EscapeVersion(*version)
	if err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0o755); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(*out)
		}
	}()
	target := filepath.Join(*out, filepath.FromSlash(ep), "@v")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(target, ev+".zip")
	f, err := os.OpenFile(zipPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	subdir := *dir
	if subdir == "." {
		subdir = ""
	}
	zipErr := modzip.CreateFromVCS(f, module.Version{Path: *identity, Version: *version}, root, *sha, subdir)
	closeErr := f.Close()
	if zipErr != nil {
		return zipErr
	}
	if closeErr != nil {
		return closeErr
	}
	sum, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		return err
	}
	modSum, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(manifest)), nil
	})
	if err != nil {
		return err
	}
	info, err := json.Marshal(struct{ Version, Time string }{*version, strings.TrimSpace(string(timestamp))})
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{ev + ".mod": manifest, ev + ".info": info, "list": []byte(*version + "\n")} {
		if err := os.WriteFile(filepath.Join(target, name), data, 0o644); err != nil {
			return err
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
		"module": *identity, "version": *version, "sha": *sha, "directory": *dir,
		"sum": sum, "go_mod_sum": modSum, "proxy_path": ep + "/@v/" + ev,
	}); err != nil {
		return err
	}
	success = true
	return nil
}
