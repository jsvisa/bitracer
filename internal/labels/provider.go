package labels

import (
	"context"
	"log/slog"
)

type Label struct {
	Name   string
	Source string
	IsCEX  bool
	Kind   string
}

type Provider interface {
	Name() string
	Lookup(ctx context.Context, address string) (*Label, error)
}

type Registry struct {
	providers []Provider
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: providers}
}

func (r *Registry) Providers() []Provider {
	return r.providers
}

func (r *Registry) Lookup(ctx context.Context, address string) (*Label, error) {
	for _, p := range r.providers {
		lbl, err := p.Lookup(ctx, address)
		if err != nil {
			slog.Warn("label provider failed", "vendor", p.Name(), "address", address, "err", err)
			continue
		}
		if lbl != nil {
			if lbl.Source == "" {
				lbl.Source = p.Name()
			}
			return lbl, nil
		}
	}
	return nil, nil
}
