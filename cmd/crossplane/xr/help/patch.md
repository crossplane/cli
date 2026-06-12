The `xr patch` command applies XR-level patches to a Composite Resource (XR).

It reads the XR from a file (or stdin), applies the requested patches, and
writes the result to stdout or to a file. Pass at least one patching flag;
today the only one is `--xrd`, which applies default values from an XRD's
`openAPIV3Schema` to the XR. Future releases add more patching flags.

## Examples

Apply default values from an XRD to an XR:

```shell
crossplane xr patch xr.yaml --xrd xrd.yaml
```

Patch an XR from stdin:

```shell
cat xr.yaml | crossplane xr patch - --xrd xrd.yaml
```

Write the patched XR to a file:

```shell
crossplane xr patch xr.yaml --xrd xrd.yaml -o patched.yaml
```
