package tracer

import (
	"time"

	opentracing "github.com/opentracing/opentracing-go"
)

// SpanReporter record completed Spans
type SpanReporter interface {
	ReportSpan(span RawSpan)
}

// Sampler control if a span shold be sampled
type Sampler interface {
	ShouldSample(span RawSpan) bool
	IsEarly() bool
}

// WavefrontTracer implements the `Tracer` interface.
type WavefrontTracer struct {
	textPropagator     *textMapPropagator
	binaryPropagator   *binaryPropagator
	accessorPropagator *accessorPropagator

	earlySamplers []Sampler
	lateSamplers  []Sampler
	reporter      SpanReporter
}

// Option allow WavefrontTracer customization
type Option func(*WavefrontTracer)

// WithSampler define a Sampler
func WithSampler(sampler Sampler) Option {
	return func(args *WavefrontTracer) {
		if sampler.IsEarly() {
			args.earlySamplers = append(args.earlySamplers, sampler)
		} else {
			args.lateSamplers = append(args.lateSamplers, sampler)
		}
	}
}

// New creates and returns a WavefrontTracer which defers completed Spans to
// `reporter`.
func New(reporter SpanReporter, options ...Option) opentracing.Tracer {
	tracer := &WavefrontTracer{
		reporter: reporter,
	}

	tracer.textPropagator = &textMapPropagator{tracer}
	tracer.binaryPropagator = &binaryPropagator{tracer}
	tracer.accessorPropagator = &accessorPropagator{tracer}

	for _, option := range options {
		option(tracer)
	}
	return tracer
}

func (t *WavefrontTracer) StartSpan(operationName string, opts ...opentracing.StartSpanOption) opentracing.Span {
	options := opentracing.StartSpanOptions{}
	for _, o := range opts {
		o.Apply(&options)
	}

	// Start time.
	startTime := options.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}

	// Tags.
	tags := options.Tags

	// Build the new span. This is the only allocation: We'll return this as
	// an opentracing.Span.
	sp := t.getSpan()

	// Look for a parent in the list of References.
	var firstChildOfRef SpanContext
	var firstFollowsFromRef SpanContext
	var refCtx SpanContext

	for _, ref := range options.References {
		switch ref.Type {
		case opentracing.ChildOfRef:
			if len(firstChildOfRef.TraceID) == 0 {
				firstChildOfRef = ref.ReferencedContext.(SpanContext)
			}
		case opentracing.FollowsFromRef:
			if len(firstChildOfRef.TraceID) == 0 {
				firstFollowsFromRef = ref.ReferencedContext.(SpanContext)
			}
		}
	}

	if len(firstChildOfRef.TraceID) != 0 {
		refCtx = firstChildOfRef
	} else {
		refCtx = firstFollowsFromRef
	}

	if len(refCtx.TraceID) != 0 {
		sp.raw.Context.TraceID = refCtx.TraceID
		sp.raw.Context.SpanID = randomID()
		sp.raw.Context.Sampled = refCtx.Sampled
		sp.raw.ParentSpanID = refCtx.SpanID

		if l := len(refCtx.Baggage); l > 0 {
			sp.raw.Context.Baggage = make(map[string]string, l)
			for k, v := range refCtx.Baggage {
				sp.raw.Context.Baggage[k] = v
			}
		}
	} else {
		// No parent Span found; allocate new trace and span ids and determine
		// the Sampled status.
		sp.raw.Context.TraceID, sp.raw.Context.SpanID = randomID2()
		if len(t.earlySamplers) == 0 && len(t.lateSamplers) == 0 {
			sp.raw.Context.Sampled = true
		} else if len(t.earlySamplers) > 0 {
			for _, sampler := range t.earlySamplers {
				if !sp.raw.Context.Sampled {
					sp.raw.Context.Sampled = sampler.ShouldSample(sp.raw)
				}
			}
		} else {
			sp.raw.Context.Sampled = false
		}
	}

	sp.tracer = t
	sp.raw.Operation = operationName
	sp.raw.Start = startTime
	sp.raw.Duration = -1
	sp.raw.Tags = tags
	sp.raw.References = options.References
	return sp
}

func (t *WavefrontTracer) getSpan() *spanImpl {
	return &spanImpl{}
}

type delegatorType struct{}

// Delegator is the format to use for DelegatingCarrier.
var Delegator delegatorType

func (t *WavefrontTracer) Inject(sc opentracing.SpanContext, format interface{}, carrier interface{}) error {
	switch format {
	case opentracing.TextMap, opentracing.HTTPHeaders:
		return t.textPropagator.Inject(sc, carrier)
	case opentracing.Binary:
		return t.binaryPropagator.Inject(sc, carrier)
	}
	if _, ok := format.(delegatorType); ok {
		return t.accessorPropagator.Inject(sc, carrier)
	}
	return opentracing.ErrUnsupportedFormat
}

func (t *WavefrontTracer) Extract(format interface{}, carrier interface{}) (opentracing.SpanContext, error) {
	switch format {
	case opentracing.TextMap, opentracing.HTTPHeaders:
		return t.textPropagator.Extract(carrier)
	case opentracing.Binary:
		return t.binaryPropagator.Extract(carrier)
	}
	if _, ok := format.(delegatorType); ok {
		return t.accessorPropagator.Extract(carrier)
	}
	return nil, opentracing.ErrUnsupportedFormat
}
