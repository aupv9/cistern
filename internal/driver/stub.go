package driver

import "context"

type Stub struct {
	name   string
	Params Params
}

func NewStub(name string, p Params) *Stub { return &Stub{name: name, Params: p} }

func (s *Stub) Name() string                               { return s.name }
func (s *Stub) Check(context.Context) error                { return ErrNotImplemented }
func (s *Stub) Backup(context.Context, Sink) (Info, error) { return Info{}, ErrNotImplemented }
func (s *Stub) Restore(context.Context, Source) error      { return ErrNotImplemented }
