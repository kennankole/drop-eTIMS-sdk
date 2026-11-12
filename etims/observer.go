package etims

import "time"

// Event describes one call to KRA.
type Event struct {
	// Operation names the call: "initialize", "register_item",
	// "submit_invoice", "item_classes", "codes", "branches".
	Operation string
	Branch    Branch
	// ResultCode is KRA's resultCd when a response was parsed.
	ResultCode string
	Err        error
	Latency    time.Duration
}

// Observer receives an Event for every call, so applications can wire
// metrics and alarms without this package depending on a metrics library.
type Observer interface {
	Observe(*Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(*Event)

// Observe implements Observer.
func (f ObserverFunc) Observe(e *Event) { f(e) }

type noopObserver struct{}

func (noopObserver) Observe(*Event) {}

// NoopObserver discards every event.
var NoopObserver Observer = noopObserver{}
