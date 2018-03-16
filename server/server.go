package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log"
	"os/exec"
	"sync"

	"github.com/evanphx/mesh-shell/msg"
	"github.com/evanphx/mesh/instance"
	"github.com/evanphx/mesh/pb"
	"github.com/evanphx/mesh/protocol/pipe"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-multierror"
	"github.com/kr/pty"
	uuid "github.com/satori/go.uuid"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	id   string
	inst *instance.Instance
	lp   *pipe.ListenPipe
	log  hclog.Logger
}

type ServerOptions struct {
	Id      string
	Network string
	Debug   bool
}

func NewServer(opts ServerOptions) (*Server, error) {
	inst, err := instance.InitNew()
	if err != nil {
		return nil, err
	}

	_, err = inst.ListenTCP(":0", []string{opts.Network})
	if err != nil {
		return nil, err
	}

	adver := &pb.Advertisement{
		Pipe: "mesh-shell",
		Tags: map[string]string{
			"id": opts.Id,
		},
	}

	lp, err := inst.Listen(adver)
	if err != nil {
		return nil, err
	}

	logLevel := hclog.Info

	if opts.Debug {
		logLevel = hclog.Debug
	}

	l := hclog.New(&hclog.LoggerOptions{
		Name:  "mshd",
		Level: logLevel,
	})

	l.Info("listening for connections", "network", opts.Network, "pipe", "mesh-shell", "id", opts.Id)

	serv := &Server{
		id:   opts.Id,
		inst: inst,
		lp:   lp,
		log:  l,
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
				s.log.Error("error handling session", "error", err)
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

	id := uuid.NewV4().String()

	cLogger := s.log.With("id", id)

	cLogger.Debug("client connected", "ident", hello.Ident)

	var shello msg.Hello
	shello.Ident = "mshd v0.1"

	err = sess.send(ctx, &shello)
	if err != nil {
		return err
	}

	var (
		user string
		keys Keys
		auth bool
	)

authLoop:
	for {
		var aa msg.AuthAttempt

		err = sess.recv(ctx, &aa)
		if err != nil {
			return err
		}

		cLogger.Debug("auth attempt", "type", aa.Type)

		if aa.Type == msg.PUBKEY {
			if user != aa.User {
				user = aa.User
				keys, err = KeysForUser(user)
				if err != nil {
					cLogger.Error("error reading keys", "error", err)
				}
			}

			cLogger.Debug("available keys", "count", len(keys))

			var found *Key

			for _, k := range keys {
				if bytes.Equal(aa.Data, k.Key.Marshal()) {
					found = k
					break
				}
			}

			if found == nil {
				var ac msg.AuthChallenge
				ac.Type = msg.REJECT

				err = sess.send(ctx, &ac)
				if err != nil {
					return err
				}
				continue
			}

			h := sha256.Sum256(found.Key.Marshal())
			cLogger.Debug("issuing challenge", "key", base64.StdEncoding.EncodeToString(h[:]))

			nonce := make([]byte, 128)

			_, err = io.ReadFull(rand.Reader, nonce)
			if err != nil {
				return err
			}

			var ac msg.AuthChallenge
			ac.Type = msg.SIGN
			ac.Nonce = nonce

			err = sess.send(ctx, &ac)
			if err != nil {
				return err
			}

			var ar msg.AuthResponse

			err = sess.recv(ctx, &ar)
			if err != nil {
				return err
			}

			sig := &ssh.Signature{
				Format: ar.Format,
				Blob:   ar.Answer,
			}

			cLogger.Debug("verifying signature")

			err = found.Key.Verify(nonce, sig)
			if err == nil {
				var ac msg.AuthChallenge
				ac.Type = msg.ACCEPT

				cLogger.Info("successful login", "request_user", aa.User, "key_comment", found.Comment, "key_options", found.Options)

				err = sess.send(ctx, &ac)
				if err != nil {
					return err
				}

				auth = true
				break authLoop
			} else {
				var ac msg.AuthChallenge
				ac.Type = msg.REJECT

				cLogger.Debug("rejected signature")

				err = sess.send(ctx, &ac)
				if err != nil {
					return err
				}
			}
		} else {
			var ac msg.AuthChallenge
			ac.Type = msg.REJECT

			err = sess.send(ctx, &ac)
			if err != nil {
				return err
			}
		}
	}

	if !auth {
		cLogger.Info("rejected auth attempt", "request_user", user)
		return nil
	}

	var req msg.RequestShell
	err = sess.recv(ctx, &req)
	if err != nil {
		return err
	}

	cLogger.Debug("starting shell")

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
