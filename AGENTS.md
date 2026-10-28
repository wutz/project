# project - AI Agent Guide

This repository is a Kubebuilder project with three controllers. It reconciles the
`Project` CRD and the `InstanceType` CRD (both in `workspace.wutz.dev/v1alpha1`), and the
registry controller that gives each Project a Harbor project.

## Project Structure

Single-group layout (the only layout used here):

```
cmd/main.go                    Manager entry (registers controllers)
api/v1alpha1/*_types.go        Project and InstanceType CRD schemas (+kubebuilder markers)
api/v1alpha1/zz_generated.*    Auto-generated (DO NOT EDIT)
internal/controller/*          Project, InstanceType and registry reconcilers
internal/controller/testdata/  CRDs loaded by envtest (Kueue, InstanceType)
pkg/platform/                  Platform ConfigMap loader
pkg/harbor/                    Harbor API client
pkg/naming/                    Namespace and resource naming
pkg/instancetype/              InstanceType reads (unstructured)
pkg/kf/, pkg/sandbox/          Kueue, Kubeflow Trainer and agent-sandbox API groups
config/crd/bases/*             Generated CRDs (DO NOT EDIT)
config/rbac/role.yaml          Generated RBAC (DO NOT EDIT)
config/samples/*               Example CRs (edit these)
charts/chart/                  Helm chart (published by the release workflow)
test/e2e/                     End-to-end suite (needs a dedicated Kind cluster)
Makefile                       Build, test, lint, chart and release targets
PROJECT                        Kubebuilder metadata (generated)
```

## Critical Rules

### Never Edit These (Auto-Generated)
- `config/crd/bases/*.yaml`: from `make manifests`
- `config/rbac/role.yaml`: from `make manifests`
- `**/zz_generated.*.go`: from `make generate`
- `PROJECT`: from `kubebuilder` commands

### Never Remove Scaffold Markers
Do not delete `// +kubebuilder:scaffold:*` comments. The CLI injects code at these markers.

### Keep the Public Contract Stable
- The API group `workspace.wutz.dev`, the field owner `project`, and the finalizers
  `workspace.wutz.dev/*` are recorded on existing objects. Renaming any of them needs a migration.
- The platform ConfigMap `workspace-platform` (key `config.yaml`) in the namespace set by
  `PROJECT_NAMESPACE` is read by `pkg/platform`. Its schema is public too.
- The chart values, README and CRD field names are public API. Changes need a release note.
- Member roles (`workspace-edit`, `workspace-view`) aggregate the ClusterRoles labelled
  `workspace.wutz.dev/aggregate-to-member`. A platform adds rules for its own kinds with its
  own labelled ClusterRoles; do not add kinds the operator does not own.

### Use the CLI for Scaffolding
Use `kubebuilder create api` and `kubebuilder create webhook` to scaffold. Do not create
scaffold files by hand.

### E2E Tests Require an Isolated Kind Cluster
`make test-e2e` expects a dedicated Kind cluster, similar to CI. Do not run it against a
shared or production cluster.

## After Making Changes

After editing `*_types.go` or kubebuilder markers:

```bash
make manifests   # regenerate CRDs and RBAC from markers
make generate    # regenerate DeepCopy methods
```

After editing `*.go` files:

```bash
make lint-fix    # auto-fix style
make test        # unit and controller tests (envtest)
```

When the chart or manifests change, also run `make helm-lint`.

## Testing

```bash
make test         # envtest: real API server and etcd
make test-e2e     # dedicated Kind cluster only
```

Tests use Ginkgo and Gomega. Check `internal/controller/suite_test.go` for setup. The
controller suite loads the Kueue and InstanceType CRDs from `internal/controller/testdata/`.

## Controller Design

RBAC markers live next to the reconciler in `internal/controller/*_controller.go`. Keep
them minimal and regenerate `config/rbac/role.yaml` after changing them.

Implementation rules:
- Reconciliation is idempotent: safe to run many times.
- Re-fetch before update (`r.Get` then `r.Update`) to avoid conflicts.
- Use `metav1.Condition` for status, not custom string fields.
- Set owner references so children are garbage collected.
- Watch secondary resources with `.Owns()` or `.Watches()`, not only `RequeueAfter`.
- Clean up external resources (Harbor projects, ServiceAccount tokens) with a finalizer.

### Logging

Follow the Kubernetes logging guidelines:

- Start the message with a capital letter and do not end it with a period.
- Use active voice with a past tense for completed actions: `"Could not delete Pod"`.
- Name the object type: `"Deleted Pod"`, not `"Deleted"`.
- Keep key-value pairs balanced.

```go
log := log.FromContext(ctx)
log.Info("Created Namespace", "name", ns.Name)
log.Error(err, "Failed to create LocalQueue", "name", name)
```

Reference: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines

## Distribution

- Container image: `ghcr.io/wutz/workspace`, built by the release workflow on tags.
- Helm chart: `oci://ghcr.io/wutz/charts/workspace`, versioned from `charts/chart/Chart.yaml`.
- Keep `charts/chart/Chart.yaml` `version` and `appVersion` in step with the release tag.

## References

- Kubebuilder Book: https://book.kubebuilder.io
- controller-runtime FAQ: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md
- Good practices: https://book.kubebuilder.io/reference/good-practices.html
- Markers reference: https://book.kubebuilder.io/reference/markers.html
- Kueue: https://kueue.sigs.k8s.io/
