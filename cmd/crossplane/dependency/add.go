/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dependency

import (
	"cmp"
	"context"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/git"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/add.md
var addHelp string

// addCmd adds a dependency to the current project.
type addCmd struct {
	Package     string `arg:""                      help:"Package to add (xpkg OCI reference, k8s:<version>, git repository URL, HTTP(S) URL, or local path)."`
	ProjectFile string `default:"${project_file}"   help:"Path to project definition file."                                                        short:"f"`
	CacheDir    string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents."                                             name:"cache-dir"`
	GitToken    string `env:"CROSSPLANE_GIT_TOKEN"  help:"Token for git HTTPS authentication."`
	GitUsername string `default:"x-access-token"    env:"CROSSPLANE_GIT_USERNAME"                                                                  help:"Username for git HTTPS authentication."`

	// Flags for specific dependency types.
	APIOnly bool   `help:"Mark an xpkg dependency as API-only (not a runtime dependency)." name:"api-only"`
	GitRef  string `help:"Git ref for CRD dependencies (branch, tag, or commit SHA)."      name:"git-ref"`
	GitPath string `help:"Path to CRDs in the git repository."                             name:"git-path"`
}

func (c *addCmd) Help() string {
	return addHelp
}

// Run executes the add command.
func (c *addCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	ctx := context.Background()

	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	projFS := afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := projectfile.Parse(projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}

	client, err := clixpkg.NewClient(clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(c.CacheDir, dependency.DefaultCacheDir())), clixpkg.WithImageConfigs(proj.Spec.ImageConfigs))
	if err != nil {
		return errors.Wrap(err, "cannot create xpkg client")
	}

	opts := []dependency.ManagerOption{
		dependency.WithProjectFile(c.ProjectFile),
		dependency.WithSchemaGenerators(generator.Filter(
			generator.AllLanguages(
				generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
				generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
			),
			proj.Spec.Schemas.GetLanguages(),
		)),
		dependency.WithXpkgClient(client),
	}

	if c.GitToken != "" {
		opts = append(opts, dependency.WithGitAuthProvider(&git.HTTPSAuthProvider{
			Username: c.GitUsername,
			Password: c.GitToken,
		}))
	}

	m := dependency.NewManager(proj, projFS, opts...)

	dep, err := c.buildDependency(projDirPath)
	if err != nil {
		return err
	}

	desc := dependency.GetSourceDescription(dep)
	logger.Debug("Adding dependency", "dependency", desc)
	return sp.WrapWithSuccessSpinner("Adding "+desc, func() error {
		return m.AddDependency(ctx, &dep)
	})
}

func (c *addCmd) buildDependency(projDir string) (v1alpha1.Dependency, error) {
	// k8s dependency: k8s:vX.Y.Z
	if version, found := strings.CutPrefix(c.Package, "k8s:"); found {
		if version == "" {
			return v1alpha1.Dependency{}, errors.New("k8s version is required (e.g., k8s:v1.33.0)")
		}
		if c.APIOnly {
			return v1alpha1.Dependency{}, errors.New("--api-only is only valid for xpkg dependencies")
		}
		return v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeK8s,
			K8s: &v1alpha1.K8sDependency{
				Version: version,
			},
		}, nil
	}

	// CRD dependency via git ref.
	if c.GitRef != "" {
		if c.Package == "" {
			return v1alpha1.Dependency{}, errors.New("repository URL is required for git-based CRD dependencies")
		}
		if c.APIOnly {
			return v1alpha1.Dependency{}, errors.New("--api-only is only valid for xpkg dependencies")
		}
		return v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeCRD,
			Git: &v1alpha1.GitDependency{
				Repository: c.Package,
				Ref:        c.GitRef,
				Path:       c.GitPath,
			},
		}, nil
	}

	// CRD dependency via HTTP URL.
	if strings.HasPrefix(c.Package, "http://") || strings.HasPrefix(c.Package, "https://") {
		if c.APIOnly {
			return v1alpha1.Dependency{}, errors.New("--api-only is only valid for xpkg dependencies")
		}
		return v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeCRD,
			HTTP: &v1alpha1.HTTPDependency{
				URL: c.Package,
			},
		}, nil
	}

	// CRD dependency from the local filesystem.
	if isLocalRef(c.Package) {
		if c.APIOnly {
			return v1alpha1.Dependency{}, errors.New("--api-only is only valid for xpkg dependencies")
		}
		path, err := localPath(c.Package, projDir)
		if err != nil {
			return v1alpha1.Dependency{}, err
		}
		return v1alpha1.Dependency{
			Type:  v1alpha1.DependencyTypeCRD,
			Local: &v1alpha1.LocalDependency{Path: path},
		}, nil
	}

	// Default: xpkg dependency. We allow three formats:
	//
	// 1. registry.example.com/repo:<tag / semver constraint>
	// 2. registry.example.com/repo@<digest>
	// 3. registry.example.com/repo (implies a semver constraint of '>=v0.0.0').
	pkg := c.Package
	version := ">=v0.0.0"
	if ref, err := name.NewDigest(c.Package, name.StrictValidation); err == nil {
		pkg = ref.Context().String()
		version = ref.DigestStr()
	} else if repo, tag, ok := strings.Cut(c.Package, ":"); ok {
		// NOTE(adamwg): This doesn't work properly if the dependency has a
		// colon in the registry part for a port number (e.g.,
		// example.com:5000/my-repo:v1.2.3). But there's no easy way to handle
		// that correctly in all cases (since we also allow
		// `example.com:5000/my-repo, with no tag/constraint), so leave the
		// corner case unhandled for now.
		pkg = repo
		version = tag
	}

	return v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeXpkg,
		Xpkg: &v1alpha1.XpkgDependency{
			Package: pkg,
			Version: version,
			APIOnly: c.APIOnly,
		},
	}, nil
}

// isLocalRef returns true if ref refers to a path on the local filesystem
// rather than a package. Local paths must start with ./, ../ or file:// since
// anything else could be a valid xpkg reference.
func isLocalRef(ref string) bool {
	for _, prefix := range []string{"./", "../", "file://"} {
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}

// localPath converts a local reference to a slash-separated path relative to
// the project directory. Relative references are resolved against the current
// working directory, as for any other CLI path argument.
func localPath(ref, projDir string) (string, error) {
	path := filepath.FromSlash(strings.TrimPrefix(ref, "file://"))
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.Wrapf(err, "cannot resolve local path %q", ref)
	}
	rel, err := filepath.Rel(projDir, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.Errorf("local path %q must be inside the project directory", ref)
	}
	return filepath.ToSlash(rel), nil
}
