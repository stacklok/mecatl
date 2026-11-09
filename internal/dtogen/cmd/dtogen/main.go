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
	roots := flag.String("types", "", "comma-separated local named roots; omit to select //dtogen:include declarations (cannot combine)")
	out := flag.String("out", "zz_cbor_generated.go", "single package-local owned output filename")
	repo := flag.String("repo", "", "repository root; derive targets from source-local go:generate directives")
	emit := flag.String("emit", "", "explicit cbor,clone,equal combination or standalone view capability (required outside repository mode)")
	check := flag.Bool("check", false, "check repository inventory and bytes without writing output")
	flag.Parse()
	typesSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "types" {
			typesSet = true
		}
	})
	if flag.NArg() != 0 || (typesSet && *roots == "") || (*repo == "" && (*dir == "" || *check)) || (*repo != "" && (*dir != "" || typesSet || *emit != "" || *out != "zz_cbor_generated.go")) {
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
		err = dtogen.Generate(context.Background(), dtogen.LoadConfig{Dir: *dir, Types: types, Output: *out, Emit: *emit})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
