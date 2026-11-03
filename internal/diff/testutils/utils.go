package testutils

import (
	"regexp"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/restmapper"
	kt "k8s.io/client-go/testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// Colors for terminal output.
const (
	ColorRed    = "\x1b[31m"
	ColorGreen  = "\x1b[32m"
	ColorYellow = "\x1b[33m"
	ColorReset  = "\x1b[0m"
)

// color takes a multiline string, splits it by line, and adds the specified coloring to each line.
// It returns a single string with all lines joined back together.
func color(colorCode, input string) string {
	lines := strings.Split(input, "\n")
	coloredLines := make([]string, 0, len(lines))

	for _, line := range lines {
		// Handle the case of the last empty line after a newline
		if line == "" && len(coloredLines) == len(lines)-1 {
			coloredLines = append(coloredLines, "")
			continue
		}

		coloredLines = append(coloredLines, colorCode+line+ColorReset)
	}

	return strings.Join(coloredLines, "\n")
}

// Green takes a multiline string, splits it by line, and adds green coloring to each line.
// It returns a single string with all lines joined back together.
func Green(input string) string {
	return color(ColorGreen, input)
}

// Red takes a multiline string, splits it by line, and adds red coloring to each line.
// It returns a single string with all lines joined back together.
func Red(input string) string {
	return color(ColorRed, input)
}

// Yellow takes a multiline string, splits it by line, and adds yellow coloring to each line.
// It returns a single string with all lines joined back together.
func Yellow(input string) string {
	return color(ColorYellow, input)
}

// CompareIgnoringAnsi compares two strings after stripping ANSI special characters.
func CompareIgnoringAnsi(expected, actual string) bool {
	// Strip ANSI codes from both strings
	ansiPattern := regexp.MustCompile("\x1b\\[[0-9;]*m")
	expectedStripped := ansiPattern.ReplaceAllString(expected, "")
	actualStripped := ansiPattern.ReplaceAllString(actual, "")

	// Compare the stripped strings
	return expectedStripped == actualStripped
}

// TestLogger coerces the T.Log into the shape of a Logr logger.
func TestLogger(t *testing.T, verbose bool) logging.Logger {
	verbosity := 0
	if verbose {
		verbosity = 1
	}

	return logging.NewLogrLogger(testr.NewWithOptions(t, testr.Options{Verbosity: verbosity}))
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
