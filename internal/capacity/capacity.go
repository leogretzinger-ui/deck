// Package capacity collects privacy-bounded subscription allowance from the
// real provider executables for the default-off unconfined routing experiment.
package capacity

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/noisefloor-oss/deck/internal/provider"
)

const SourceUnknown = "unknown"

type Model struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name,omitempty"`
	Default          bool     `json:"default,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
}

type Window struct {
	Name         string     `json:"name"`
	Scope        string     `json:"scope"`
	UsedPercent  float64    `json:"used_percent"`
	DurationMins *int64     `json:"duration_minutes,omitempty"`
	ResetsAt     *time.Time `json:"resets_at,omitempty"`
}

type Evidence struct {
	Provider   provider.Name `json:"provider"`
	Source     string        `json:"source"`
	ObservedAt time.Time     `json:"observed_at"`
	Models     []Model       `json:"models"`
	Windows    []Window      `json:"windows"`
	State      string        `json:"state"`
	Diagnostic string        `json:"diagnostic,omitempty"`
}

type Collector struct {
	descriptors map[provider.Name]provider.Descriptor
	now         func() time.Time
	run         commandRunner
}

func New(descriptors map[provider.Name]provider.Descriptor) *Collector {
	return &Collector{descriptors: descriptors, now: time.Now, run: runCommand}
}

func (c *Collector) Providers() []provider.Name {
	names := make([]provider.Name, 0, len(c.descriptors))
	for name := range c.descriptors {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func (c *Collector) Collect(ctx context.Context, name provider.Name) Evidence {
	observedAt := c.now().UTC()
	descriptor, ok := c.descriptors[name]
	if !ok {
		return unknown(name, observedAt, "provider is not configured")
	}
	switch name {
	case provider.Claude:
		return c.collectClaude(ctx, descriptor.Executable(), observedAt)
	case provider.Codex:
		return c.collectCodex(ctx, descriptor.Executable(), observedAt)
	default:
		return unknown(name, observedAt, "no subscription-capacity adapter is available")
	}
}

func unknown(name provider.Name, observedAt time.Time, diagnostic string) Evidence {
	return Evidence{
		Provider: name, Source: SourceUnknown, ObservedAt: observedAt,
		Models: []Model{}, Windows: []Window{}, State: "unknown", Diagnostic: diagnostic,
	}
}

func partialOrAvailable(e Evidence) Evidence {
	switch {
	case len(e.Models) > 0 && len(e.Windows) > 0:
		e.State = "available"
	case len(e.Models) > 0 || len(e.Windows) > 0:
		e.State = "partial"
	default:
		e.State = "unknown"
	}
	return e
}

func timePointer(t time.Time) *time.Time { return &t }

func commandDiagnostic(label string, err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s command failed", label)
}
