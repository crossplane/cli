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

// Package runner contains functions for handling containers for schema
// generation.
package runner

import (
	"context"
	"os"
	"strings"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/filesystem"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// SchemaRunner defines an interface for schema generation.
type SchemaRunner interface {
	Generate(ctx context.Context, fs afero.Fs, folder string, basePath string, imageName string, args []string, options ...Option) error
}

// RealSchemaRunner implements the SchemaRunner interface.
type RealSchemaRunner struct {
	configStore xpkg.ConfigStore
	envPrefixes []string
}

// NewRealSchemaRunner creates a new RealSchemaRunner.
func NewRealSchemaRunner(opts ...ROption) *RealSchemaRunner {
	r := &RealSchemaRunner{}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ROption configures the SchemaRunner.
type ROption func(*RealSchemaRunner)

// WithEnvPrefixes forwards environment variables with the given prefixes into
// the tooling container, in addition to the default CROSSPLANE_ prefix. Host
// CLIs use this to make their own variables visible to language tooling
// (e.g. tests reading credentials from the environment).
func WithEnvPrefixes(prefixes ...string) ROption {
	return func(r *RealSchemaRunner) {
		r.envPrefixes = append(r.envPrefixes, prefixes...)
	}
}

// WithImageConfig adds image rewriting rules to the SchemaRunner.
func WithImageConfig(cfgs []pkgv1beta1.ImageConfig) ROption {
	return func(r *RealSchemaRunner) {
		r.configStore = clixpkg.NewStaticImageConfigStore(cfgs)
	}
}

// GenerateOptions holds optional parameters for Generate.
type GenerateOptions struct {
	CopyToPath    string
	CopyFromPath  string
	WorkDirectory string

	// VolumeMounts maps named Docker volumes to container paths.
	VolumeMounts map[string]string
}

// Option is a function that modifies GenerateOptions.
type Option func(*GenerateOptions)

// WithCopyToPath sets the CopyToPath option.
func WithCopyToPath(path string) Option {
	return func(o *GenerateOptions) {
		o.CopyToPath = path
	}
}

// WithCopyFromPath sets the CopyFromPath option.
func WithCopyFromPath(path string) Option {
	return func(o *GenerateOptions) {
		o.CopyFromPath = path
	}
}

// WithWorkDirectory sets the WorkDirectory option.
func WithWorkDirectory(dir string) Option {
	return func(o *GenerateOptions) {
		o.WorkDirectory = dir
	}
}

// WithVolumeMount mounts a named Docker volume at the given path, e.g. to
// give the container a cache that outlives it. Mounts are keyed by container
// path, so one volume can be mounted at several paths; a second mount at the
// same path replaces the first.
func WithVolumeMount(name, containerPath string) Option {
	return func(o *GenerateOptions) {
		if o.VolumeMounts == nil {
			o.VolumeMounts = make(map[string]string)
		}
		o.VolumeMounts[containerPath] = name
	}
}

// DefaultGenerateOptions provides default values.
func DefaultGenerateOptions() GenerateOptions {
	return GenerateOptions{
		CopyToPath:    "/data/input",
		CopyFromPath:  "/data/input",
		WorkDirectory: "/data/input",
	}
}

// Generate runs the containerized language tool for schema generation.
func (r RealSchemaRunner) Generate(ctx context.Context, fromFS afero.Fs, baseFolder, basePath, imageName string, command []string, options ...Option) error {
	if err := docker.Check(ctx); err != nil {
		return errors.Wrap(err, "failed to connect to Docker; schema generation requires a Docker-compatible container runtime")
	}

	_, rewritten, err := r.configStore.RewritePath(ctx, imageName)
	if err != nil {
		return errors.Wrap(err, "failed to rewrite image ref")
	}
	if rewritten != "" {
		imageName = rewritten
	}

	o := DefaultGenerateOptions()
	for _, opt := range options {
		opt(&o)
	}

	var opts []filesystem.FSToTarOption
	if basePath != "" {
		opts = append(opts, filesystem.WithSymlinkBasePath(basePath))
	}
	tarBuffer, err := filesystem.FSToTar(fromFS, baseFolder, opts...)
	if err != nil {
		return errors.Wrapf(err, "failed to create tar from fs")
	}

	prefixes := append([]string{"CROSSPLANE_"}, r.envPrefixes...)
	var envVars []string
	for _, e := range os.Environ() {
		for _, prefix := range prefixes {
			// An empty prefix would forward the entire host environment.
			if prefix == "" {
				continue
			}
			if strings.HasPrefix(e, prefix) {
				envVars = append(envVars, e)
				break
			}
		}
	}

	startOpts := []docker.StartContainerOption{
		docker.StartWithCopyFiles(tarBuffer, o.CopyToPath),
		docker.StartWithCommand(command),
		docker.StartWithEnv(envVars...),
		docker.StartWithWorkingDirectory(o.WorkDirectory),
	}
	for path, name := range o.VolumeMounts {
		startOpts = append(startOpts, docker.StartWithVolumeMount(name, path))
	}

	cid, err := docker.StartContainer(ctx, "", imageName, startOpts...)
	if err != nil {
		return err
	}

	defer func() {
		_ = docker.StopContainerByID(ctx, cid)
	}()

	if err := docker.WaitForContainerByID(ctx, cid); err != nil {
		return err
	}

	return errors.Wrapf(
		docker.CopyFromContainer(ctx, cid, o.CopyFromPath, fromFS),
		"failed to copy tar from container",
	)
}
