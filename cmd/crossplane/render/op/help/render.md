The `operation render` command shows you what resources an Operation would
create or mutate by running the operation locally and printing its results. It
runs the Crossplane render engine (either in a Docker container or via a local
binary) to produce high-fidelity output that matches what the real reconciler
would produce.

> **Important:** This command runs operation functions and the Crossplane render
> engine using Docker by default, requiring a working Docker installation. See
> the function annotations and `--crossplane-binary` option below to understand
> how to render without Docker.

## Function runtime configuration

Operation Functions are pulled and run using Docker by default. You can add the
following annotations to each Function to change how they're run:

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

## Examples

Render an Operation:

```shell
crossplane operation render operation.yaml functions.yaml
```

Pin the Crossplane version used for rendering:

```shell
crossplane operation render operation.yaml functions.yaml \
  --crossplane-version=v2.2.1
```

Use a local crossplane binary instead of Docker:

```shell
crossplane operation render operation.yaml functions.yaml \
  --crossplane-binary=/usr/local/bin/crossplane
```

Pass context values to the function pipeline:

```shell
crossplane operation render operation.yaml functions.yaml \
  --context-values=apiextensions.crossplane.io/environment='{"key": "value"}'
```

Pass required resources functions can request:

```shell
crossplane operation render operation.yaml functions.yaml \
  --required-resources=required-resources.yaml
```

Pass OpenAPI schemas for functions that need them:

```shell
crossplane operation render operation.yaml functions.yaml \
  --required-schemas=schemas/
```

Render a WatchOperation with a watched resource:

```shell
crossplane operation render watchoperation.yaml functions.yaml \
  --watched-resource=watched-configmap.yaml
```

Pass credentials to functions that need them:

```shell
crossplane operation render operation.yaml functions.yaml \
  --function-credentials=credentials.yaml
```

Include function results and context in output:

```shell
crossplane operation render operation.yaml functions.yaml -r -c
```

Include the full Operation with original spec and metadata:

```shell
crossplane operation render operation.yaml functions.yaml -o
```

Override function annotations for remote Docker daemon:

```shell
crossplane operation render operation.yaml functions.yaml \
  -a render.crossplane.io/runtime-docker-publish-address=0.0.0.0 \
  -a render.crossplane.io/runtime-docker-target=192.168.1.100
```

Use development runtime with custom target for all functions:

```shell
crossplane operation render operation.yaml functions.yaml \
  -a render.crossplane.io/runtime=Development \
  -a render.crossplane.io/runtime-development-target=localhost:9444
```
