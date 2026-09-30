// Command candidateproxy packages current adapter source for an offline external consumer.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

const candidate = "v0.1.0-dev"

func main() {
	if len(os.Args) != 3 {
		panic("usage: candidateproxy REPO SCRATCH")
	}
	repo, scratch := os.Args[1], os.Args[2]
	// #nosec G703 -- scratch is the intentional CLI-selected disposable destination; all child operations use scratchRoot.
	must(os.MkdirAll(scratch, 0755))
	repoRoot, err := os.OpenRoot(repo)
	must(err)
	defer func() { must(repoRoot.Close()) }()
	scratchRoot, err := os.OpenRoot(scratch)
	must(err)
	defer func() { must(scratchRoot.Close()) }()

	const manifest = "adapters/go.mod"
	b, err := repoRoot.ReadFile(manifest)
	must(err)
	f, err := modfile.Parse(manifest, b, nil)
	must(err)
	if len(f.Replace) != 0 {
		panic("adapter manifest has replace directives")
	}
	engine := ""
	for _, req := range f.Require {
		if req.Mod.Path == "github.com/stacklok/mecatl/engine" {
			engine = req.Mod.Version
		}
	}
	if engine == "" {
		panic("adapter manifest has no engine baseline")
	}
	escaped, err := module.EscapePath(f.Module.Mod.Path)
	must(err)
	proxyDir := filepath.Join("proxy", escaped, "@v")
	must(scratchRoot.MkdirAll(proxyDir, 0755))
	must(scratchRoot.WriteFile(filepath.Join(proxyDir, candidate+".mod"), b, 0644))
	must(scratchRoot.WriteFile(filepath.Join(proxyDir, candidate+".info"), []byte(fmt.Sprintf(`{"Version":%q,"Time":%q}`, candidate, time.Unix(0, 0).UTC().Format(time.RFC3339))), 0644))
	must(scratchRoot.WriteFile(filepath.Join(proxyDir, "list"), []byte(candidate+"\n"), 0644))
	out, err := scratchRoot.OpenFile(filepath.Join(proxyDir, candidate+".zip"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	must(err)
	err = modzip.CreateFromDir(out, module.Version{Path: f.Module.Mod.Path, Version: candidate}, filepath.Join(repoRoot.Name(), "adapters"))
	closeErr := out.Close()
	must(err)
	must(closeErr)

	consumer, err := fs.Sub(repoRoot.FS(), "integration/external-persistence-consumer")
	must(err)
	must(os.CopyFS(filepath.Join(scratchRoot.Name(), "consumer"), consumer))
	must(scratchRoot.MkdirAll("engine-only", 0755))
	must(scratchRoot.WriteFile("engine-only/go.mod", []byte("module example.org/mecatl-engine-closure-proof\n\ngo 1.27.0\n\nrequire github.com/stacklok/mecatl/engine "+engine+"\n"), 0644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
