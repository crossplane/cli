The render takes the same render engine flags as `crossplane render`:
`--crossplane-version` and `--crossplane-image`, v2.3.4 or later, or
`--crossplane-binary`, pick what renders, and `--crossplane-docker-network`
names the Docker network the render and function containers join. The network
comes from `--crossplane-docker-network`, then the `CROSSPLANE_DOCKER_NETWORK`
environment variable, then, when simulating a project, the first project
function with a `render.crossplane.io/runtime-docker-network` annotation. The
annotation fallback reads the project's functions only. `--crossplane-binary`
runs no render container and ignores the network; passing
`--crossplane-docker-network` with it prints a warning.
