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

// Package filesystem contains utilities for working with filesystems.
package filesystem

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Walk is a replacement for afero.Walk that ensures paths are normalized for
// in-memory filesystem compatibility on Windows. This reimplementation uses
// path.Join instead of filepath.Join to always use forward slashes.
func Walk(fs afero.Fs, root string, walkFn filepath.WalkFunc) error {
	root = filepath.ToSlash(root)
	if root == "" {
		root = "."
	}

	info, err := fs.Stat(root)
	if err != nil {
		return walkFn(root, nil, err)
	}
	return walk(fs, root, info, walkFn)
}

func walk(fs afero.Fs, p string, info os.FileInfo, walkFn filepath.WalkFunc) error {
	err := walkFn(p, info, nil)
	if err != nil {
		if info.IsDir() && errors.Is(err, filepath.SkipDir) {
			return nil
		}
		return err
	}

	if !info.IsDir() {
		return nil
	}

	f, err := fs.Open(p)
	if err != nil {
		return walkFn(p, info, err)
	}
	defer f.Close() //nolint:errcheck // Can't do anything useful with this error.

	list, err := f.Readdir(-1)
	if err != nil {
		return walkFn(p, info, err)
	}

	for _, fileInfo := range list {
		filename := path.Join(p, fileInfo.Name())
		err = walk(fs, filename, fileInfo, walkFn)
		if err != nil {
			if !fileInfo.IsDir() || !errors.Is(err, filepath.SkipDir) {
				return err
			}
		}
	}
	return nil
}

// CopyFilesBetweenFs copies all files from the source filesystem (fromFS) to
// the destination filesystem (toFS).
func CopyFilesBetweenFs(fromFS, toFS afero.Fs) error {
	return afero.Walk(fromFS, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		dir := filepath.Dir(path)
		if err := toFS.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		fileData, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return err
		}
		return afero.WriteFile(toFS, path, fileData, 0o644)
	})
}

type fsToTarConfig struct {
	symlinkBasePath *string
	uidOverride     *int
	gidOverride     *int
	excludes        []string
}

// FSToTarOption configures the behavior of FSToTar.
type FSToTarOption func(*fsToTarConfig)

// WithSymlinkBasePath provides the real base path of the filesystem, for use in
// symlink resolution.
func WithSymlinkBasePath(bp string) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.symlinkBasePath = &bp
	}
}

// WithUIDOverride sets the owner UID to use in the tar archive.
func WithUIDOverride(uid int) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.uidOverride = &uid
	}
}

// WithGIDOverride sets the owner GID to use in the tar archive.
func WithGIDOverride(gid int) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.gidOverride = &gid
	}
}

// WithExcludePrefix excludes files with the given prefix from the tar archive.
func WithExcludePrefix(prefix string) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.excludes = append(opts.excludes, prefix)
	}
}

// FSToTar produces a tarball of all the files in a filesystem.
func FSToTar(f afero.Fs, prefix string, opts ...FSToTarOption) ([]byte, error) {
	cfg := &fsToTarConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	prefixHdr := &tar.Header{
		Name:     prefix,
		Typeflag: tar.TypeDir,
		Mode:     0o777,
	}
	if cfg.uidOverride != nil {
		prefixHdr.Uid = *cfg.uidOverride
	}
	if cfg.gidOverride != nil {
		prefixHdr.Gid = *cfg.gidOverride
	}

	if err := tw.WriteHeader(prefixHdr); err != nil {
		return nil, errors.Wrap(err, "failed to create prefix directory in tar archive")
	}
	err := Walk(f, ".", func(name string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		for _, prefix := range cfg.excludes {
			if strings.HasPrefix(name, prefix) {
				return filepath.SkipDir
			}
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if cfg.symlinkBasePath == nil {
				return errors.New("cannot follow symlinks unless base path is configured")
			}
			return addSymlinkToTar(tw, prefix, name, cfg)
		}

		return addToTar(tw, prefix, f, name, info, cfg)
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to populate tar archive")
	}
	if err := tw.Close(); err != nil {
		return nil, errors.Wrap(err, "failed to close tar archive")
	}

	return buf.Bytes(), nil
}

func addToTar(tw *tar.Writer, prefix string, f afero.Fs, filename string, info fs.FileInfo, cfg *fsToTarConfig) error {
	fullPath := path.Join(prefix, filename)

	if info.IsDir() {
		if fullPath == prefix {
			return nil
		}

		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = fullPath
		if cfg.uidOverride != nil {
			h.Uid = *cfg.uidOverride
		}
		if cfg.gidOverride != nil {
			h.Gid = *cfg.gidOverride
		}
		return tw.WriteHeader(h)
	}

	if !info.Mode().IsRegular() {
		return errors.Errorf("unhandled file mode %v", info.Mode())
	}

	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = fullPath
	if cfg.uidOverride != nil {
		h.Uid = *cfg.uidOverride
	}
	if cfg.gidOverride != nil {
		h.Gid = *cfg.gidOverride
	}
	if err := tw.WriteHeader(h); err != nil {
		return err
	}

	file, err := f.Open(filename)
	if err != nil {
		return err
	}

	_, err = io.Copy(tw, file)
	return err
}

func addSymlinkToTar(tw *tar.Writer, prefix string, symlinkPath string, cfg *fsToTarConfig) error {
	osFs := afero.NewOsFs()

	targetPath, err := filepath.EvalSymlinks(filepath.Join(*cfg.symlinkBasePath, symlinkPath))
	if err != nil {
		return nil //nolint:nilerr // Symlink target may be missing, safe to skip.
	}

	exists, err := afero.Exists(osFs, targetPath)
	if err != nil || !exists {
		return err
	}

	return afero.Walk(osFs, targetPath, func(symlinkedFile string, symlinkedInfo fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if symlinkedInfo.IsDir() {
			return nil
		}

		targetHeader, err := tar.FileInfoHeader(symlinkedInfo, "")
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(targetPath, symlinkedFile)
		if err != nil {
			return err
		}
		targetHeader.Name = path.Join(prefix, filepath.ToSlash(symlinkPath), filepath.ToSlash(relativePath))
		if cfg.uidOverride != nil {
			targetHeader.Uid = *cfg.uidOverride
		}
		if cfg.gidOverride != nil {
			targetHeader.Gid = *cfg.gidOverride
		}

		if err := tw.WriteHeader(targetHeader); err != nil {
			return err
		}

		targetFile, err := osFs.Open(symlinkedFile)
		if err != nil {
			return err
		}

		_, err = io.Copy(tw, targetFile)
		return err
	})
}

// CopyFolder recursively copies directory and all its contents from sourceDir
// to targetDir.
func CopyFolder(fs afero.Fs, sourceDir, targetDir string) error {
	return afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return errors.Wrapf(err, "failed to determine relative path for %s", path)
		}

		destPath := filepath.Join(targetDir, relPath)

		if info.IsDir() {
			return fs.MkdirAll(destPath, 0o755)
		}

		srcFile, err := fs.Open(path)
		if err != nil {
			return errors.Wrapf(err, "failed to open source file %s", path)
		}

		destFile, err := fs.Create(destPath)
		if err != nil {
			return errors.Wrapf(err, "failed to create destination file %s", destPath)
		}

		_, err = io.Copy(destFile, srcFile)
		return errors.Wrapf(err, "failed to copy file from %s to %s", path, destPath)
	})
}

// CopyFileIfExists copies a file from src to dst if the src file exists.
func CopyFileIfExists(fs afero.Fs, src, dst string) error {
	exists, err := afero.Exists(fs, src)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	srcFile, err := fs.Open(src)
	if err != nil {
		return errors.Wrapf(err, "failed to open source file %s", src)
	}

	destFile, err := fs.Create(dst)
	if err != nil {
		return errors.Wrapf(err, "failed to create destination file %s", dst)
	}

	_, err = io.Copy(destFile, srcFile)
	return errors.Wrapf(err, "failed to copy file from %s to %s", src, dst)
}

// FindNestedFoldersWithPattern finds nested folders containing files that match
// a specified pattern.
func FindNestedFoldersWithPattern(fs afero.Fs, root string, pattern string) ([]string, error) {
	var foldersWithFiles []string

	err := afero.Walk(fs, root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			return nil
		}

		files, err := afero.ReadDir(fs, path)
		if err != nil {
			return err
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}

			match, _ := filepath.Match(pattern, f.Name())
			if match {
				foldersWithFiles = append(foldersWithFiles, path)
				break
			}
		}

		return nil
	})

	return foldersWithFiles, err
}

// FullPath returns the full path to path within the given filesystem. If fs is
// not an afero.BasePathFs the original path is returned.
func FullPath(fs afero.Fs, path string) string {
	bfs, ok := fs.(*afero.BasePathFs)
	if ok {
		return afero.FullBaseFsPath(bfs, path)
	}
	return path
}

// MemOverlay returns a filesystem that uses the given filesystem as a base
// layer but writes changes to an in-memory overlay filesystem.
func MemOverlay(fs afero.Fs) afero.Fs {
	return afero.NewBasePathFs(afero.NewCopyOnWriteFs(fs, afero.NewMemMapFs()), "/")
}

// CreateSymlink creates a symlink in a BasePathFs, potentially to another
// BasePathFs that shares the same underlying filesystem.
func CreateSymlink(targetFS *afero.BasePathFs, targetPath string, sourceFS *afero.BasePathFs, sourcePath string) error {
	realTargetPath, err := targetFS.RealPath(targetPath)
	if err != nil {
		return errors.Wrapf(err, "failed to get real path for targetPath: %s", targetPath)
	}

	realSourcePath, err := sourceFS.RealPath(sourcePath)
	if err != nil {
		return errors.Wrapf(err, "failed to get real path for sourcePath: %s", sourcePath)
	}

	symlinkParentDir := filepath.Dir(realTargetPath)

	absSymlinkParentDir, err := filepath.Abs(symlinkParentDir)
	if err != nil {
		return errors.Wrapf(err, "failed to get absolute path for symlink parent directory: %s", symlinkParentDir)
	}

	absRealSourcePath, err := filepath.Abs(realSourcePath)
	if err != nil {
		return errors.Wrapf(err, "failed to get absolute path for source path: %s", realSourcePath)
	}

	relativeSymlinkPath, err := filepath.Rel(absSymlinkParentDir, absRealSourcePath)
	if err != nil {
		return errors.Wrapf(err, "failed to calculate relative symlink path from %s to %s", absSymlinkParentDir, absRealSourcePath)
	}

	symlinkPath := filepath.Join(absSymlinkParentDir, filepath.Base(realTargetPath))

	if _, err := os.Lstat(symlinkPath); err == nil {
		if err := os.Remove(symlinkPath); err != nil {
			return errors.Wrapf(err, "failed to remove existing symlink or file at %s", symlinkPath)
		}
	}

	if err := os.Symlink(relativeSymlinkPath, symlinkPath); err != nil {
		baseMsg := "failed to create symlink from " + relativeSymlinkPath + " to " + symlinkPath
		if strings.Contains(err.Error(), "A required privilege is not held by the client") {
			return errors.Errorf(
				"%s: %v\n\nOn Windows, creating symlinks requires either:\n"+
					"  1. Running as Administrator, or\n"+
					"  2. Enabling Developer Mode (Settings > Update & Security > For developers > Developer Mode)",
				baseMsg, err,
			)
		}
		return errors.Wrap(err, baseMsg)
	}

	return nil
}
