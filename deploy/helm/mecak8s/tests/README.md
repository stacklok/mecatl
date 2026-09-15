# Helm unit tests

Run the chart-native tests from the repository root:

```sh
task deploy:helm-unittest
```

The task installs the pinned `helm-unittest` plugin when it is absent and
renders the chart without a Kubernetes cluster. These suites cover direct
relationships between Helm values and rendered resources, plus expected schema
and template failures.

To test a published chart, extract it before running the plugin; version 1.0.3
does not discover suites inside a packaged `.tgz`:

```sh
helm pull oci://ghcr.io/stacklok/mecatl/charts/mecak8s --version <version> --untar
helm unittest mecak8s
```

Keep tests in `chart_test.go` when they need Kubernetes Go types, generated
matrices, application parsers, files outside the chart, or comparisons between
multiple renders. `task deploy:check` validates complete renders with
`kubeconform`. The Kind end-to-end suite verifies installation and runtime
behavior in a Kubernetes cluster.
