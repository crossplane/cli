The `xrd convert` command converts a CompositeResourceDefinition (XRD) into one
or more CustomResourceDefinitions that Crossplane derives from it internally.

Useful for inspecting the generated CRD shape, feeding it into kubectl-based
tooling that doesn't understand XRDs, or debugging composition behavior.

Output depends on the XRD type, detected automatically:

* Namespaced or Cluster-scoped XRD: 1 CRD for the XR
* Legacy XRD without `claimNames`: 1 CRD for the XR
* Legacy XRD with `claimNames`: 2 CRDs: one for the XR and one for the Claim

## Examples

Convert an XRD file and print the CRDs to stdout (multi-doc YAML for legacy
XRDs):

```shell
crossplane xrd convert xrd.yaml
```

Convert and write to a single file (multi-doc YAML for legacy XRDs):

```shell
crossplane xrd convert xrd.yaml -o crds.yaml
```

Split per-CRD files into a directory (each named `<crd.Name>.yaml`):

```shell
crossplane xrd convert xrd.yaml --output-dir ./crds/
```

Read the XRD from stdin:

```shell
cat xrd.yaml | crossplane xrd convert -
```
