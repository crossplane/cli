The `project init` command scaffolds a new, empty Crossplane Project. It
creates a target directory containing a minimal `crossplane-project.yaml` along
with the standard sub-directories used by the DevEx tooling: `apis`,
`functions`, `examples`, `tests`, and `operations`.

The project name must be a valid DNS-1035 label. By default, the `init` command
creates a new directory named after the project; use `--directory` (`-d`) to
choose a different target directory.

## Examples

Create a new project named `my-project` in `./my-project/`:

```shell
crossplane project init my-project
```

Create a new project in a specific directory:

```shell
crossplane project init my-project --directory ./projects/new-project
```
