package observability_test

// Shared test helpers for dashboard test files.
// capturingAdapter and strPtr are declared here; all _dashboard_test.go files
// are in package observability_test and share this file.

import (
	"context"
	"fmt"

	"github.com/walker-morse/batch/_shared/ports"
)

// capturingAdapter records every LogEvent and RecordMetric call in memory.
// Used by all dashboard tests to assert event shape without needing a real adapter.
type capturingAdapter struct {
	events  []*ports.LogEvent
	metrics []metricCall
}

type metricCall struct {
	Name  string
	Value float64
	Dims  map[string]string
}

func (c *capturingAdapter) LogEvent(_ context.Context, e *ports.LogEvent) error {
	c.events = append(c.events, e)
	return nil
}

func (c *capturingAdapter) RecordMetric(_ context.Context, name string, value float64, dims map[string]string) error {
	c.metrics = append(c.metrics, metricCall{name, value, dims})
	return nil
}

func (c *capturingAdapter) eventsOfType(t string) []*ports.LogEvent {
	var out []*ports.LogEvent
	for _, e := range c.events {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

func (c *capturingAdapter) firstOfType(t string) *ports.LogEvent {
	for _, e := range c.events {
		if e.EventType == t {
			return e
		}
	}
	return nil
}

func (c *capturingAdapter) metricsNamed(name string) []metricCall {
	var out []metricCall
	for _, m := range c.metrics {
		if m.Name == name {
			out = append(out, m)
		}
	}
	return out
}

func strPtr(s string) *string { return &s }

func itoa(i int) string { return fmt.Sprintf("%d", i)  }
