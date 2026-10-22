package diffprocessor

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	xp "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	k8 "github.com/crossplane/cli/v2/internal/diff/client/kubernetes"
	"github.com/crossplane/cli/v2/internal/diff/compare"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
)

const (
	// DefaultMaxNestedDepth is the default maximum depth for recursive nested XR processing. The
	// processor constructors apply it, and the CLI's --max-nested-depth default must match it.
	DefaultMaxNestedDepth = 10

	// DefaultMaxRenderIterations is the default maximum render iterations. The processor constructors
	// apply it, and the CLI's --max-iterations default must match it.
	DefaultMaxRenderIterations = 20
)

// AnalyzeOn names the smallest composition change that triggers per-composite impact analysis.
// The values are ordered from least to most eager.
type AnalyzeOn string

const (
	// AnalyzeOnSpecChange evaluates composites only when the composition's spec changes. The
	// cheapest setting, and a deliberate user judgement: a metadata-only change still produces a new
	// CompositionRevision, whose identity a composition template can observe through the XR's
	// compositionRevisionRef, so choosing this asserts that none of your compositions do that.
	AnalyzeOnSpecChange AnalyzeOn = "spec-change"

	// AnalyzeOnAnyChange evaluates composites whenever anything Crossplane hashes into the
	// composition's identity differs — labels and annotations as well as spec. The default, because
	// whether a metadata-only change reaches rendered output can only be settled by rendering.
	AnalyzeOnAnyChange AnalyzeOn = "any-change"

	// AnalyzeOnAlways evaluates composites even when the composition is identical, which creates no
	// revision and so cannot change anything. Useful as a pre-edit "is my cluster converged?"
	// baseline; note that any delta it reports is by construction not caused by this composition.
	AnalyzeOnAlways AnalyzeOn = "always"
)

// DryRunOn selects which resources are round-tripped through the apiserver to compute their
// post-apply form. Like AnalyzeOn it is a depth/cost knob: every resource dry-run costs one
// apiserver round-trip and requires a permission, and a resource left un-dry-run is marked with a
// DryRunInfo so "we did not check" stays distinguishable from "we checked and it matches".
type DryRunOn string

const (
	// DryRunOnExisting dry-runs only resources that already exist in the cluster, leaving additions
	// as the render pipeline produced them plus locally predicted CRD defaults (see Defaulter). This
	// was the only behaviour before crossplane-diff#334. It needs no create permission, and costs one
	// round-trip fewer per added resource.
	DryRunOnExisting DryRunOn = "existing"

	// DryRunOnAll additionally dry-run creates added resources, so their diffs pick up
	// apiserver-side defaulting and mutating admission — neither of which the local Defaulter can
	// predict for a built-in type, and neither of which it can predict at all for admission. The
	// default. Where the create is not permitted, the resource degrades to rendered output plus
	// locally predicted CRD defaults, and says so rather than failing the run.
	DryRunOnAll DryRunOn = "all"
)

// ChangeScope is how much of a composition differs from its in-cluster version, in terms of what
// Crossplane hashes into the composition's identity (see Composition.Hash()).
type ChangeScope string

const (
	// ChangeScopeNone means nothing Crossplane hashes differs. No CompositionRevision is created, so
	// nothing can adopt anything.
	ChangeScopeNone ChangeScope = "none"

	// ChangeScopeMetadata means the labels or annotations differ but the spec does not. A new
	// CompositionRevision is still created, carrying an identical spec.
	ChangeScopeMetadata ChangeScope = "metadata"

	// ChangeScopeSpec means the spec differs.
	ChangeScopeSpec ChangeScope = "spec"
)

// triggersAnalysis reports whether a change of this scope should cause per-composite impact
// analysis at the given setting.
func (s ChangeScope) triggersAnalysis(on AnalyzeOn) bool {
	switch on {
	case AnalyzeOnAlways:
		return true
	case AnalyzeOnSpecChange:
		return s == ChangeScopeSpec
	case AnalyzeOnAnyChange, "":
		return s != ChangeScopeNone
	default:
		// Unrecognized values are rejected by the CLI; fall back to the default rather than
		// silently analysing nothing.
		return s != ChangeScopeNone
	}
}

// ProcessorConfig contains configuration for the DiffProcessor. It holds no presentation state: the
// processors return reports, and the caller renders them.
type ProcessorConfig struct {
	// MaxNestedDepth is the maximum depth for recursive nested XR processing
	MaxNestedDepth int

	// IncludeManual determines whether to include XRs with Manual update policy in composition diffs
	IncludeManual bool

	// Advisories is the channel user-facing advisories are raised on, and drained from into the report.
	// Pass the same collector to every processor of one run (the CLI passes one set of options to
	// both), so the composition processor's report includes what its XR processor raised. Unset, each
	// processor collects its own, writing nowhere; the CLI injects one that also writes to stderr.
	Advisories *AdvisoryCollector

	// AnalyzeOn is the smallest composition change that triggers per-composite impact analysis.
	// Evaluating a composite costs one function render, so this is a cost knob — it never suppresses
	// a reported consequence. RevisionImpact is populated at every setting, and composites left
	// unevaluated are marked ImpactAnalysisSkipped so "we did not look" stays distinguishable from
	// "we looked and found nothing".
	//
	// Zero value means AnalyzeOnAnyChange, matching the CLI default.
	AnalyzeOn AnalyzeOn

	// DryRunOn selects which resources are verified against the apiserver. Zero value means
	// DryRunOnAll, matching the CLI default.
	DryRunOn DryRunOn

	// EventualState enables iterative simulation to show eventual state after all reconciliation
	// cycles complete. Useful with function-sequencer which hides later stage resources.
	EventualState bool

	// MaxRenderIterations is the maximum number of render iterations when resolving requirements
	// or simulating eventual state. Higher values may be needed for complex pipelines.
	MaxRenderIterations int

	// ChangedFunctions names functions whose behaviour differs from the cluster's. A composition whose
	// pipeline calls one is analysed as at least a metadata-scope change, even if it is otherwise
	// identical. Empty by default, which changes nothing.
	ChangedFunctions map[string]bool

	// IgnorePaths is a list of paths to ignore when calculating diffs
	IgnorePaths []string

	// KeepEqualDiffs, when set, names the composed resources whose diff is kept even when equal, and
	// makes an unchanged composite's impact carry its diffs. Nil, the default, keeps none. Every
	// HasChanges and renderer skips equal diffs, so this only adds diffs a caller can refine further.
	KeepEqualDiffs func(*un.Unstructured) bool

	// XRTimeout, when positive, bounds each composite resource's diff, one input of PerformDiff or one
	// affected composite of DiffComposition: its render and AfterXRDiff. A composite that runs out
	// fails alone, as a failed render does, and the next one still renders. Zero, the default, adds
	// no deadline to the caller's.
	XRTimeout time.Duration

	// AfterXRDiff, when set, runs after each composite resource's diff succeeds, under its XRTimeout
	// deadline, with the composite's diffs. An error fails the composite as a failed render does: none
	// of its diffs are reported. Nil, the default, runs nothing.
	AfterXRDiff func(ctx context.Context, diffs map[string]*dt.ResourceDiff) error

	// FunctionCredentials holds Secret credentials to pass to Functions during rendering
	FunctionCredentials []corev1.Secret

	// FunctionRegistryOverride overrides the registry in all function package refs.
	FunctionRegistryOverride string

	// MaxRecvMessageSize is the max gRPC message size (MB) for render function
	// containers. Zero leaves the function's own default (function-sdk-go uses
	// 4MB). When >0 it is injected as the appropriate container env var,
	//  so large XRs don't trip the default limit under render.
	MaxRecvMessageSize int

	// Logger is the logger to use
	Logger logging.Logger

	// RenderFunc is the function to use for rendering resources. If left nil,
	// processors construct a default engine-backed RenderFn on initialization.
	RenderFunc RenderFn

	// CrossplaneRenderBinary, when non-empty, causes the default
	// engine-backed RenderFn to invoke a local `crossplane` binary at the
	// supplied path instead of the upstream docker engine. This is a test
	// affordance that gives integration tests a fast in-process render path;
	// production users should leave it empty so the docker engine pulls
	// xpkg.crossplane.io/crossplane/crossplane:stable. Ignored when
	// RenderFunc is set explicitly.
	CrossplaneRenderBinary string

	// CrossplaneVersion, when non-empty, causes the default engine-backed
	// RenderFn's docker engine to pull
	// xpkg.crossplane.io/crossplane/crossplane:<version> instead of :stable.
	// Mutually exclusive with CrossplaneImage and CrossplaneRenderBinary.
	// Ignored when RenderFunc is set explicitly.
	CrossplaneVersion string

	// CrossplaneImage, when non-empty, causes the default engine-backed
	// RenderFn's docker engine to pull this full image reference instead of
	// the default xpkg.crossplane.io/crossplane/crossplane:stable. Mutually
	// exclusive with CrossplaneVersion and CrossplaneRenderBinary. Ignored
	// when RenderFunc is set explicitly.
	CrossplaneImage string

	// Factories provide factory functions for creating components
	Factories ComponentFactories
}

// ComponentFactories contains factory functions for creating processor components.
type ComponentFactories struct {
	// ResourceManager creates a ResourceManager
	ResourceManager func(client k8.ResourceClient, defClient xp.DefinitionClient, treeClient xp.ResourceTreeClient, logger logging.Logger, advisories dt.Advisories) ResourceManager

	// SchemaValidator creates a SchemaValidator
	SchemaValidator func(schema k8.SchemaClient, resource k8.ResourceClient, def xp.DefinitionClient, logger logging.Logger) SchemaValidator

	// Defaulter creates a Defaulter with the given policy. The processor makes two: a strict one for the
	// XR it renders, and a lenient one the DiffCalculator uses to predict additions.
	Defaulter func(schema k8.SchemaClient, def xp.DefinitionClient, policy DefaultingPolicy) Defaulter

	// DiffCalculator creates a DiffCalculator
	DiffCalculator func(apply k8.ApplyClient, access k8.AccessChecker, tree xp.ResourceTreeClient, resourceManager ResourceManager, logger logging.Logger, compareOptions compare.Options, dryRunOn DryRunOn, predictor Defaulter) DiffCalculator

	// RequirementsProvider creates an ExtraResourceProvider
	RequirementsProvider func(res k8.ResourceClient, def xp.EnvironmentClient, logger logging.Logger) *RequirementsProvider

	// FunctionProvider creates a FunctionProvider
	FunctionProvider func(fnClient xp.FunctionClient, logger logging.Logger) FunctionProvider

	// InputValidator creates the InputValidator for one PerformDiff run
	InputValidator InputValidatorFactory
}

// ProcessorOption defines a function that can modify a ProcessorConfig.
type ProcessorOption func(*ProcessorConfig)

// WithMaxNestedDepth sets the maximum depth for recursive nested XR processing.
func WithMaxNestedDepth(depth int) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.MaxNestedDepth = depth
	}
}

// WithIncludeManual sets whether to include XRs with Manual update policy in composition diffs.
func WithIncludeManual(includeManual bool) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.IncludeManual = includeManual
	}
}

// WithAdvisories sets the collector advisories are raised on and drained from for structured output.
func WithAdvisories(advisories *AdvisoryCollector) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Advisories = advisories
	}
}

// WithAnalyzeOn sets the smallest composition change that triggers per-composite impact analysis
// (see ProcessorConfig.AnalyzeOn). An empty value leaves the default in place.
func WithAnalyzeOn(analyzeOn AnalyzeOn) ProcessorOption {
	return func(config *ProcessorConfig) {
		if analyzeOn != "" {
			config.AnalyzeOn = analyzeOn
		}
	}
}

// WithChangedFunctions names functions whose behaviour differs from the cluster's (see
// ProcessorConfig.ChangedFunctions).
func WithChangedFunctions(names ...string) ProcessorOption {
	return func(config *ProcessorConfig) {
		if config.ChangedFunctions == nil {
			config.ChangedFunctions = make(map[string]bool, len(names))
		}

		for _, n := range names {
			config.ChangedFunctions[n] = true
		}
	}
}

// WithDryRunOn sets which resources are round-tripped through the apiserver. An empty value leaves
// the config's default in place, matching WithAnalyzeOn.
func WithDryRunOn(dryRunOn DryRunOn) ProcessorOption {
	return func(config *ProcessorConfig) {
		if dryRunOn != "" {
			config.DryRunOn = dryRunOn
		}
	}
}

// WithEventualState sets whether to show eventual state after all reconciliation cycles complete.
// When enabled, the processor runs an iterative simulation that synthesizes Ready status on
// rendered resources until no new resources appear. This is useful with function-sequencer
// which hides later stage resources until earlier stages become Ready.
func WithEventualState(enabled bool) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.EventualState = enabled
	}
}

// WithMaxRenderIterations sets the maximum number of render iterations when resolving requirements
// or simulating eventual state.
func WithMaxRenderIterations(maxIterations int) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.MaxRenderIterations = maxIterations
	}
}

// WithIgnorePaths sets the paths to ignore when calculating diffs.
func WithIgnorePaths(ignorePaths []string) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.IgnorePaths = ignorePaths
	}
}

// WithXRTimeout bounds each composite resource's diff (see ProcessorConfig.XRTimeout).
func WithXRTimeout(d time.Duration) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.XRTimeout = d
	}
}

// WithAfterXRDiff runs fn after each composite resource's diff (see ProcessorConfig.AfterXRDiff).
func WithAfterXRDiff(fn func(ctx context.Context, diffs map[string]*dt.ResourceDiff) error) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.AfterXRDiff = fn
	}
}

// WithKeepEqualDiffs keeps the equal diffs of the composed resources keep reports true for (see
// ProcessorConfig.KeepEqualDiffs).
func WithKeepEqualDiffs(keep func(*un.Unstructured) bool) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.KeepEqualDiffs = keep
	}
}

// WithFunctionCredentials sets the credentials to pass to Functions during rendering.
func WithFunctionCredentials(creds []corev1.Secret) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.FunctionCredentials = creds
	}
}

// WithFunctionRegistryOverride overrides the registry in all function package refs.
func WithFunctionRegistryOverride(registry string) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.FunctionRegistryOverride = registry
	}
}

// WithMaxRecvMessageSize sets the max gRPC message size (MB) injected into
// render function containers.
func WithMaxRecvMessageSize(mb int) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.MaxRecvMessageSize = mb
	}
}

// WithLogger sets the logger for the processor.
func WithLogger(logger logging.Logger) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Logger = logger
	}
}

// WithRenderFunc sets the render function for the processor.
func WithRenderFunc(renderFn RenderFn) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.RenderFunc = renderFn
	}
}

// WithCrossplaneRenderBinary points the default engine-backed RenderFn at a
// local `crossplane` binary instead of the upstream docker engine. See
// ProcessorConfig.CrossplaneRenderBinary for the semantics — production
// callers should not use this; it exists for fast integration-test iteration.
func WithCrossplaneRenderBinary(path string) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.CrossplaneRenderBinary = path
	}
}

// WithCrossplaneVersion pins the crossplane render version the default
// engine-backed RenderFn's docker engine pulls (…/crossplane:<version>).
// See ProcessorConfig.CrossplaneVersion. Mutually exclusive with
// WithCrossplaneImage and WithCrossplaneRenderBinary.
func WithCrossplaneVersion(version string) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.CrossplaneVersion = version
	}
}

// WithCrossplaneImage pins the full crossplane render image reference the
// default engine-backed RenderFn's docker engine pulls. See
// ProcessorConfig.CrossplaneImage. Mutually exclusive with
// WithCrossplaneVersion and WithCrossplaneRenderBinary.
func WithCrossplaneImage(image string) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.CrossplaneImage = image
	}
}

// WithResourceManagerFactory sets the ResourceManager factory function.
func WithResourceManagerFactory(factory func(k8.ResourceClient, xp.DefinitionClient, xp.ResourceTreeClient, logging.Logger, dt.Advisories) ResourceManager) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.ResourceManager = factory
	}
}

// WithSchemaValidatorFactory sets the SchemaValidator factory function.
func WithSchemaValidatorFactory(factory func(k8.SchemaClient, k8.ResourceClient, xp.DefinitionClient, logging.Logger) SchemaValidator) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.SchemaValidator = factory
	}
}

// WithDefaulterFactory sets the Defaulter factory function.
func WithDefaulterFactory(factory func(k8.SchemaClient, xp.DefinitionClient, DefaultingPolicy) Defaulter) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.Defaulter = factory
	}
}

// WithDiffCalculatorFactory sets the DiffCalculator factory function.
func WithDiffCalculatorFactory(factory func(k8.ApplyClient, k8.AccessChecker, xp.ResourceTreeClient, ResourceManager, logging.Logger, compare.Options, DryRunOn, Defaulter) DiffCalculator) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.DiffCalculator = factory
	}
}

// WithRequirementsProviderFactory sets the RequirementsProvider factory function.
func WithRequirementsProviderFactory(factory func(k8.ResourceClient, xp.EnvironmentClient, logging.Logger) *RequirementsProvider) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.RequirementsProvider = factory
	}
}

// WithInputValidatorFactory sets the InputValidator factory function, called once per PerformDiff run.
func WithInputValidatorFactory(factory InputValidatorFactory) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.InputValidator = factory
	}
}

// WithFunctionProviderFactory sets the FunctionProvider factory function.
func WithFunctionProviderFactory(factory func(xp.FunctionClient, logging.Logger) FunctionProvider) ProcessorOption {
	return func(config *ProcessorConfig) {
		config.Factories.FunctionProvider = factory
	}
}

// ErrXRTimedOut is the cause an XR context is cancelled with when its XRTimeout budget runs out.
// OutsideXRDeadline also returns it, so callers can match it with errors.Is.
var ErrXRTimedOut = errors.New("composite resource timed out")

// xrContext returns the context one composite resource's diff runs under: ctx bounded by XRTimeout
// when it is set. The budget can be paused with OutsideXRDeadline.
func (c *ProcessorConfig) xrContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.XRTimeout <= 0 {
		return ctx, func() {}
	}

	xrCtx, cancel := context.WithCancelCause(ctx)
	b := &xrBudget{remaining: c.XRTimeout, run: ctx, cancel: cancel, start: time.Now()}
	b.timer = time.AfterFunc(c.XRTimeout, b.expire)

	return context.WithValue(xrCtx, budgetKey{}, b), func() {
		b.mu.Lock()
		b.expired = true // A pause still running must not re-arm the timer.
		b.timer.Stop()
		b.mu.Unlock()
		cancel(context.Canceled)
	}
}

// NewXRContext returns the context a composite resource's diff runs under with an XRTimeout of d.
func NewXRContext(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return (&ProcessorConfig{XRTimeout: d}).xrContext(ctx)
}

// budgetKey keys the xrBudget of a composite resource's context.
type budgetKey struct{}

// xrBudget is the pausable XRTimeout of one composite resource.
type xrBudget struct {
	run    context.Context //nolint:containedctx // The budget hands the run context to OutsideXRDeadline's callers.
	cancel context.CancelCauseFunc

	mu        sync.Mutex
	timer     *time.Timer
	start     time.Time
	remaining time.Duration
	pauses    int
	expired   bool
}

func (b *xrBudget) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.expired = true
	b.cancel(ErrXRTimedOut)
}

// pause stops the clock, reporting false when the budget already ran out.
func (b *xrBudget) pause() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.expired {
		return false
	}

	if b.pauses == 0 {
		if !b.timer.Stop() {
			// The timer fired; its expiry is on its way.
			b.expired = true
			return false
		}

		b.remaining -= time.Since(b.start)
	}

	b.pauses++

	return true
}

// resume restarts the clock once the last concurrent pause ends.
func (b *xrBudget) resume() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pauses--
	if b.pauses == 0 && !b.expired {
		b.start = time.Now()
		b.timer = time.AfterFunc(max(b.remaining, 0), b.expire)
	}
}

// OutsideXRDeadline runs fn with the run context, which ctx's composite resource budget (XRTimeout)
// does not bound, and does not count fn's time against that budget. Work shared by the whole run,
// like starting function runtimes and provider diff servers, which pulls images, goes through it so
// it neither fails nor is billed to the composite that happens to need it first. Calls may nest and
// run concurrently. Without a budget in ctx it calls fn(ctx). If the budget already ran out it
// returns that expiry without running fn.
func OutsideXRDeadline(ctx context.Context, fn func(run context.Context) error) error {
	b, ok := ctx.Value(budgetKey{}).(*xrBudget)
	if !ok {
		return fn(ctx)
	}

	if !b.pause() {
		return ErrXRTimedOut
	}
	defer b.resume()

	return fn(b.run) //nolint:contextcheck // The run context is deliberately the one the XR budget does not bound.
}

// afterXR finishes one composite resource's diff, which returned diffs and err under xrCtx, a child
// of ctx: it runs AfterXRDiff on a success, and says so when the composite ran out of XRTimeout.
func (c *ProcessorConfig) afterXR(ctx, xrCtx context.Context, diffs map[string]*dt.ResourceDiff, err error) error {
	if err == nil && c.AfterXRDiff != nil {
		err = c.AfterXRDiff(xrCtx, diffs)
	}

	// Only the composite's own deadline is named; the caller's ending reads as it always has.
	if err != nil && ctx.Err() == nil && errors.Is(context.Cause(xrCtx), ErrXRTimedOut) {
		return errors.Wrapf(err, "timed out after %s", c.XRTimeout)
	}

	return err
}

// GetCompareOptions returns the compare.Options based on the ProcessorConfig.
func (c *ProcessorConfig) GetCompareOptions() compare.Options {
	return compare.Options{IgnorePaths: c.IgnorePaths, KeepEqualDiffs: c.KeepEqualDiffs}
}

// SetDefaultFactories sets default component factory functions if not already set.
func (c *ProcessorConfig) SetDefaultFactories() {
	if c.Factories.ResourceManager == nil {
		c.Factories.ResourceManager = NewResourceManager
	}

	if c.Factories.SchemaValidator == nil {
		c.Factories.SchemaValidator = NewSchemaValidator
	}

	if c.Factories.Defaulter == nil {
		c.Factories.Defaulter = NewDefaulter
	}

	if c.Factories.DiffCalculator == nil {
		c.Factories.DiffCalculator = NewDiffCalculator
	}

	if c.Factories.RequirementsProvider == nil {
		c.Factories.RequirementsProvider = NewRequirementsProvider
	}

	if c.Factories.FunctionProvider == nil {
		// Use CachedFunctionProvider by default for container reuse across renders.
		// This prevents container proliferation with --eventual-state (multiple iterations)
		// and enables reuse when diffing multiple XRs with the same composition.
		c.Factories.FunctionProvider = NewCachedFunctionProvider
	}

	if c.Factories.InputValidator == nil {
		c.Factories.InputValidator = NewBundleInputValidator
	}
}
