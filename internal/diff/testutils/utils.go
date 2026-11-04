package testutils

import (
	stdlog "log"
	"slices"
	"sync"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/go-logr/stdr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/restmapper"
	kt "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
)

// Colors for terminal output.
const (
	ColorRed    = "\x1b[31m"
	ColorGreen  = "\x1b[32m"
	ColorYellow = "\x1b[33m"
	ColorReset  = "\x1b[0m"
)

// SetupKubeTestLogger sets the global logger for use of the Kube environment to the T.Log of this test.
func SetupKubeTestLogger(t *testing.T) {
	t.Helper()

	// Create a logr.Logger that writes to testing.T.Log
	testLogger := stdr.NewWithOptions(stdlog.New(testWriter{t}, "", 0), stdr.Options{LogCaller: stdr.All})

	// Set the logger for controller-runtime
	log.SetLogger(testLogger)
}

// testWriter adapts testing.T.Log to io.Writer.
type testWriter struct {
	t *testing.T
}

// Write logs the provided argument as a string to the T.Log.
func (tw testWriter) Write(p []byte) (int, error) {
	tw.t.Log(string(p))
	return len(p), nil
}

// TestLogger coerces the T.Log into the shape of a Logr logger.
func TestLogger(t *testing.T, verbose bool) logging.Logger {
	verbosity := 0
	if verbose {
		verbosity = 1
	}

	return logging.NewLogrLogger(testr.NewWithOptions(t, testr.Options{Verbosity: verbosity}))
}

// AdvisoryRecorder is a dt.Advisories that records every advisory raised on it, in order, so a test
// in a package that cannot import diffprocessor's AdvisoryCollector (dockerrun, which it imports) can
// assert what was raised. Unlike the collector it does not collapse repeats.
type AdvisoryRecorder struct {
	mu         sync.Mutex
	advisories []dt.OutputWarning
}

// Raise records w.
func (r *AdvisoryRecorder) Raise(w dt.OutputWarning) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.advisories = append(r.advisories, w)
}

// Advisories returns the advisories recorded so far, in the order they were raised.
func (r *AdvisoryRecorder) Advisories() []dt.OutputWarning {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.advisories)
}

// CreateFakeDiscoveryClient is a helper function to create a fake discovery client for testing.
// The fake discovery client automatically derives API groups from the provided resources,
// enabling both ServerResourcesForGroupVersion() and ServerGroups() to work correctly.
func CreateFakeDiscoveryClient(resources map[string][]metav1.APIResource) discovery.DiscoveryInterface {
	fakeDiscovery := &fakediscovery.FakeDiscovery{
		Fake: &kt.Fake{},
	}

	apiResourceLists := make([]*metav1.APIResourceList, 0, len(resources))

	for gv, apiResources := range resources {
		apiResourceLists = append(apiResourceLists, &metav1.APIResourceList{
			GroupVersion: gv,
			APIResources: apiResources,
		})
	}

	fakeDiscovery.Resources = apiResourceLists
	fakeDiscovery.Resources = apiResourceLists

	return fakeDiscovery
}

// CreateFakeRESTMapper returns a RESTMapper over the same resources CreateFakeDiscoveryClient serves.
func CreateFakeRESTMapper(resources map[string][]metav1.APIResource) meta.RESTMapper {
	groups, err := restmapper.GetAPIGroupResources(CreateFakeDiscoveryClient(resources))
	if err != nil {
		panic(err)
	}

	return restmapper.NewDiscoveryRESTMapper(groups)
}
