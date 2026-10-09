package docker

import (
	"context"
	"fmt"
	"slices"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// StorageType identifies the storage backend used by a container.
type StorageType string

// Supported container storage backends.
const (
	StorageTypeBindMount StorageType = "bind"
	StorageTypeVolume    StorageType = "volume"
)

// ValidStorageTypes lists the supported container storage backend names.
func ValidStorageTypes() []string {
	return []string{
		string(StorageTypeBindMount),
		string(StorageTypeVolume),
	}
}

// IsValidStorageType returns true if the supplied storageType string is one of the supported storagetypes.
func IsValidStorageType(storageType string) bool {
	return slices.Contains(ValidStorageTypes(), storageType)
}

// Storage configures container storage and synchronizes files to it.
type Storage interface {
	// Type identifies the storage backend.
	Type() StorageType

	// ContainerOptions returns the required docker start options for its storage type.
	ContainerOptions() []StartContainerOption

	// Sync ensures the specified directory is synced to the container.
	Sync(ctx context.Context, containerID string) error

	// Returns the destination directory of the storage.
	DestDir() string
}

var (
	_ Storage = (*VolumeStorage)(nil)
	_ Storage = (*BindMountStorage)(nil)
)

// BindMountStorage provides storage operations for bind-mounts, which mounts a directory on the host machine to a registry container.
type BindMountStorage struct {
	sourceDir string
	destDir   string
}

// NewBindMountStorage creates storage that bind-mounts the source directory into the container.
func NewBindMountStorage(sourceDir, destDir string) *BindMountStorage {
	return &BindMountStorage{
		sourceDir: sourceDir,
		destDir:   destDir,
	}
}

// Type returns the bind-mount storage type.
func (s BindMountStorage) Type() StorageType {
	return StorageTypeBindMount
}

// ContainerOptions returns the options for bind-mounting the source directory.
func (s BindMountStorage) ContainerOptions() []StartContainerOption {
	return []StartContainerOption{
		StartWithBindMount(s.sourceDir, s.destDir),
	}
}

// Sync is a no-op because the source directory is already bind-mounted.
func (s BindMountStorage) Sync(_ context.Context, _ string) error {
	return nil
}

// DestDir returns the directory where the storage is mounted in the container.
func (s BindMountStorage) DestDir() string {
	return s.destDir
}

// VolumeStorage provides storage operations for volume-based registry storage.
// This is necessary for DinD scenarios, where bind-mount is unusable cause the bound file-system is
// the file system of the docker daemon, not the running cli process.
type VolumeStorage struct {
	sourceDir string
	destDir   string
	initFiles []byte
}

// NewVolumeStorage creates volume storage initialized with the supplied file archive.
func NewVolumeStorage(sourceDir, destDir string, initFiles []byte) *VolumeStorage {
	return &VolumeStorage{
		sourceDir: sourceDir,
		destDir:   destDir,
		initFiles: initFiles,
	}
}

// Type returns the volume storage type.
func (s VolumeStorage) Type() StorageType {
	return StorageTypeVolume
}

// ContainerOptions returns the options for mounting and initializing the volume.
func (s VolumeStorage) ContainerOptions() []StartContainerOption {
	return []StartContainerOption{
		StartWithVolume(s.destDir),
		StartWithCopyFiles(s.initFiles, s.destDir),
	}
}

// Sync copies the source directory to the volume in the specified container.
func (s VolumeStorage) Sync(ctx context.Context, containerID string) error {
	if err := CopyDirectoryToContainer(ctx, containerID, s.sourceDir, s.destDir); err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to copy directory %q to %q", s.sourceDir, containerID))
	}

	return nil
}

// DestDir returns the directory where the storage is mounted in the container.
func (s VolumeStorage) DestDir() string {
	return s.destDir
}
