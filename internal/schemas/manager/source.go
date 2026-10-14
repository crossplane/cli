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

package manager

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/helper/iofs"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/filesystem"
	"github.com/crossplane/cli/v2/internal/git"
)

// SourceType represents the type of source.
type SourceType string

const (
	// SourceTypeCRD indicates a source containing CRDs/XRDs.
	SourceTypeCRD SourceType = "crd"
	// SourceTypeOpenAPI indicates a source containing OpenAPI specifications.
	SourceTypeOpenAPI SourceType = "openapi"
)

// Source is a source of resources for which schemas can be generated.
type Source interface {
	// ID returns a unique identifier for this source.
	ID() string
	// Version returns a revision identifier for this source.
	Version(ctx context.Context) (string, error)
	// Resources returns a filesystem containing resources for which schemas
	// need to be generated.
	Resources(ctx context.Context) (afero.Fs, error)
	// Type returns the type of source.
	Type() SourceType
}

// describer is implemented by sources that have a more readable name for
// messages than their ID. IDs are lock keys and carry a scheme prefix
// (http://https://example.com/crds.yaml), which reads badly in an error.
type describer interface {
	Description() string
}

// describeSource returns the name to use for a source in messages.
func describeSource(src Source) string {
	if d, ok := src.(describer); ok {
		return d.Description()
	}
	return src.ID()
}

// calculateFilesystemHash calculates a SHA256 hash of the filesystem contents.
// A filesystem whose root does not exist hashes as empty: a source whose
// directory has not been created yet (e.g. a Configuration with no apis/)
// contributes no resources, and must not fail the generation pass.
func calculateFilesystemHash(filesystem afero.Fs, sourceType SourceType) (string, error) {
	h := sha256.New()

	if _, err := filesystem.Stat("."); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Sprintf("%x", h.Sum(nil)), nil
		}
		return "", err
	}

	var extensions []string
	switch sourceType {
	case SourceTypeCRD:
		extensions = []string{".yaml", ".yml"}
	case SourceTypeOpenAPI:
		extensions = []string{".json"}
	default:
		extensions = []string{".yaml", ".yml", ".json"}
	}

	if err := afero.Walk(filesystem, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !slices.Contains(extensions, ext) {
			return nil
		}

		f, err := filesystem.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()

		if _, err := io.Copy(h, f); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// fsSource is a resource source backed by a filesystem.
type fsSource struct {
	id      string
	fs      afero.Fs
	exclude []string
}

func (f *fsSource) ID() string {
	return "fs://" + f.id
}

func (f *fsSource) Description() string {
	if p := cleanProjectPath(f.id); p != "" {
		return p + "/"
	}
	return "the project root"
}

// Version hashes exactly the files Resources returns, so a change anywhere
// else under the source's root -- a function's node_modules, the generated
// schemas -- never reads as an API change.
func (f *fsSource) Version(ctx context.Context) (string, error) {
	res, err := f.Resources(ctx)
	if err != nil {
		return "", err
	}
	return calculateFilesystemHash(res, SourceTypeCRD)
}

// Resources returns the source's YAML files. Excluded paths and hidden
// directories are skipped: a project whose APIs live at its root ("/") would
// otherwise hand the generators every file it has, including function
// dependencies and the schemas directory's own caches.
func (f *fsSource) Resources(_ context.Context) (afero.Fs, error) {
	out := afero.NewMemMapFs()

	// A source whose directory does not exist yet (a project that declares
	// apis/ but has not created it) contributes no resources.
	if _, err := f.fs.Stat("."); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}

	err := afero.Walk(f.fs, ".", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), "/")
		if info.IsDir() {
			if rel != "." && rel != "" && (strings.HasPrefix(info.Name(), ".") || f.excluded(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		bs, err := afero.ReadFile(f.fs, p)
		if err != nil {
			return errors.Wrapf(err, "failed to read %s", p)
		}
		if err := out.MkdirAll(path.Dir(rel), 0o755); err != nil {
			return err
		}
		return afero.WriteFile(out, rel, bs, 0o644)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// excluded reports whether a slash-separated path relative to the source's
// root is, or is inside, one of the excluded paths.
func (f *fsSource) excluded(rel string) bool {
	for _, ex := range f.exclude {
		if rel == ex || strings.HasPrefix(rel, ex+"/") {
			return true
		}
	}
	return false
}

func (f *fsSource) Type() SourceType {
	return SourceTypeCRD
}

// FSSourceOption configures a filesystem source.
type FSSourceOption func(*fsSource)

// WithExcludedPaths skips the given paths, relative to the source's root, and
// everything under them.
func WithExcludedPaths(paths ...string) FSSourceOption {
	return func(f *fsSource) {
		for _, p := range paths {
			p = strings.Trim(path.Clean(filepath.ToSlash(p)), "/")
			if p != "" && p != "." {
				f.exclude = append(f.exclude, p)
			}
		}
	}
}

// NewFSSource returns a new filesystem-backed resource source. The id should
// be a stable, location-independent identifier (e.g. a project-relative path)
// since it is persisted in the schema manager's lockfile.
func NewFSSource(id string, fs afero.Fs, opts ...FSSourceOption) Source {
	f := &fsSource{id: id, fs: fs}
	for _, o := range opts {
		o(f)
	}
	return f
}

// NewProjectAPIsSource returns the source for a project's own APIs, read from
// paths.apis under projFS. The APIs path may contain the project's other
// directories -- most often with `apis: .`, which makes it the project root.
// Functions, examples, operations, tests and the generated schemas are then
// excluded, so the source reads only the project's own API definitions.
func NewProjectAPIsSource(projFS afero.Fs, paths *v1alpha1.ProjectPaths) Source {
	p := paths
	if p == nil {
		p = &v1alpha1.ProjectPaths{}
		p.Default()
	}

	apis := cleanProjectPath(p.APIs)
	fsys := projFS
	if apis != "" {
		fsys = afero.NewBasePathFs(projFS, apis)
	}

	var exclude []string
	for _, other := range []string{p.Functions, p.Examples, p.Operations, p.Tests, p.Schemas} {
		o := cleanProjectPath(other)
		switch {
		case o == "" || o == apis:
			// Unset, or the same directory as the APIs: nothing to exclude.
		case apis == "":
			exclude = append(exclude, o)
		case strings.HasPrefix(o, apis+"/"):
			exclude = append(exclude, strings.TrimPrefix(o, apis+"/"))
		}
	}

	return NewFSSource(p.APIs, fsys, WithExcludedPaths(exclude...))
}

// cleanProjectPath normalizes a project-relative path to slash-separated form
// with no leading or trailing slash. The project root is "".
func cleanProjectPath(p string) string {
	return strings.Trim(path.Clean("/"+filepath.ToSlash(p)), "/")
}

// xpkgSource is a source backed by extracted CRDs from an xpkg. Unlike the
// up CLI, this always generates client-side (never uses pre-packaged schemas).
type xpkgSource struct {
	id      string
	version string
	crdFS   afero.Fs
}

func (s *xpkgSource) ID() string {
	return "xpkg://" + s.id
}

func (s *xpkgSource) Description() string {
	return s.id
}

func (s *xpkgSource) Version(_ context.Context) (string, error) {
	return s.version, nil
}

func (s *xpkgSource) Resources(_ context.Context) (afero.Fs, error) {
	return s.crdFS, nil
}

func (s *xpkgSource) Type() SourceType {
	return SourceTypeCRD
}

// NewXpkgSource returns a new xpkg-backed resource source. The crdFS should
// contain extracted CRD YAML files from the package.
func NewXpkgSource(id, version string, crdFS afero.Fs) Source {
	return &xpkgSource{
		id:      id,
		version: version,
		crdFS:   crdFS,
	}
}

const maxCloneAttempts = 3

// gitSource is a resource source that fetches directly from git repositories.
type gitSource struct {
	gitRef       *v1alpha1.GitDependency
	cloner       git.Cloner
	authProvider git.AuthProvider
	sourceType   SourceType
	fs           afero.Fs
	commitSHA    string
}

func (g *gitSource) ID() string {
	id := fmt.Sprintf("git://%s", g.gitRef.Repository)
	if g.gitRef.Path != "" {
		id = fmt.Sprintf("%s/%s", id, g.gitRef.Path)
	}
	return id
}

func (g *gitSource) Description() string {
	if g.gitRef.Path != "" {
		return g.gitRef.Repository + " at " + g.gitRef.Path
	}
	return g.gitRef.Repository
}

func (g *gitSource) Version(ctx context.Context) (string, error) {
	if g.commitSHA != "" {
		return g.commitSHA, nil
	}

	if _, err := g.Resources(ctx); err != nil {
		return "", err
	}
	return g.commitSHA, nil
}

func (g *gitSource) Resources(_ context.Context) (afero.Fs, error) {
	if g.fs != nil {
		return g.fs, nil
	}

	ref := g.normalizeRef(g.gitRef.Ref)

	var memFS billy.Filesystem
	var lastErr error

	for attempt := 1; attempt <= maxCloneAttempts; attempt++ {
		memFS = memfs.New()

		headRef, err := g.cloner.CloneRepository(
			memory.NewStorage(),
			memFS,
			g.authProvider,
			git.CloneOptions{
				Repo:    g.gitRef.Repository,
				RefName: ref,
				Path:    g.gitRef.Path,
			},
		)
		if err != nil {
			lastErr = errors.Wrapf(err, "clone attempt %d failed", attempt)
			continue
		}

		if headRef != nil {
			g.commitSHA = headRef.Hash().String()
		}

		if err := g.verifyClone(memFS, g.gitRef.Path); err != nil {
			lastErr = errors.Wrapf(err, "verification failed after attempt %d", attempt)
			continue
		}

		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, errors.Wrapf(lastErr, "failed to clone repository %s after %d attempts", g.gitRef.Repository, maxCloneAttempts)
	}

	resultFS := afero.NewMemMapFs()

	if err := filesystem.CopyFilesBetweenFs(afero.FromIOFS{FS: iofs.New(memFS)}, resultFS); err != nil {
		return nil, errors.Wrap(err, "failed to copy files from git repository")
	}

	g.fs = resultFS
	return g.fs, nil
}

func (g *gitSource) Type() SourceType {
	return g.sourceType
}

func (g *gitSource) normalizeRef(ref string) string {
	if ref == "" {
		return "refs/heads/main"
	}

	if git.CheckSHA1Hash(ref) {
		return ref
	}

	if len(ref) > 5 && ref[:5] == "refs/" {
		return ref
	}

	if _, err := semver.NewVersion(ref); err == nil {
		return "refs/tags/" + ref
	}

	return "refs/heads/" + ref
}

func (g *gitSource) verifyClone(fs billy.Filesystem, path string) error {
	files, err := fs.ReadDir(path)
	if err != nil {
		return errors.Wrapf(err, "cannot read cloned path %s", path)
	}

	if len(files) == 0 {
		return errors.Errorf("no files found in cloned path %s", path)
	}

	return nil
}

// NewGitSource returns a new git-backed resource source.
func NewGitSource(dep v1alpha1.Dependency, cloner git.Cloner, authProvider git.AuthProvider) Source {
	sourceType := SourceTypeCRD
	if dep.Type == v1alpha1.DependencyTypeK8s {
		sourceType = SourceTypeOpenAPI
	}

	return &gitSource{
		gitRef:       dep.Git,
		cloner:       cloner,
		authProvider: authProvider,
		sourceType:   sourceType,
	}
}

const (
	defaultHTTPTimeout = 1 * time.Minute
	maxHTTPSize        = 100 * 1024 * 1024
)

// httpSource is a resource source that fetches from HTTP/HTTPS URLs.
type httpSource struct {
	httpRef    *v1alpha1.HTTPDependency
	client     *http.Client
	sourceType SourceType
	fs         afero.Fs
}

func (h *httpSource) ID() string {
	return fmt.Sprintf("http://%s", h.httpRef.URL)
}

func (h *httpSource) Description() string {
	return h.httpRef.URL
}

func (h *httpSource) Version(ctx context.Context) (string, error) {
	if h.fs != nil {
		return h.calculateHash()
	}

	if _, err := h.Resources(ctx); err != nil {
		return "", err
	}
	return h.calculateHash()
}

func (h *httpSource) Resources(ctx context.Context) (afero.Fs, error) {
	if h.fs != nil {
		return h.fs, nil
	}

	u, err := url.Parse(h.httpRef.URL)
	if err != nil {
		return nil, errors.Wrap(err, "invalid URL")
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.Errorf("unsupported URL scheme: %s", u.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.httpRef.URL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch URL")
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do here

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if resp.ContentLength > maxHTTPSize {
		return nil, errors.Errorf("content too large: %d bytes (max: %d)", resp.ContentLength, maxHTTPSize)
	}

	limitedReader := io.LimitReader(resp.Body, maxHTTPSize)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read response body")
	}

	resultFS := afero.NewMemMapFs()
	filename := h.getFilename(u)

	if err := afero.WriteFile(resultFS, filename, content, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write content to filesystem")
	}

	h.fs = resultFS
	return h.fs, nil
}

func (h *httpSource) Type() SourceType {
	return h.sourceType
}

func (h *httpSource) calculateHash() (string, error) {
	return calculateFilesystemHash(h.fs, h.sourceType)
}

func (h *httpSource) getFilename(u *url.URL) string {
	filename := path.Base(u.Path)

	if filename == "" || filename == "." || filename == "/" {
		switch {
		case strings.Contains(u.String(), "yaml") || strings.Contains(u.String(), "yml"):
			filename = "crd.yaml"
		case h.sourceType == SourceTypeOpenAPI:
			filename = "openapi.json"
		default:
			filename = "crd"
		}
	}

	return filename
}

// NewHTTPSource returns a new HTTP-backed resource source.
func NewHTTPSource(dep v1alpha1.Dependency) Source {
	sourceType := SourceTypeCRD
	if dep.Type == v1alpha1.DependencyTypeK8s {
		sourceType = SourceTypeOpenAPI
	}

	return &httpSource{
		httpRef: dep.HTTP,
		client: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		sourceType: sourceType,
	}
}

// k8sOpenAPISource is a source that generates OpenAPI schemas for Kubernetes
// built-in APIs by downloading the swagger spec.
type k8sOpenAPISource struct {
	k8sRef *v1alpha1.K8sDependency
	client *http.Client
	fs     afero.Fs
}

func (k *k8sOpenAPISource) ID() string {
	return fmt.Sprintf("k8s://%s", k.k8sRef.Version)
}

func (k *k8sOpenAPISource) Description() string {
	return "Kubernetes API " + k.k8sRef.Version
}

func (k *k8sOpenAPISource) Version(_ context.Context) (string, error) {
	return k.k8sRef.Version, nil
}

func (k *k8sOpenAPISource) Resources(ctx context.Context) (afero.Fs, error) {
	if k.fs != nil {
		return k.fs, nil
	}

	// Download the OpenAPI v3 spec from the Kubernetes repo
	specURL := fmt.Sprintf("https://raw.githubusercontent.com/kubernetes/kubernetes/v%s/api/openapi-spec/v3/api__v1_openapi.json", strings.TrimPrefix(k.k8sRef.Version, "v"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch Kubernetes OpenAPI spec")
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.Errorf("no Kubernetes OpenAPI spec found for version %s (HTTP 404 from %s); check that this Kubernetes version exists", k.k8sRef.Version, specURL)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to download the Kubernetes OpenAPI spec for version %s: HTTP %d from %s", k.k8sRef.Version, resp.StatusCode, specURL)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPSize))
	if err != nil {
		return nil, errors.Wrap(err, "failed to read Kubernetes OpenAPI spec")
	}

	resultFS := afero.NewMemMapFs()
	if err := afero.WriteFile(resultFS, "api__v1_openapi.json", content, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write OpenAPI spec")
	}

	// Also try to download the apis specs
	apisGroups := []string{
		"apis__apps__v1",
		"apis__autoscaling__v1",
		"apis__batch__v1",
		"apis__networking.k8s.io__v1",
		"apis__policy__v1",
		"apis__rbac.authorization.k8s.io__v1",
		"apis__storage.k8s.io__v1",
	}

	for _, group := range apisGroups {
		groupURL := fmt.Sprintf("https://raw.githubusercontent.com/kubernetes/kubernetes/v%s/api/openapi-spec/v3/%s_openapi.json", strings.TrimPrefix(k.k8sRef.Version, "v"), group)
		groupReq, err := http.NewRequestWithContext(ctx, http.MethodGet, groupURL, nil)
		if err != nil {
			continue
		}

		groupResp, err := k.client.Do(groupReq)
		if err != nil {
			continue
		}

		if groupResp.StatusCode == http.StatusOK {
			groupContent, err := io.ReadAll(io.LimitReader(groupResp.Body, maxHTTPSize))
			if err == nil {
				_ = afero.WriteFile(resultFS, group+"_openapi.json", groupContent, 0o644)
			}
		}
		_ = groupResp.Body.Close()
	}

	k.fs = resultFS
	return k.fs, nil
}

func (k *k8sOpenAPISource) Type() SourceType {
	return SourceTypeOpenAPI
}

// NewK8sSource returns a source for Kubernetes built-in APIs.
func NewK8sSource(dep v1alpha1.Dependency) Source {
	return &k8sOpenAPISource{
		k8sRef: dep.K8s,
		client: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}
}
