package debugbundle

type EventOption interface {
	apply(*eventOptions)
}

type eventOptions struct {
	context      map[string]any
	failureRef   string
	failureScope *EvidenceScope
}

type eventOptionFunc func(*eventOptions)

func (funcOption eventOptionFunc) apply(options *eventOptions) {
	funcOption(options)
}

func WithEventContext(values map[string]any) EventOption {
	return eventOptionFunc(func(options *eventOptions) {
		if len(values) == 0 {
			return
		}
		if options.context == nil {
			options.context = map[string]any{}
		}
		for key, value := range values {
			options.context[key] = value
		}
	})
}

type MessageOption = EventOption

type ProbeOption interface {
	applyProbe(*probeOptions)
}

type probeOptions struct {
	heavy bool
}

type probeOptionFunc func(*probeOptions)

func (funcOption probeOptionFunc) applyProbe(options *probeOptions) {
	funcOption(options)
}

func WithHeavyProbe() ProbeOption {
	return probeOptionFunc(func(options *probeOptions) {
		options.heavy = true
	})
}

// WithFailureReference attaches an explicit fresh UUID v4 to the current owned
// operation, or to an explicitly supplied page/operation scope.
func WithFailureReference(reference string, scopes ...EvidenceScope) EventOption {
	return eventOptionFunc(func(options *eventOptions) {
		if !validEvidenceID(reference) {
			return
		}
		options.failureRef = reference
		if len(scopes) == 1 {
			scope := scopes[0]
			options.failureScope = &scope
		}
	})
}
