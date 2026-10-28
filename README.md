# workspace

A Kubernetes operator that turns a `Project` custom resource into an isolated
workspace: a namespace with a GPU/CPU/memory quota, a Kueue queue, storage and
pod limits, a network policy, and per-member access.

The operator runs three controllers. The Workspace controller reconciles the
`Project` API (`workspace.wutz.dev/v1alpha1`). The InstanceType controller derives one
`InstanceType` per GPU class and size from the GPU nodes and keeps its availability up to
date. The registry controller gives each Project a Harbor project when the platform config
enables a registry.

## What a Project creates

For each `Project` the operator reconciles:

- A workspace namespace. The name prefix comes from the platform config.
- A `ResourceQuota` derived from `spec.quota`. GPU quota is per GPU class; CPU and
  memory follow from the per-GPU slice of each `InstanceType`.
- Kueue ClusterQueue and LocalQueue objects, only when the Kueue API is installed (see `--enable-kueue`).
- A `NetworkPolicy` according to `spec.network`. `Isolated` admits traffic only from
  the workspace and platform namespaces; `denyInternet` also blocks egress outside the workspace.
- For each entry in `spec.members`, a ServiceAccount in the platform members namespace
  and a long-lived token Secret. The member's `edit` or `view` role is bound in the workspace namespace.
- When a registry is configured, a Harbor project named after the Project.

`spec.deletionPolicy` decides whether the namespace (and its volumes) and the registry
project are removed (`Delete`) or only access is revoked (`Retain`, the default).

A minimal example, from [config/samples/workspace_v1alpha1_workspace.yaml](config/samples/workspace_v1alpha1_workspace.yaml):

```yaml
apiVersion: workspace.wutz.dev/v1alpha1
kind: Project
metadata:
  name: demo
spec:
  displayName: Demo project
  quota:
    gpus:
    - class: rtx-5090
      count: 8
    storage: 1Ti
    volumes: 10
    registryStorage: 200Gi
  network:
    isolation: Isolated
    denyInternet: false
  members:
  - name: alice
    role: edit
  - name: bob
    role: view
  trashRetention: 168h
  deletionPolicy: Retain
```

## Requirements

- Kubernetes 1.37 (the version the envtest suite runs against).
- [Kueue](https://kueue.sigs.k8s.io/) with the `kueue.x-k8s.io/v1beta2` API. Optional: without it the
  operator keeps no queues or ResourceFlavors, and the ResourceQuota alone limits a workspace.
- GPU nodes labelled with `nvidia.com/gpu.product`. The operator derives the `InstanceType`
  objects (`instancetypes.workspace.wutz.dev`) from them and uses them to size GPU quotas.
- A platform config ConfigMap named `workspace-platform` with key `config.yaml`, in the
  namespace the operator runs in (`PROJECT_NAMESPACE`, which the chart sets to the release
  namespace). The platform that installs the operator creates it. It holds the namespace
  prefix, registry, instance type sizing, default quotas, network and ingress settings.
  The operator reloads it when it changes.

## Install

The chart is published as an OCI artifact and the image to GHCR. Both are public.

```bash
helm install workspace oci://ghcr.io/wutz/charts/workspace \
  --version 0.5.2 \
  --namespace workspace-system
```

The operator reads the platform ConfigMap from its own namespace, so the platform config
must be created in the namespace the chart is installed into.

Override values with `--set` or a values file. Common settings:

| Key | Default | Description |
|---|---|---|
| `manager.image.repository` | `ghcr.io/wutz/workspace` | Controller image |
| `manager.image.tag` | `Chart.appVersion` | Controller image tag |
| `manager.replicas` | `1` | Controller replicas (leader election is enabled) |
| `metrics.enabled` | `true` | Serve Prometheus metrics |

See [charts/chart/values.yaml](charts/chart/values.yaml) for the full list.

## Development

Requirements: Go (see `go.mod`), Docker or a compatible builder, `kubectl`, and `helm`
for chart checks. Tools such as `controller-gen`, `kustomize` and `golangci-lint` are
downloaded into `bin/` by the Makefile.

```bash
make manifests generate   # regenerate CRDs, RBAC and DeepCopy code after editing *_types.go
make lint                 # golangci-lint
make test                 # unit and controller tests (envtest: real API server and etcd)
make run                  # run the controller against the current kubeconfig context
make helm-lint            # lint and template the chart
```

Generated files (`config/crd/bases/*`, `config/rbac/role.yaml`, `**/zz_generated.*.go`)
must not be edited by hand. Run the generator instead.

The e2e suite (`test/e2e`) builds the image, deploys it and checks the result. It
expects a dedicated [Kind](https://kind.sigs.k8s.io/) cluster and does not run
against a real environment.

```bash
make test-e2e
```

## Releases

Pushing a `v*` tag runs `.github/workflows/helm-release.yml`. It publishes the
container image to `ghcr.io/wutz/workspace` and the chart to
`oci://ghcr.io/wutz/charts/workspace`, both tagged with the release version.

## License

Apache License 2.0. See [LICENSE](LICENSE).
