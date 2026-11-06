`--image-configs` takes a YAML file of ImageConfig manifests that rewrite
package paths for pulls, for both the render and the provider plans: the images
of the functions the render runs, except a project's embedded functions,
provider images, and a project's function and provider dependencies. When
simulating a project, it replaces the project's imageConfigs for those pulls
rather than adding to them; schema generation images still use the project's
imageConfigs, and without the flag the project's imageConfigs apply.
