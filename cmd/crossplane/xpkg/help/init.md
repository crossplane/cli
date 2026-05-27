The `xpkg init` command initializes a directory that you can use to build a
package. It uses a template to initialize the directory, and can use any Git
repository as a template.

Specify either a full Git URL or one of the following names as the template:

%s

## `NOTES.txt`

The `init` command prints the contents of any `NOTES.txt` file in the template
root after initializing the directory. Useful for instructions on how to use the
template.

## `init.sh`

The `init` command executes any `init.sh` file in the template root (after user
confirmation). Useful for scripts that personalize the template. Pass `-r`
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
