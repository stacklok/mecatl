# Helm unit tests

Run the chart-native tests from the repository root:

```sh
task deploy:helm-unittest
```

The task installs `helm-unittest` 1.0.3 when the plugin is absent and renders
the chart without a Kubernetes cluster. It stops with remediation instructions
when a different plugin version is installed, so local and CI runs use the same
test engine.

To test a published chart, extract it before running the plugin; version 1.0.3
does not discover suites inside a packaged `.tgz`:

```sh
helm pull oci://ghcr.io/stacklok/mecatl/charts/mecak8s --version <version> --untar
helm unittest mecak8s
```

## Choose the right test layer

Use `helm-unittest` for direct relationships between chart values and rendered
resources, including expected JSON Schema and template failures. Keep a focused
case here when it protects a chart contract, even if a broader Go matrix also
reaches that behavior.

Keep tests in `chart_test.go` when they need Kubernetes Go types, generated
matrices, application parsers, files outside the chart, or comparisons between
multiple renders. `task deploy:check` renders every `ci/*-values.yaml` profile
and validates complete manifests with `kubeconform`. Helm lint checks chart
structure and production values before release packaging. The Kind end-to-end
suite verifies installation, networking, storage, and runtime behavior in a
Kubernetes cluster.
