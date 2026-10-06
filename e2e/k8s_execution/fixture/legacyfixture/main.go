//go:build kind_execution_e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: legacyfixture NAMESPACE PROFILES OUTPUT")
		os.Exit(2)
	}
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		fail(err)
	}
	d, err := dynamic.NewForConfig(config)
	if err != nil {
		fail(err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		fail(err)
	}
	seed, err := executioncontroller.SeedLegacyMigrationFixture(context.Background(), d, kube, os.Args[1], os.Args[2])
	if err != nil {
		fail(err)
	}
	encoded, err := json.Marshal(seed)
	if err != nil {
		fail(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(os.Args[3], encoded, 0o600); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
