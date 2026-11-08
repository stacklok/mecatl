// Command dtogen generates selected typed DTO operations from Go declarations.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/stacklok/mecatl/internal/dtogen"
)

func main() {
	dir := flag.String("dir", "", "declaring package directory")
	roots := flag.String("types", "", "comma-separated local named root types")
	out := flag.String("out", "zz_cbor_generated.go", "single package-local owned output filename")
	repo := flag.String("repo", "", "repository root; derive targets from source-local go:generate directives")
	views := flag.String("views", "", "package-local JSON view projection map")
	emit := flag.String("emit", "", "explicit comma-separated cbor,clone,equal,view capabilities (required outside repository mode)")
	check := flag.Bool("check", false, "check repository inventory and bytes without writing output")
	flag.Parse()
	if flag.NArg() != 0 || (*repo == "" && (*dir == "" || (*roots == "" && *views == "") || *check)) || (*repo != "" && (*dir != "" || *roots != "" || *emit != "" || *views != "" || *out != "zz_cbor_generated.go")) {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	if *repo != "" {
		err = dtogen.GenerateRepository(context.Background(), *repo, *check)
	} else {
		var types []string
		if *roots != "" {
			types = strings.Split(*roots, ",")
		}
		err = dtogen.Generate(context.Background(), dtogen.LoadConfig{Dir: *dir, Types: types, Output: *out, Emit: *emit, Views: *views})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
