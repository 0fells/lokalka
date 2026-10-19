package tun

import (
	"io"
	"sync"
)

type Device interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	Name() string
}

type Blackhole struct {
	once   sync.Once
	closed chan struct{}
}

func NewBlackhole() *Blackhole {
	return &Blackhole{closed: make(chan struct{})}
}

func (b *Blackhole) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *Blackhole) Write(p []byte) (int, error) { return len(p), nil }

func (b *Blackhole) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func (b *Blackhole) Name() string { return "none" }
