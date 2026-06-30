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

package contextfn

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// Handle is the owner of a running in-process context function.
type Handle struct {
	target       string
	srv          *grpc.Server
	fn           *server
	stop         sync.Once
	seedInput    *runtime.RawExtension
	captureInput *runtime.RawExtension
}

// Captured returns the context observed by the capture step, or nil if
// capture did not run.
func (h *Handle) Captured() *structpb.Struct {
	return h.fn.capturedContext()
}

// Start starts an in-process gRPC server that implements the composition
// function RunFunction RPC for context seeding and capture. Callers must call
// Handle.Stop when done.
func Start(ctx context.Context, log logging.Logger, contextData map[string]any) (*Handle, error) {
	si, err := json.Marshal(input{Mode: modeSeed})
	if err != nil {
		return nil, errors.Wrap(err, "cannot create seed context function input")
	}
	ci, err := json.Marshal(input{Mode: modeCapture})
	if err != nil {
		return nil, errors.Wrap(err, "cannot create capture context function input")
	}

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", ":0")
	if err != nil {
		return nil, errors.Wrap(err, "cannot create listener for context function")
	}

	srv := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	fn := newServer(contextData)
	fnv1.RegisterFunctionRunnerServiceServer(srv, fn)

	addr := lis.Addr().(*net.TCPAddr) //nolint:forcetypeassert // We specified "tcp" above.

	h := &Handle{
		// Report the target as 127.0.0.1:PORT since the render machinery knows
		// how to handle functions listening on loopback.
		target:       fmt.Sprintf("127.0.0.1:%d", addr.Port),
		srv:          srv,
		fn:           fn,
		seedInput:    &runtime.RawExtension{Raw: si},
		captureInput: &runtime.RawExtension{Raw: ci},
	}

	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Debug("Context function gRPC server stopped", "error", err)
		}
	}()

	return h, nil
}

// Stop gracefully stops the function server, which closes its listener. Safe to
// call multiple times.
func (h *Handle) Stop() {
	h.stop.Do(func() {
		done := make(chan struct{})
		go func() {
			h.srv.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			h.srv.Stop()
		}
	})
}
