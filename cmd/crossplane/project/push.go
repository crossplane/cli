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

package project

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
)

// pushCmd pushes a built project to an OCI registry.
type pushCmd struct {
	ProjectFile           string `default:"crossplane-project.yaml"                                          help:"Path to project definition."                                                  short:"f"`
	Repository            string `help:"Override the repository in the project file."                        optional:""`
	Tag                   string `default:""                                                                 help:"Tag for the pushed package. If not provided, a semver tag will be generated." short:"t"`
	PackageFile           string `help:"Package file to push. Defaults to <output-dir>/<project-name>.xpkg." optional:""`
	OutputDir             string `default:"_output"                                                          help:"Directory containing built packages."                                         short:"o"`
	MaxConcurrency        uint   `default:"8"                                                                help:"Max concurrent function pushes."`
	InsecureSkipTLSVerify bool   `help:"[INSECURE] Skip verifying TLS certificates."`

	proj      *devv1alpha1.Project
	projFS    afero.Fs
	packageFS afero.Fs
	transport http.RoundTripper
}

// AfterApply parses flags, reads the project file, and prepares the push
// runtime (filesystem views and HTTP transport).
func (c *pushCmd) AfterApply() error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	projFileName := filepath.Base(c.ProjectFile)
	prj, err := projectfile.Parse(c.projFS, projFileName)
	if err != nil {
		return errors.Wrapf(err, "failed to parse project file %q", c.ProjectFile)
	}
	c.proj = prj

	// If a package file was provided, treat it as an OS path. Otherwise read
	// from <project-root>/<output-dir>, which is where `crossplane project
	// build` writes packages.
	if c.PackageFile == "" {
		c.packageFS = afero.NewBasePathFs(c.projFS, c.OutputDir)
	} else {
		c.packageFS = afero.NewOsFs()
	}

	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // http.DefaultTransport is always *http.Transport
	if c.InsecureSkipTLSVerify {
		t.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // we need to support insecure connections if requested
		}
	}
	c.transport = t

	return nil
}

// Run executes the push command.
func (c *pushCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter) error {
	ctx := context.Background()

	if c.Repository != "" {
		ref, err := name.NewRepository(c.Repository)
		if err != nil {
			return errors.Wrap(err, "failed to parse repository")
		}
		c.proj.Spec.Repository = ref.String()
	}
	if c.PackageFile == "" {
		c.PackageFile = fmt.Sprintf("%s.xpkg", c.proj.Name)
	}

	var imgMap project.ImageTagMap
	if err := sp.WrapWithSuccessSpinner(
		fmt.Sprintf("Loading packages from %s", c.PackageFile),
		func() error {
			var err error
			imgMap, err = c.loadPackages()
			return err
		},
	); err != nil {
		return err
	}

	pusher := project.NewPusher(
		project.PushWithTransport(c.transport),
		project.PushWithAuthKeychain(authn.DefaultKeychain),
		project.PushWithMaxConcurrency(max(1, c.MaxConcurrency)),
	)

	var pushedTag name.Tag
	if err := sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		opts := []project.PushOption{project.PushWithEventChannel(ch)}
		if c.Tag != "" {
			opts = append(opts, project.PushWithTag(c.Tag))
		}

		var perr error
		pushedTag, perr = pusher.Push(ctx, c.proj, imgMap, opts...)
		return perr
	}); err != nil {
		return err
	}

	logger.Debug("Push complete", "tag", pushedTag.String())
	fmt.Printf("Pushed project %q to %s\n", c.proj.Name, pushedTag) //nolint:forbidigo // CLI output.

	return nil
}

func (c *pushCmd) loadPackages() (project.ImageTagMap, error) {
	opener := func() (io.ReadCloser, error) {
		return c.packageFS.Open(c.PackageFile)
	}
	mfst, err := tarball.LoadManifest(opener)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read package file manifest")
	}

	imgMap := make(project.ImageTagMap)
	for _, desc := range mfst {
		if len(desc.RepoTags) == 0 {
			// Ignore images with no tags; we shouldn't find these in xpkg
			// files, but best not to panic if it happens.
			continue
		}

		tag, err := name.NewTag(desc.RepoTags[0])
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse image tag %q", desc.RepoTags[0])
		}
		image, err := tarball.Image(opener, &tag)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to load image %q from package", tag)
		}
		imgMap[tag] = image
	}

	return imgMap, nil
}
