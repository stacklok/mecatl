package main

import (
	"context"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/microvmcmd"
)

var newLocalMicroVMManager = func() (microvmcmd.Manager, error) {
	manager, _, err := microvmmanager.DefaultLocal()
	return manager, err
}

func runLocalMicroVMCommand(args []string, stdin io.Reader, stdout io.Writer) error {
	if microVMReleaseStampRequired != "" {
		if _, err := microVMReadyRequest(); err != nil {
			return err
		}
	}
	manager, err := newLocalMicroVMManager()
	if err != nil {
		return err
	}
	interactive := false
	if inFile, inOK := stdin.(*os.File); inOK {
		if outFile, outOK := stdout.(*os.File); outOK {
			interactive = term.IsTerminal(int(inFile.Fd())) && term.IsTerminal(int(outFile.Fd()))
		}
	}
	return microvmcmd.Run(context.Background(), args, stdin, stdout, manager, interactive)
}
