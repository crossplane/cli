package tree

import (
	"context"
	"encoding/json"
	"maps"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/cli/v2/pkg/resource"
)

// newTestResource returns a resource with the supplied name, annotations and children.
func newTestResource(name string, annotations map[string]string, children ...*resource.Resource) *resource.Resource {
	u := unstructured.Unstructured{}
	u.SetAPIVersion("example.org/v1")
	u.SetKind("Example")
	u.SetName(name)
	if annotations != nil {
		u.SetAnnotations(annotations)
	}

	return &resource.Resource{Unstructured: u, Children: children}
}

// recordingClient returns a mock client that records the annotations each
// patched object would have after applying the merge patch, keyed by object
// name. If failOn is non-empty, patching the object with that name returns err.
func recordingClient(updated map[string]map[string]string, failOn string, err error) *test.MockClient {
	return &test.MockClient{
		MockPatch: func(_ context.Context, obj client.Object, patch client.Patch, _ ...client.PatchOption) error {
			if obj.GetName() == failOn {
				return err
			}
			data, perr := patch.Data(obj)
			if perr != nil {
				return perr
			}
			p := struct {
				Metadata struct {
					Annotations map[string]*string `json:"annotations"`
				} `json:"metadata"`
			}{}
			if perr := json.Unmarshal(data, &p); perr != nil {
				return perr
			}
			result := map[string]string{}
			maps.Copy(result, obj.GetAnnotations())
			for k, v := range p.Metadata.Annotations {
				if v == nil {
					delete(result, k)
					continue
				}
				result[k] = *v
			}
			updated[obj.GetName()] = result
			return nil
		},
	}
}
