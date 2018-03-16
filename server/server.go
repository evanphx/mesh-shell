package server

import (
	"context"
	"io"
	"log"
	"os/exec"
	"sync"

	"github.com/evanphx/mesh-shell/msg"
	"github.com/evanphx/mesh/instance"
	"github.com/evanphx/mesh/pb"
	"github.com/evanphx/mesh/protocol/pipe"
	"github.com/hashicorp/go-multierror"
	"github.com/kr/pty"
)

type Server struct {
	id   string
	inst *instance.Instance
	lp   *pipe.ListenPipe
}

func NewServer(id, network string) (*Server, error) {
	inst, err := instance.InitNew()
	if err != nil {
		return nil, err
	}

	_, err = inst.ListenTCP(":0", []string{network})
	if err != nil {
		return nil, err
	}

	adver := &pb.Advertisement{
		Pipe: "mesh-shell",
		Tags: map[string]string{
			"id": id,
		},
	}

	lp, err := inst.Listen(adver)
	if err != nil {
		return nil, err
	}

	serv := &Server{
		id:   id,
		inst: inst,
		lp:   lp,
	}

	return serv, nil
}

func (s *Server) Accept(ctx context.Context) error {
	for {
		pipe, err := s.lp.Accept(ctx)
		if err != nil {
			return err
		}

		go func() {
			err := s.handle(&ServerSession{pipe: pipe})
			if err != nil {
				log.Printf("error handling session: %s\n", err)
			}
		}()
	}
}

type ServerSession struct {
	pipe *pipe.Pipe
}

type Marshaler interface {
	Marshal() ([]byte, error)
}

func (s *ServerSession) send(ctx context.Context, m Marshaler) error {
	data, err := m.Marshal()
	if err != nil {
		return err
	}

	return s.pipe.Send(ctx, data)
}

type Unmarshaler interface {
	Unmarshal([]byte) error
}

func (s *ServerSession) recv(ctx context.Context, m Unmarshaler) error {
	data, err := s.pipe.Recv(ctx)
	if err != nil {
		return err
	}

	return m.Unmarshal(data)
}

func (s *Server) handle(sess *ServerSession) error {
	ctx := context.Background()

	var hello msg.Hello

	err := sess.recv(ctx, &hello)
	if err != nil {
		return err
	}

	log.Printf("client ident: %s\n", hello.Ident)

	var shello msg.Hello
	shello.Ident = "mshd v0.1"

	err = sess.send(ctx, &shello)
	if err != nil {
		return err
	}

	var req msg.RequestShell
	err = sess.recv(ctx, &req)
	if err != nil {
		return err
	}

	log.Printf("requesting shell...")

	err = s.shell(ctx, sess, &req)

	var ctl msg.ControlMessage
	ctl.Code = msg.CLOSE

	sess.send(ctx, &ctl)

	sess.pipe.Close(ctx)

	return err
}

func (s *Server) shell(ctx context.Context, sess *ServerSession, req *msg.RequestShell) error {
	c := exec.Command("bash")
	c.Env = []string{"PS1=msh> ", "PATH=/bin:/usr/bin:/usr/local/bin"}

	f, err := pty.Start(c)
	if err != nil {
		return err
	}

	var (
		pool sync.Pool
		wg   sync.WaitGroup
	)

	pool.New = func() interface{} {
		return make([]byte, 1024)
	}

	errors := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()

		buf := make([]byte, 1024)
		for {
			n, err := f.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("read error: %s", err)
					errors <- err
				}

				break
			}

			// log.Printf("sending data")
			var ctl msg.ControlMessage
			ctl.Code = msg.DATA
			ctl.Sub = 1
			ctl.Data = buf[:n]

			err = sess.send(ctx, &ctl)
			if err != nil {
				errors <- err
				return
			}
		}
	}()

	readctx, readcancel := context.WithCancel(context.Background())
	defer readcancel()

	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			var ctl msg.ControlMessage

			err := sess.recv(readctx, &ctl)
			if err != nil {
				return
			}

			switch ctl.Code {
			case msg.CLOSE:
				f.Close()
			case msg.DATA:
				for len(ctl.Data) > 0 {
					// log.Printf("writing read data")
					n, err := f.Write(ctl.Data)
					if err != nil {
						errors <- err
						return
					}

					ctl.Data = ctl.Data[n:]
				}
			}
		}
	}()

	c.Wait()
	log.Printf("command exitted\n")
	f.Close()
	readcancel()

	wg.Wait()

	close(errors)

	var out error

	for err := range errors {
		out = multierror.Append(out, err)
	}

	return out
}
