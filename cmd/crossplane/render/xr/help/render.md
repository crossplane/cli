The `composition render` command shows you what resources a Composition would
create or mutate by running the composition locally and printing its results. It
also prints any changes that would be made to the status of the XR. It runs the
Crossplane render engine (either in a Docker container or via a local binary) to
produce high-fidelity output that matches what the real reconciler would
produce.

By default, only the `status` field and `metadata.name` of the XR are
printed. Use `--include-full-xr` (`-x`) to include the full XR `spec` and
`metadata`.

> **Important:** This command runs composition functions and the Crossplane
> render engine using Docker by default, requiring a working Docker
> installation. See the function annotations and `--crossplane-binary` option
> below to understand how to render without Docker.

## Function runtime configuration

Composition Functions are pulled and run using Docker by default. You can add
the following annotations to each Function to change how they're run:

| Annotation | Purpose |
| ---------- | ------- |
| `render.crossplane.io/runtime: "Development"` | Connect to a Function that is already running, instead of using Docker. This is useful to develop and debug new Functions. The Function must be listening at `localhost:9443` and running with the `--insecure` flag. |
| `render.crossplane.io/runtime-development-target: "dns:///example.org:7443"` | Connect to a Function running somewhere other than `localhost:9443`. The target uses gRPC target syntax (e.g., `dns:///example.org:7443` or simply `example.org:7443`). |
| `render.crossplane.io/runtime-docker-cleanup: "Orphan"` | Don't stop the Function's Docker container after rendering. |
| `render.crossplane.io/runtime-docker-name: "<name>"` | Create a container with that name and reuse it as long as it is running or can be restarted. |
| `render.crossplane.io/runtime-docker-pull-policy: "Always"` | Always pull the Function's package, even if it already exists locally. Other supported values are `Never` or `IfNotPresent`. |
| `render.crossplane.io/runtime-docker-publish-address: "0.0.0.0"` | Host address that Docker should publish the Function's container port to. Defaults to `127.0.0.1` (localhost only). Use `0.0.0.0` to publish to all host network interfaces, enabling access from remote machines. |
| `render.crossplane.io/runtime-docker-target: "docker-host"` | Address that the render CLI should use to connect to the Function's Docker container. If not specified, uses the publish address. |

Use the standard `DOCKER_HOST`, `DOCKER_API_VERSION`, `DOCKER_CERT_PATH`, and
`DOCKER_TLS_VERIFY` environment variables to configure how this command connects
to the Docker daemon. See the [Docker environment
variables](https://docs.docker.com/engine/reference/commandline/cli/#environment-variables)
reference.

## Function context

The `--context-files` and `--context-values` flags pass data to each Function's
`context`. The context is JSON-formatted data.

## Function results

If a Function emits events with statuses, use `--include-function-results`
(`-r`) to print them alongside the rendered resources.

## Observed (mock) resources

`--observed-resources` (`-o`) lets you pass mocked managed resources to the
Function pipeline. `render` treats those inputs as if they were resources
observed in a Crossplane cluster, so Functions can reference and manipulate
them.

The argument may be a single YAML file containing multiple resources or a
directory of YAML files. The schema of the mocked resources isn't validated and
may contain any data.

```yaml
apiVersion: example.org/v1alpha1
kind: ComposedResource
metadata:
  name: test-render-b
  annotations:
    crossplane.io/composition-resource-name: resource-b
spec:
  coolerField: "I'm cooler!"
```

## Required (extra) resources

Required resources let a Composition request Crossplane objects on the cluster
that aren't part of the Composition. Pass them with `--required-resources`
(`-e`) — a YAML file or directory of YAML files of resources to mock. Use this
with a Function like
[function-extra-resources](https://github.com/crossplane-contrib/function-extra-resources)
or the built-in support in
[function-go-templating](https://github.com/crossplane-contrib/function-go-templating?tab=readme-ov-file#extraresources).

## Examples

Simulate creating a new XR:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml
```

Simulate updating an XR that already exists:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --observed-resources=existing-observed-resources.yaml
```

Pin the Crossplane version used for rendering:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --crossplane-version=v2.3.0
```

Use a local crossplane binary instead of Docker:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --crossplane-binary=/usr/local/bin/crossplane
```

Pass context values to the Function pipeline:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --context-values=apiextensions.crossplane.io/environment='{"key": "value"}'
```

Pass required resources Functions in the pipeline can request:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --required-resources=required-resources.yaml
```

Pass OpenAPI schemas for Functions that need them:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --required-schemas=schemas/
```

Pass credentials to Functions in the pipeline that need them:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  --function-credentials=credentials.yaml
```

Override function annotations for a remote Docker daemon:

```shell
DOCKER_HOST=tcp://192.168.1.100:2376 crossplane composition render xr.yaml composition.yaml functions.yaml \
  -a render.crossplane.io/runtime-docker-publish-address=0.0.0.0 \
  -a render.crossplane.io/runtime-docker-target=192.168.1.100
```

Force all functions to use development runtime:

```shell
crossplane composition render xr.yaml composition.yaml functions.yaml \
  -a render.crossplane.io/runtime=Development \
  -a render.crossplane.io/runtime-development-target=localhost:9444
```
