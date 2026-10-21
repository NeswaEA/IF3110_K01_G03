package database

import (
	"context"
	"fmt"
)

// DispatchInspector routes to the per-driver inspector. It is wired in
// cmd/server so postgres and sqlite stay independent of each other.
type DispatchInspector struct {
	ByDriver map[Driver]Inspector
}

func (d DispatchInspector) pick(t TargetHandle) (Inspector, error) {
	in, ok := d.ByDriver[t.Driver]
	if !ok {
		return nil, fmt.Errorf("%w: no inspector for driver %q", ErrInvalidSnapshot, t.Driver)
	}
	return in, nil
}

func (d DispatchInspector) Inspect(ctx context.Context, t TargetHandle) (SchemaSnapshot, error) {
	in, err := d.pick(t)
	if err != nil {
		return SchemaSnapshot{}, err
	}
	return in.Inspect(ctx, t)
}

func (d DispatchInspector) InspectWith(ctx context.Context, t TargetHandle, q SQLExecutor) (SchemaSnapshot, error) {
	in, err := d.pick(t)
	if err != nil {
		return SchemaSnapshot{}, err
	}
	return in.InspectWith(ctx, t, q)
}
