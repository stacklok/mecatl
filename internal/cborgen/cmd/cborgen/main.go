// Command cborgen generates strict typed persistence codecs from Go declarations.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/stacklok/mecatl/internal/cborgen"
)

func main() {
	dir := flag.String("dir", "", "declaring package directory")
	roots := flag.String("types", "", "comma-separated local struct root names")
	out := flag.String("out", "zz_cbor_generated.go", "single package-local owned output filename")
	repo := flag.String("repo", "", "repository root; derive targets from source-local go:generate directives")
	check := flag.Bool("check", false, "check repository inventory and bytes without writing output")
	flag.Parse()
	if flag.NArg() != 0 || (*repo == "" && (*dir == "" || *roots == "" || *check)) || (*repo != "" && (*dir != "" || *roots != "" || *out != "zz_cbor_generated.go")) {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	if *repo != "" {
		err = cborgen.GenerateRepository(context.Background(), *repo, *check)
	} else {
		err = cborgen.Generate(context.Background(), cborgen.LoadConfig{Dir: *dir, Types: strings.Split(*roots, ","), Output: *out})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
