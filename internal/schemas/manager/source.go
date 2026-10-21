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
	"bytes"
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
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/helper/iofs"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

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
	// Key returns a stable identifier for this source. It never encodes a
	// version, so a new revision of a source replaces its lock entry instead of
	// orphaning it.
	Key() string
	// Hash returns the authoritative content hash of this source. It is what
	// gets recorded in the lock as the source hash.
	Hash(ctx context.Context) (string, error)
	// Resources returns a filesystem containing resources for which schemas
	// need to be generated.
	Resources(ctx context.Context) (afero.Fs, error)
	// Type returns the type of source.
	Type() SourceType
}

// Pinned is implemented by sources that can be fetched again by an immutable
// reference. The manager records it in the lock when it adds the source.
type Pinned interface {
	// Pin returns the immutable reference of the source's content, for example
	// a digest or a commit.
	Pin(ctx context.Context) (string, error)
	// Version returns the human-meaningful version the pin was resolved from,
	// such as a tag or ref. It may be empty.
	Version() string
}

// calculateFilesystemHash calculates a SHA256 hash of the filesystem contents.
func calculateFilesystemHash(filesystem afero.Fs, sourceType SourceType) (string, error) {
	h := sha256.New()

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

// fsKeyPrefix prefixes the keys of filesystem sources, which are the project's
// own local APIs.
const fsKeyPrefix = "fs://"

// isLocalKey returns true if the key belongs to a local source. It is for lock
// entries, whose sources may not be at hand.
func isLocalKey(key string) bool {
	return strings.HasPrefix(key, fsKeyPrefix)
}

// isDefinitionKind returns true for the kinds of the documents that define a
// local API.
func isDefinitionKind(kind string) bool {
	return kind == "CompositeResourceDefinition" || kind == "CustomResourceDefinition"
}

// fsDocument is one API definition document of a filesystem source.
type fsDocument struct {
	path string
	doc  []byte
}

// fsSource is a resource source backed by a filesystem. Only its XRD and CRD
// documents count: they are what Resources returns and what Hash covers, so
// the hash and the generator input always agree.
type fsSource struct {
	id string
	fs afero.Fs

	once sync.Once
	docs []fsDocument
	err  error
}

func (f *fsSource) Key() string {
	return fsKeyPrefix + f.id
}

func (f *fsSource) Hash(_ context.Context) (string, error) {
	docs, err := f.definitions()
	if err != nil {
		return "", err
	}

	// Paths are part of the hash so renaming a file or splitting it into two
	// changes it.
	h := sha256.New()
	for _, d := range docs {
		_, _ = h.Write([]byte(d.path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(d.doc)
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func (f *fsSource) Resources(_ context.Context) (afero.Fs, error) {
	docs, err := f.definitions()
	if err != nil {
		return nil, err
	}

	byPath := make(map[string][][]byte)
	var paths []string
	for _, d := range docs {
		if _, ok := byPath[d.path]; !ok {
			paths = append(paths, d.path)
		}
		byPath[d.path] = append(byPath[d.path], d.doc)
	}

	out := afero.NewMemMapFs()
	for _, p := range paths {
		if err := out.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return nil, err
		}
		if err := afero.WriteFile(out, p, bytes.Join(byPath[p], []byte("---\n")), 0o644); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (f *fsSource) Type() SourceType {
	return SourceTypeCRD
}

// definitions returns the XRD and CRD documents of the filesystem in walk
// order. It reads the filesystem once.
func (f *fsSource) definitions() ([]fsDocument, error) {
	f.once.Do(func() {
		f.err = afero.Walk(f.fs, ".", func(p string, info fs.FileInfo, err error) error {
			if err != nil || info.IsDir() || !isYAML(p) {
				return err
			}
			content, err := afero.ReadFile(f.fs, p)
			if err != nil {
				return err
			}
			return eachYAMLDocument(content, func(doc []byte) error {
				// Documents that don't parse aren't definitions.
				var res struct {
					Kind string `json:"kind"`
				}
				if err := yaml.Unmarshal(doc, &res); err == nil && isDefinitionKind(res.Kind) {
					f.docs = append(f.docs, fsDocument{path: p, doc: doc})
				}
				return nil
			})
		})
	})
	return f.docs, f.err
}

// NewFSSource returns a new filesystem-backed resource source. The id should
// be a stable, location-independent identifier (e.g. a project-relative path)
// since it is persisted in the schema manager's lockfile.
func NewFSSource(id string, fs afero.Fs) Source {
	return &fsSource{id: id, fs: fs}
}

// xpkgSource is a source backed by extracted CRDs from an xpkg. Unlike the
// up CLI, this always generates client-side (never uses pre-packaged schemas).
type xpkgSource struct {
	key    string
	ref    string
	digest string
	crdFS  afero.Fs
}

func (s *xpkgSource) Key() string {
	return "xpkg://" + s.key
}

func (s *xpkgSource) Hash(_ context.Context) (string, error) {
	return s.digest, nil
}

func (s *xpkgSource) Resources(_ context.Context) (afero.Fs, error) {
	return s.crdFS, nil
}

func (s *xpkgSource) Type() SourceType {
	return SourceTypeCRD
}

func (s *xpkgSource) Pin(_ context.Context) (string, error) {
	return s.key + "@" + s.digest, nil
}

func (s *xpkgSource) Version() string {
	return s.ref
}

// NewXpkgSource returns a new xpkg-backed resource source. The key identifies
// the package without its version (e.g. its repository), the ref is the
// resolved tag or version, and the digest is the resolved package digest. The
// crdFS should contain extracted CRD YAML files from the package.
func NewXpkgSource(key, ref, digest string, crdFS afero.Fs) Source {
	return &xpkgSource{
		key:    key,
		ref:    ref,
		digest: digest,
		crdFS:  crdFS,
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
	// cloneRef is what to clone when it differs from the dependency's ref, i.e.
	// the commit a lock pinned. Empty means clone gitRef.Ref.
	cloneRef string
}

func (g *gitSource) Key() string {
	id := fmt.Sprintf("git://%s", g.gitRef.Repository)
	if g.gitRef.Path != "" {
		id = fmt.Sprintf("%s/%s", id, g.gitRef.Path)
	}
	return id
}

func (g *gitSource) Hash(ctx context.Context) (string, error) {
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

	cloneRef := g.cloneRef
	if cloneRef == "" {
		cloneRef = g.gitRef.Ref
	}
	ref := g.normalizeRef(cloneRef)

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

func (g *gitSource) Pin(ctx context.Context) (string, error) {
	return g.Hash(ctx)
}

func (g *gitSource) Version() string {
	return g.gitRef.Ref
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
	return NewPinnedGitSource(dep, "", cloner, authProvider)
}

// NewPinnedGitSource returns a git-backed resource source that clones pin, a
// commit, instead of the dependency's ref. Its Version is still the ref, so a
// lock written from it keeps the branch or tag the pin was resolved from. An
// empty pin clones the ref.
func NewPinnedGitSource(dep v1alpha1.Dependency, pin string, cloner git.Cloner, authProvider git.AuthProvider) Source {
	sourceType := SourceTypeCRD
	if dep.Type == v1alpha1.DependencyTypeK8s {
		sourceType = SourceTypeOpenAPI
	}

	return &gitSource{
		gitRef:       dep.Git,
		cloner:       cloner,
		authProvider: authProvider,
		sourceType:   sourceType,
		cloneRef:     pin,
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

func (h *httpSource) Key() string {
	return fmt.Sprintf("http://%s", h.httpRef.URL)
}

func (h *httpSource) Hash(ctx context.Context) (string, error) {
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

func (h *httpSource) Pin(ctx context.Context) (string, error) {
	return h.Hash(ctx)
}

func (h *httpSource) Version() string {
	return ""
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

func (k *k8sOpenAPISource) Key() string {
	return fmt.Sprintf("k8s://%s", k.k8sRef.Version)
}

// Hash hashes the downloaded OpenAPI specs rather than the release string, so
// a changed spec is noticed even when the version is not.
func (k *k8sOpenAPISource) Hash(ctx context.Context) (string, error) {
	fromFS, err := k.Resources(ctx)
	if err != nil {
		return "", err
	}
	return calculateFilesystemHash(fromFS, SourceTypeOpenAPI)
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

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to download Kubernetes OpenAPI spec: HTTP %d", resp.StatusCode)
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

func (k *k8sOpenAPISource) Pin(_ context.Context) (string, error) {
	return k.k8sRef.Version, nil
}

func (k *k8sOpenAPISource) Version() string {
	return k.k8sRef.Version
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
