package docker

import (
	"context"
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

type StorageType string

const (
	StorageTypeBindMount StorageType = "bindMount"
	StorageTypeVolume    StorageType = "volume"
)

type Storage interface {
	// ContainerOptions returns the required docker start options for its storage type.
	ContainerOptions() []StartContainerOption

	// Sync ensures the specified directory is synced to the container.
	Sync(ctx context.Context, containerId string) error

	// Returns the destination directory of the storage.
	DestDir() string
}

var (
	_ Storage = (*DockerVolumeStorage)(nil)
	_ Storage = (*BindMountStorage)(nil)
)

// BindMountStorage provides storage operations for bind-mounts, which mounts a directory on the host machine to a registry container.
type BindMountStorage struct {
	sourceDir string
	destDir   string
}

func NewBindMountStorage(sourceDir, destDir string) *BindMountStorage {
	return &BindMountStorage{
		sourceDir: sourceDir,
		destDir:   destDir,
	}
}

func (s BindMountStorage) ContainerOptions() []StartContainerOption {
	return []StartContainerOption{
		StartWithBindMount(s.sourceDir, s.destDir),
	}
}

func (s BindMountStorage) Sync(_ context.Context, _ string) error {
	//  The source directory is mounted, no need for sync
	return nil
}

func (s BindMountStorage) DestDir() string {
	return s.destDir
}

// DockerVolumeStorage provides storage operations for volume-based registry storage.
// This is necessary for DinD scenarios, where bind-mount is unusable cause the bound file-system is
// the file system of the docker daemon, not the running cli process.
type DockerVolumeStorage struct {
	sourceDir string
	destDir   string
	initFiles []byte
}

func NewVolumeStorage(sourceDir, destDir string, initFiles []byte) *DockerVolumeStorage {
	return &DockerVolumeStorage{
		sourceDir: sourceDir,
		destDir:   destDir,
		initFiles: initFiles,
	}
}

func (s DockerVolumeStorage) ContainerOptions() []StartContainerOption {
	return []StartContainerOption{
		StartWithVolume(s.destDir),
		StartWithCopyFiles(s.initFiles, s.destDir),
	}
}

func (s DockerVolumeStorage) Sync(ctx context.Context, containerId string) error {
	if err := CopyDirectoryToContainer(ctx, containerId, s.sourceDir, s.destDir); err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to copy directory %q to %q", s.sourceDir, containerId))
	}

	return nil
}

func (s DockerVolumeStorage) DestDir() string {
	return s.destDir
}
