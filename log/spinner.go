package log

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var herokuStyle = []string{"⣾", "⣷", "⣯", "⣟", "⡿", "⢿", "⣻", "⣽"}

type Spinner struct {
	Verbose bool

	msg chan string
	out io.Writer
	ctx context.Context
	wg  sync.WaitGroup
}

func (s *Spinner) Start(ctx context.Context) {
	s.msg = make(chan string, 10)
	s.out = os.Stdout
	s.ctx = ctx
	s.wg.Add(1)
	go s.spin()
}

func RunSpinner(ctx context.Context, f func(s *Spinner) error) error {
	var s Spinner

	ctx, cancel := context.WithCancel(ctx)

	defer func() {
		cancel()
		s.WaitFinished()
	}()

	s.Start(ctx)

	return f(&s)
}

func (s *Spinner) SetMessage(str string) {
	s.msg <- str
}

func (s *Spinner) Printf(str string, args ...interface{}) {
	s.SetMessage(fmt.Sprintf(str, args...))
}

func (s *Spinner) WaitFinished() {
	s.wg.Wait()
}

func (s *Spinner) spin() {
	defer s.wg.Done()

	var (
		idx = 0
		msg = ""
	)

	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()

	msgUpdate := time.NewTicker(250 * time.Millisecond)
	defer msgUpdate.Stop()

	for {
		select {
		case <-s.ctx.Done():
			if s.Verbose && msg != "" {
				fmt.Fprintf(s.out, "\r\x1b[K- %s\n", msg)
			} else {
				fmt.Fprintf(s.out, "\r\x1b[K")
			}
			return
		case <-msgUpdate.C:
			select {
			case m := <-s.msg:
				if s.Verbose && msg != "" {
					fmt.Fprintf(s.out, "\r\x1b[K- %s\n", msg)
				}
				msg = m
			default:
			}
		case <-ticker.C:
			idx += 1
			if idx >= len(herokuStyle) {
				idx = 0
			}
		}

		fmt.Fprintf(s.out, "\r\x1b[K%s %s", herokuStyle[idx], msg)
	}
}
