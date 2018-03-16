package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/signal"
	"os/user"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/terminal"

	"github.com/evanphx/mesh"
	"github.com/evanphx/mesh-shell/log"
	"github.com/evanphx/mesh-shell/msg"
	"github.com/evanphx/mesh/instance"
	"github.com/evanphx/mesh/protocol/pipe"
)

type Client struct {
	inst *instance.Instance

	pipe *pipe.Pipe

	options ClientOptions
}

type ClientOptions struct {
	Verbose bool
}

func NewClient(options ClientOptions) (*Client, error) {
	inst, err := instance.InitNew()
	if err != nil {
		return nil, err
	}

	return &Client{inst: inst, options: options}, nil
}

func (c *Client) Connect(ctx context.Context, network, id string) error {
	return log.RunSpinner(ctx, func(s *log.Spinner) error {
		s.Verbose = c.options.Verbose
		s.Printf("discovering endpoints")

		err := c.inst.FindNodes(ctx, network)
		if err != nil {
			return err
		}

		s.Printf("connecting to %s", id)

		time.Sleep(1 * time.Second)

		sel := &mesh.PipeSelector{
			Pipe: "mesh-shell",
			Tags: map[string]string{
				"id": id,
			},
		}

		pipe, err := c.inst.Connect(ctx, sel)
		if err != nil {
			return err
		}

		c.pipe = pipe

		s.Printf("handshaking with %s", pipe.PeerIdentity().Short())

		return c.handshake(ctx, s)
	})
}

type Marshaler interface {
	Marshal() ([]byte, error)
}

func (c *Client) send(ctx context.Context, m Marshaler) error {
	data, err := m.Marshal()
	if err != nil {
		return err
	}

	return c.pipe.Send(ctx, data)
}

type Unmarshaler interface {
	Unmarshal([]byte) error
}

func (c *Client) recv(ctx context.Context, m Unmarshaler) error {
	data, err := c.pipe.Recv(ctx)
	if err != nil {
		return err
	}

	return m.Unmarshal(data)
}

func (c *Client) handshake(ctx context.Context, s *log.Spinner) error {
	var hello msg.Hello
	hello.Ident = "msh v0.1"

	err := c.send(ctx, &hello)
	if err != nil {
		return err
	}

	var shello msg.Hello

	err = c.recv(ctx, &shello)
	if err != nil {
		return err
	}

	s.Printf("server ident: %s", shello.Ident)

	sockAddr := os.Getenv("SSH_AUTH_SOCK")

	sock, err := net.Dial("unix", sockAddr)
	if err != nil {
		return err
	}

	sshAgent := agent.NewClient(sock)

	keys, err := sshAgent.List()
	if err != nil {
		return err
	}

	u, err := user.Current()
	if err != nil {
		return err
	}

	for _, k := range keys {
		h := sha256.Sum256(k.Blob)

		s.Printf("offering %s %s (%s)", base64.StdEncoding.EncodeToString(h[:]), k.Comment, k.Type())

		var aa msg.AuthAttempt
		aa.Type = msg.PUBKEY
		aa.Data = k.Marshal()
		aa.User = u.Username

		err = c.send(ctx, &aa)
		if err != nil {
			return err
		}

		var ac msg.AuthChallenge

		err = c.recv(ctx, &ac)
		if err != nil {
			return err
		}

		if ac.Type == msg.SIGN {
			s.Printf("calculating challenge")
			sig, err := sshAgent.Sign(k, ac.Nonce)
			if err != nil {
				continue
			}

			var ar msg.AuthResponse
			ar.Answer = sig.Blob
			ar.Format = sig.Format

			err = c.send(ctx, &ar)
			if err != nil {
				return err
			}

			var ac msg.AuthChallenge

			err = c.recv(ctx, &ac)
			if err != nil {
				return err
			}

			if ac.Type == msg.ACCEPT {
				s.Printf("key accepted")
				return nil
			}

			s.Printf("key rejected")
		}
	}

	return fmt.Errorf("unable to authenticate")
}

func (c *Client) nextStream() int64 {
	return 1
}

const (
	STDIN  = 0
	STDOUT = 1
	STDERR = 2
)

type streamOutput struct {
	sub  int32
	data []byte
}

func (c *Client) StartShell(ctx context.Context) error {
	oldState, err := terminal.MakeRaw(0)
	if err != nil {
		return err
	}

	defer terminal.Restore(0, oldState)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var req msg.RequestShell
	req.Id = c.nextStream()

	err = c.send(ctx, &req)
	if err != nil {
		return err
	}

	buf := make([]byte, 1024)
	input := make(chan []byte, 10)
	output := make(chan streamOutput)

	go func() {
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading: %s", err)
				return
			}
			input <- buf[:n]
		}
	}()

	go func() {
		for {
			var ctl msg.ControlMessage

			err = c.recv(ctx, &ctl)
			if err != nil {
				return
			}

			switch ctl.Code {
			case msg.CLOSE:
				cancel()
				return

			case msg.DATA:
				output <- streamOutput{sub: ctl.Sub, data: ctl.Data}
			}
		}
	}()

	intSig := make(chan os.Signal, 1)

	signal.Notify(intSig, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	var escapeState int

	for {
		select {
		case <-intSig:
			return nil
		case <-ctx.Done():
			return nil
		case usable := <-input:
			// fmt.Fprintf(os.Stderr, "dn(%v, %d) ", usable, bytes.IndexByte(usable, 10))
			if idx := bytes.IndexByte(usable, 13); idx != -1 {
				// fmt.Fprintf(os.Stderr, "nl(%d)", escapeState)
				if idx == len(usable)-1 {
					escapeState = 1
				} else {
					if usable[idx+1] == '~' {
						escapeState = 2

						var ctl msg.ControlMessage

						ctl.Code = msg.DATA
						ctl.Sub = 0
						ctl.Data = usable[:idx+1]

						err = c.send(ctx, &ctl)
						if err != nil {
							fmt.Fprintf(os.Stderr, "error sending: %s", err)
							return nil
						}

						usable = usable[idx+1:]
					}
				}
			} else if escapeState == 1 {
				if usable[0] == '~' {
					escapeState = 2
					if len(usable) == 1 {
						continue
					}
					usable = usable[1:]
				} else {
					escapeState = 0
				}
			}

			if escapeState == 2 {
				escapeState = 0
				switch usable[0] {
				case '.':
					return nil
				default:
					usable = append([]byte{'~'}, usable...)
				}
			}

			// fmt.Fprintf(os.Stderr, "es(%d)", escapeState)

			var ctl msg.ControlMessage

			ctl.Code = msg.DATA
			ctl.Sub = 0
			ctl.Data = usable

			err = c.send(ctx, &ctl)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error sending: %s", err)
				return nil
			}
		case so := <-output:
			switch so.sub {
			case STDOUT:
				os.Stdout.Write(so.data)
			case STDERR:
				os.Stderr.Write(so.data)
			}
		}
	}
}
