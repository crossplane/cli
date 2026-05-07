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

package filesystem

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

type fileInfo struct {
	mode int64
	uid  int
	gid  int
}

func TestFSToTar(t *testing.T) {
	// Helper function to read the contents of a TAR file.
	readTar := func(t *testing.T, tarData []byte) map[string]*tar.Header {
		t.Helper()
		tr := tar.NewReader(bytes.NewReader(tarData))
		files := map[string]*tar.Header{}
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			files[hdr.Name] = hdr
		}
		return files
	}

	tests := []struct {
		name          string
		setupFs       func(fs afero.Fs)
		prefix        string
		opts          []FSToTarOption
		expectErr     bool
		expectedFiles map[string]fileInfo
	}{
		{
			name: "SimpleFileTarWithPrefix",
			setupFs: func(fs afero.Fs) {
				// Create a file in the in-memory file system.
				_ = afero.WriteFile(fs, "file.txt", []byte("test content"), os.ModePerm)
			},
			prefix: "my-prefix/",
			expectedFiles: map[string]fileInfo{
				"my-prefix/":         {mode: 0o777},
				"my-prefix/file.txt": {mode: 0o777},
			},
		},
		{
			name: "NonRegularFileDirectory",
			setupFs: func(fs afero.Fs) {
				// Create a directory, which should be ignored by FSToTar.
				_ = fs.Mkdir("dir", os.ModePerm)
			},
			prefix: "my-prefix/",
			expectedFiles: map[string]fileInfo{
				"my-prefix/": {mode: 0o777}, // Only prefix should exist, no dir should be included.
			},
		},
		{
			name: "FilesystemWithMultipleFiles",
			setupFs: func(fs afero.Fs) {
				// Create multiple files in the in-memory file system.
				_ = afero.WriteFile(fs, "file1.txt", []byte("test content 1"), os.ModePerm)
				_ = afero.WriteFile(fs, "file2.txt", []byte("test content 2"), os.ModePerm)
			},
			prefix: "another-prefix/",
			expectedFiles: map[string]fileInfo{
				"another-prefix/":          {mode: 0o777},
				"another-prefix/file1.txt": {mode: 0o777},
				"another-prefix/file2.txt": {mode: 0o777},
			},
		},
		{
			name: "UIDOverride",
			setupFs: func(fs afero.Fs) {
				// Create multiple files in the in-memory file system.
				_ = afero.WriteFile(fs, "file1.txt", []byte("test content 1"), os.ModePerm)
				_ = afero.WriteFile(fs, "file2.txt", []byte("test content 2"), os.ModePerm)
			},
			prefix: "my-prefix/",
			opts: []FSToTarOption{
				WithUIDOverride(2345),
			},
			expectedFiles: map[string]fileInfo{
				"my-prefix/":          {mode: 0o777, uid: 2345},
				"my-prefix/file1.txt": {mode: 0o777, uid: 2345},
				"my-prefix/file2.txt": {mode: 0o777, uid: 2345},
			},
		},
		{
			name: "GIDOverride",
			setupFs: func(fs afero.Fs) {
				// Create multiple files in the in-memory file system.
				_ = afero.WriteFile(fs, "file1.txt", []byte("test content 1"), os.ModePerm)
				_ = afero.WriteFile(fs, "file2.txt", []byte("test content 2"), os.ModePerm)
			},
			prefix: "my-prefix/",
			opts: []FSToTarOption{
				WithGIDOverride(2345),
			},
			expectedFiles: map[string]fileInfo{
				"my-prefix/":          {mode: 0o777, gid: 2345},
				"my-prefix/file1.txt": {mode: 0o777, gid: 2345},
				"my-prefix/file2.txt": {mode: 0o777, gid: 2345},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup in-memory file system.
			fs := afero.NewMemMapFs()

			// Apply the setup function for the file system.
			tt.setupFs(fs)

			// Run the FSToTar function.
			tarData, err := FSToTar(fs, tt.prefix, tt.opts...)

			// Validate errors if expected.
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			// Read the TAR contents.
			files := readTar(t, tarData)

			// Validate that the correct files were included.
			for expectedFile, expectedInfo := range tt.expectedFiles {
				file, ok := files[expectedFile]
				if !ok {
					t.Fatalf("%s not found in tar", expectedFile)
				}

				if diff := cmp.Diff(expectedInfo.mode, file.Mode); diff != "" {
					t.Errorf("Incorrect file mode for %s (-want +got):\n%s", expectedFile, diff)
				}
				if diff := cmp.Diff(expectedInfo.uid, file.Uid); diff != "" {
					t.Errorf("Incorrect UID for %s (-want +got):\n%s", expectedFile, diff)
				}
				if diff := cmp.Diff(expectedInfo.gid, file.Gid); diff != "" {
					t.Errorf("Incorrect GID for %s (-want +got):\n%s", expectedFile, diff)
				}
			}
		})
	}
}

func TestCopyFilesBetweenFs(t *testing.T) {
	tests := []struct {
		name          string
		setupFromFs   func(fromFS afero.Fs)
		setupToFs     func(toFS afero.Fs)
		expectedFiles map[string]string // Map of file paths to their expected content in destination filesystem
		expectErr     bool
	}{
		{
			name: "CopySingleFile",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a single file.
				_ = afero.WriteFile(fromFS, "file.txt", []byte("file content"), os.ModePerm)
			},
			setupToFs: func(_ afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"file.txt": "file content", // File content should be the same
			},
		},
		{
			name: "SkipDirectories",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a file inside a directory.
				_ = fromFS.Mkdir("dir", os.ModePerm)
				_ = afero.WriteFile(fromFS, "dir/file.txt", []byte("nested file content"), os.ModePerm)
			},
			setupToFs: func(_ afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"dir/file.txt": "nested file content", // Only the file inside the directory should be copied.
			},
		},
		{
			name: "MultipleFilesInRoot",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with multiple files.
				_ = afero.WriteFile(fromFS, "file1.txt", []byte("file 1 content"), os.ModePerm)
				_ = afero.WriteFile(fromFS, "file2.txt", []byte("file 2 content"), os.ModePerm)
			},
			setupToFs: func(_ afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"file1.txt": "file 1 content",
				"file2.txt": "file 2 content",
			},
		},
		{
			name: "FileOverwriteInDestination",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a file.
				_ = afero.WriteFile(fromFS, "file.txt", []byte("new file content"), os.ModePerm)
			},
			setupToFs: func(toFS afero.Fs) {
				// Setup destination filesystem with an existing file.
				_ = afero.WriteFile(toFS, "file.txt", []byte("old file content"), os.ModePerm)
			},
			expectedFiles: map[string]string{
				"file.txt": "new file content", // The content should be overwritten in the destination.
			},
		},
		{
			name: "CopyFileInNestedDirectory",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a file deep inside nested directories.
				_ = fromFS.MkdirAll("dir1/dir2", os.ModePerm)
				_ = afero.WriteFile(fromFS, "dir1/dir2/file.txt", []byte("deep nested file content"), os.ModePerm)
			},
			setupToFs: func(_ afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"dir1/dir2/file.txt": "deep nested file content", // Ensure nested directories are created and file copied.
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup in-memory filesystems.
			fromFS := afero.NewMemMapFs()
			toFS := afero.NewMemMapFs()

			// Apply file system setup for the test case.
			tt.setupFromFs(fromFS)
			tt.setupToFs(toFS)

			// Run the CopyFilesBetweenFs function.
			err := CopyFilesBetweenFs(fromFS, toFS)

			// Validate errors if expected.
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			// Validate that the expected files exist in the destination filesystem.
			for filePath, expectedContent := range tt.expectedFiles {
				data, err := afero.ReadFile(toFS, filePath)
				if err != nil {
					t.Fatalf("Expected file %s not found in destination filesystem: %v", filePath, err)
				}
				if diff := cmp.Diff(expectedContent, string(data)); diff != "" {
					t.Errorf("Content mismatch for file %s (-want +got):\n%s", filePath, diff)
				}
			}
		})
	}
}

func TestCopyFolder(t *testing.T) {
	tests := []struct {
		name        string
		setupFs     func(fs afero.Fs)
		sourceDir   string
		targetDir   string
		expectedErr bool
		verifyFs    func(t *testing.T, fs afero.Fs)
	}{
		{
			name:      "CopyEmptyDirectory",
			sourceDir: "/source",
			targetDir: "/target",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("/source", os.ModePerm)
			},
			expectedErr: false,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				exists, err := afero.DirExists(fs, "/target")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected target directory to exist, but it does not")
				}
			},
		},
		{
			name:      "CopySingleFile",
			sourceDir: "/source",
			targetDir: "/target",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("/source", os.ModePerm)
				_ = afero.WriteFile(fs, "/source/file1.txt", []byte("content"), 0o644)
			},
			expectedErr: false,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				exists, err := afero.Exists(fs, "/target/file1.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected file to be copied to target, but it does not exist")
				}

				content, err := afero.ReadFile(fs, "/target/file1.txt")
				if err != nil {
					t.Errorf("unexpected error reading file: %v", err)
				}
				if string(content) != "content" {
					t.Errorf("expected file content 'content', got '%s'", string(content))
				}
			},
		},
		{
			name:      "CopyNestedDirectories",
			sourceDir: "/source",
			targetDir: "/target",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("/source/dir1/dir2", os.ModePerm)
				_ = afero.WriteFile(fs, "/source/dir1/file1.txt", []byte("file1 content"), 0o644)
				_ = afero.WriteFile(fs, "/source/dir1/dir2/file2.txt", []byte("file2 content"), 0o644)
			},
			expectedErr: false,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				exists, err := afero.Exists(fs, "/target/dir1/file1.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected file1 to be copied to target, but it does not exist")
				}

				exists, err = afero.Exists(fs, "/target/dir1/dir2/file2.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected file2 to be copied to target, but it does not exist")
				}

				content1, err := afero.ReadFile(fs, "/target/dir1/file1.txt")
				if err != nil {
					t.Errorf("unexpected error reading file1: %v", err)
				}
				if string(content1) != "file1 content" {
					t.Errorf("expected file1 content 'file1 content', got '%s'", string(content1))
				}

				content2, err := afero.ReadFile(fs, "/target/dir1/dir2/file2.txt")
				if err != nil {
					t.Errorf("unexpected error reading file2: %v", err)
				}
				if string(content2) != "file2 content" {
					t.Errorf("expected file2 content 'file2 content', got '%s'", string(content2))
				}
			},
		},
		{
			name:        "SourceDirDoesNotExist",
			sourceDir:   "/nonexistent",
			targetDir:   "/target",
			setupFs:     func(_ afero.Fs) {},
			expectedErr: true,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				exists, err := afero.DirExists(fs, "/target")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if exists {
					t.Errorf("expected target directory not to exist when source does not exist, but it does")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tt.setupFs(fs)

			err := CopyFolder(fs, tt.sourceDir, tt.targetDir)
			if tt.expectedErr && err == nil {
				t.Errorf("expected an error, but got none")
			} else if !tt.expectedErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			tt.verifyFs(t, fs)
		})
	}
}

func TestCopyFileIfExists(t *testing.T) {
	tests := []struct {
		name        string
		setupFs     func(fs afero.Fs)
		src         string
		dst         string
		expectedErr bool
		verifyFs    func(t *testing.T, fs afero.Fs)
	}{
		{
			name: "SourceFileExists",
			src:  "/source/file.txt",
			dst:  "/destination/file.txt",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("/source", os.ModePerm)
				_ = afero.WriteFile(fs, "/source/file.txt", []byte("file content"), 0o644)
			},
			expectedErr: false,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				exists, err := afero.Exists(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected destination file to exist, but it does not")
				}

				content, err := afero.ReadFile(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error reading file: %v", err)
				}
				if string(content) != "file content" {
					t.Errorf("expected file content 'file content', got '%s'", string(content))
				}
			},
		},
		{
			name: "SourceFileDoesNotExist",
			src:  "/source/nonexistent.txt",
			dst:  "/destination/file.txt",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("/source", os.ModePerm)
			},
			expectedErr: false,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				exists, err := afero.Exists(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if exists {
					t.Errorf("expected destination file not to exist when source does not exist")
				}
			},
		},
		{
			name: "OverwriteDestinationFile",
			src:  "/source/file.txt",
			dst:  "/destination/file.txt",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("/source", os.ModePerm)
				_ = fs.MkdirAll("/destination", os.ModePerm)
				_ = afero.WriteFile(fs, "/source/file.txt", []byte("new content"), 0o644)
				_ = afero.WriteFile(fs, "/destination/file.txt", []byte("old content"), 0o644)
			},
			expectedErr: false,
			verifyFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				content, err := afero.ReadFile(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error reading file: %v", err)
				}
				if string(content) != "new content" {
					t.Errorf("expected file content 'new content', got '%s'", string(content))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tt.setupFs(fs)

			err := CopyFileIfExists(fs, tt.src, tt.dst)
			if tt.expectedErr && err == nil {
				t.Errorf("expected an error, but got none")
			} else if !tt.expectedErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			tt.verifyFs(t, fs)
		})
	}
}

func TestWalk(t *testing.T) {
	tests := []struct {
		name         string
		setupFs      func(fs afero.Fs)
		root         string
		expectedPath []string
		skipDir      string
		expectErr    bool
	}{
		{
			name: "WalkSingleFile",
			setupFs: func(fs afero.Fs) {
				_ = afero.WriteFile(fs, "file.txt", []byte("content"), os.ModePerm)
			},
			root:         ".",
			expectedPath: []string{".", "file.txt"},
		},
		{
			name: "WalkNestedDirectories",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("dir1/dir2", os.ModePerm)
				_ = afero.WriteFile(fs, "dir1/file1.txt", []byte("content1"), os.ModePerm)
				_ = afero.WriteFile(fs, "dir1/dir2/file2.txt", []byte("content2"), os.ModePerm)
			},
			root: ".",
			expectedPath: []string{
				".",
				"dir1",
				"dir1/dir2",
				"dir1/dir2/file2.txt",
				"dir1/file1.txt",
			},
		},
		{
			name: "WalkSkipDir",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("dir1/dir2", os.ModePerm)
				_ = afero.WriteFile(fs, "dir1/file1.txt", []byte("content1"), os.ModePerm)
				_ = afero.WriteFile(fs, "dir1/dir2/file2.txt", []byte("content2"), os.ModePerm)
				_ = afero.WriteFile(fs, "file.txt", []byte("content"), os.ModePerm)
			},
			root:    ".",
			skipDir: "dir1",
			expectedPath: []string{
				".",
				"file.txt",
			},
		},
		{
			name: "WalkEmptyDirectory",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("empty", os.ModePerm)
			},
			root:         ".",
			expectedPath: []string{".", "empty"},
		},
		{
			name: "WalkNonExistentRoot",
			setupFs: func(_ afero.Fs) {
			},
			root:      "nonexistent",
			expectErr: true,
		},
		{
			name: "WalkForwardSlashPaths",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("dir/subdir", os.ModePerm)
				_ = afero.WriteFile(fs, "dir/file.txt", []byte("content"), os.ModePerm)
			},
			root: ".",
			expectedPath: []string{
				".",
				"dir",
				"dir/file.txt",
				"dir/subdir",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tt.setupFs(fs)

			var visited []string
			err := Walk(fs, tt.root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if tt.skipDir != "" && info.IsDir() && path == tt.skipDir {
					return filepath.SkipDir
				}
				visited = append(visited, path)
				return nil
			})

			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.expectedPath, visited); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestWalkSkipDirHandling(t *testing.T) {
	tests := []struct {
		name         string
		setupFs      func(fs afero.Fs)
		walkFn       filepath.WalkFunc
		expectedPath []string
	}{
		{
			name: "SkipDirOnDirectory",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("skip/subdir", os.ModePerm)
				_ = afero.WriteFile(fs, "skip/file.txt", []byte("content"), os.ModePerm)
				_ = afero.WriteFile(fs, "keep.txt", []byte("content"), os.ModePerm)
			},
			walkFn: func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if path == "skip" && info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			},
			expectedPath: []string{".", "keep.txt"},
		},
		{
			name: "SkipDirInNestedWalk",
			setupFs: func(fs afero.Fs) {
				_ = fs.MkdirAll("dir1/skip/nested", os.ModePerm)
				_ = fs.MkdirAll("dir1/keep", os.ModePerm)
				_ = afero.WriteFile(fs, "dir1/skip/file.txt", []byte("content"), os.ModePerm)
				_ = afero.WriteFile(fs, "dir1/keep/file.txt", []byte("content"), os.ModePerm)
			},
			walkFn: func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if path == "dir1/skip" && info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			},
			expectedPath: []string{
				".",
				"dir1",
				"dir1/keep",
				"dir1/keep/file.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tt.setupFs(fs)

			var visited []string
			err := Walk(fs, ".", func(path string, info os.FileInfo, err error) error {
				walkErr := tt.walkFn(path, info, err)
				if errors.Is(walkErr, filepath.SkipDir) {
					return filepath.SkipDir
				}
				visited = append(visited, path)
				return walkErr
			})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.expectedPath, visited); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}
