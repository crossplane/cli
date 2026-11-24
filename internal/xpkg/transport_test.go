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

package xpkg

import (
	"testing"
	"time"
)

func TestNewRegistryTransport(t *testing.T) {
	t.Parallel()

	type args struct {
		responseHeaderTimeout time.Duration
		sendPingTimeout       time.Duration
	}
	type want struct {
		responseHeaderTimeout time.Duration
		sendPingTimeout       time.Duration
		maxIdleConnsPerHost   int
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Defaults": {
			reason: "The transport should carry the given timeouts and ggcr's idle pool size per host.",
			args:   args{responseHeaderTimeout: RegistryResponseHeaderTimeout, sendPingTimeout: RegistrySendPingTimeout},
			want: want{
				responseHeaderTimeout: RegistryResponseHeaderTimeout,
				sendPingTimeout:       RegistrySendPingTimeout,
				maxIdleConnsPerHost:   50,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tr := NewRegistryTransport(tc.args.responseHeaderTimeout, tc.args.sendPingTimeout)
			if tr.ResponseHeaderTimeout != tc.want.responseHeaderTimeout {
				t.Errorf("\n%s\nResponseHeaderTimeout: want %s, got %s", tc.reason, tc.want.responseHeaderTimeout, tr.ResponseHeaderTimeout)
			}
			if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != tc.want.sendPingTimeout {
				t.Errorf("\n%s\nHTTP2: want SendPingTimeout %s, got %+v", tc.reason, tc.want.sendPingTimeout, tr.HTTP2)
			}
			if tr.MaxIdleConnsPerHost != tc.want.maxIdleConnsPerHost {
				t.Errorf("\n%s\nMaxIdleConnsPerHost: want %d, got %d", tc.reason, tc.want.maxIdleConnsPerHost, tr.MaxIdleConnsPerHost)
			}
		})
	}
}
