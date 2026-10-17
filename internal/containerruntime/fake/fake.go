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

// Package fake provides a fake Docker client for testing code built on
// containerruntime without a real Docker daemon.
package fake

import (
	"context"
	"io"

	"github.com/moby/moby/client"

	"github.com/crossplane/cli/v2/internal/containerruntime"
)

// Client implements containerruntime.Client with injectable behavior per
// method.
type Client struct {
	MockImagePull        func(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
	MockContainerInspect func(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	MockContainerCreate  func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	MockContainerStart   func(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	MockContainerStop    func(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	MockContainerRemove  func(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

var _ containerruntime.Client = &Client{}

// ImagePull calls MockImagePull.
func (c *Client) ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	return c.MockImagePull(ctx, ref, options)
}

// ContainerInspect calls MockContainerInspect.
func (c *Client) ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return c.MockContainerInspect(ctx, containerID, options)
}

// ContainerCreate calls MockContainerCreate.
func (c *Client) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return c.MockContainerCreate(ctx, options)
}

// ContainerStart calls MockContainerStart.
func (c *Client) ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return c.MockContainerStart(ctx, containerID, options)
}

// ContainerStop calls MockContainerStop.
func (c *Client) ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return c.MockContainerStop(ctx, containerID, options)
}

// ContainerRemove calls MockContainerRemove.
func (c *Client) ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return c.MockContainerRemove(ctx, containerID, options)
}

// ImagePullDone is a MockImagePull that reports an image pull that has
// already completed.
func ImagePullDone(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	return pullDoneResponse{}, nil
}

// pullDoneResponse is an ImagePullResponse whose body is empty. Only the
// io.ReadCloser methods PullImage uses are implemented; the embedded
// interface is nil.
type pullDoneResponse struct {
	client.ImagePullResponse
}

func (pullDoneResponse) Read([]byte) (int, error) { return 0, io.EOF }

func (pullDoneResponse) Close() error { return nil }
