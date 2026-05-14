The `xpkg init` command initializes a directory that you can use to build a
package. It uses a template to initialize the directory, and can use any Git
repository as a template.

Specify either a full Git URL or a well-known name as the template. The
following well-known template names are supported:

%s

## NOTES.txt

If the template contains a `NOTES.txt` file in its root, its contents are
printed to stdout after the directory is initialized. Useful for instructions on
how to use the template.

## init.sh

If the template contains an `init.sh` file in its root, you are prompted to view
and/or run it. Useful for scripts that personalize the template. Pass `-r`
(`--run-init-script`) to run the script without prompting.

## Examples

Initialize a new Go Composition Function named function-example:

```shell
crossplane xpkg init function-example function-template-go
```

Initialize a new Provider named provider-example from a custom template:

```shell
crossplane xpkg init provider-example https://github.com/crossplane/provider-template-custom
```

Initialize a new Go Composition Function and run its init.sh script (if any)
without prompting or displaying its contents:

```shell
crossplane xpkg init function-example function-template-go --run-init-script
```
