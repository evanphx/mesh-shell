package log

import (
	"context"
	"fmt"
	"sync"
)

type logger struct {
	mu      sync.Mutex
	spin    Spinner
	running bool
	ctx     context.Context
	cancel  func()
}

var g logger

func (g *logger) setStatus(str string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.running == false {
		g.running = true
		g.ctx, g.cancel = context.WithCancel(context.Background())
		g.spin.Start(g.ctx)
	}

	g.spin.SetMessage(str)
}

func (g *logger) endStatus() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.running {
		g.cancel()
		g.spin.WaitFinished()
	}
}

func Statusf(msg string, args ...interface{}) {
	str := fmt.Sprintf(msg, args...)

	g.setStatus(str)
}

func EndStatus() {
	g.endStatus()
}
