package labels

import "context"

type Label struct {
	Name   string
	Source string
	IsCEX  bool
}

type Provider interface {
	Lookup(ctx context.Context, address string) (*Label, error)
}
