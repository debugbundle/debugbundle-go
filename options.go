package debugbundle

type EventOption interface {
	apply(*eventOptions)
}

type eventOptions struct {
	context map[string]any
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
