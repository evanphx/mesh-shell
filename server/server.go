package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"gopkg.in/yaml.v2"

	"github.com/evanphx/mesh-shell/backend"
	"github.com/evanphx/mesh-shell/backend/authorized_keys"
	"github.com/evanphx/mesh-shell/backend/github"
	"github.com/evanphx/mesh-shell/msg"
	"github.com/evanphx/mesh/instance"
	"github.com/evanphx/mesh/pb"
	"github.com/evanphx/mesh/protocol/pipe"
	"github.com/gravitational/teleport/lib/shell"
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

	keys backend.KeyRetrieval

	config Config
}

type ServerOptions struct {
	Config  string
	Id      string
	Network string
	Solo    int
	Debug   bool
	JSON    bool
}

func NewServer(opts ServerOptions) (*Server, error) {
	var cfg Config

	if opts.Config != "" {
		data, err := ioutil.ReadFile(opts.Config)
		if err != nil {
			return nil, err
		}

		err = yaml.Unmarshal(data, &cfg)
		if err != nil {
			return nil, err
		}
	}

	if opts.Id == "" {
		cfg.Id = opts.Id
	}

	if opts.Network == "" {
		cfg.Network = opts.Network
	}

	if opts.Solo != 0 {
		cfg.SoloPort = opts.Solo
	}

	instopts := instance.Options{
		AdvertiseMDNS: true,
	}

	if cfg.SoloPort != 0 {
		instopts.NoNetworkAuth = true
		cfg.Id = "solo"
	}

	inst, err := instance.Init(instopts)
	if err != nil {
		return nil, err
	}

	var listenAddr *net.TCPAddr

	if cfg.SoloPort != 0 {
		listenAddr, err = inst.ListenTCP(fmt.Sprintf(":%d", cfg.SoloPort), []string{"-"})
		if err != nil {
			return nil, err
		}
	} else {
		listenAddr, err = inst.ListenTCP(":0", []string{cfg.Network})
		if err != nil {
			return nil, err
		}
	}

	adver := &pb.Advertisement{
		Pipe: "mesh-shell",
		Tags: map[string]string{
			"id": cfg.Id,
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
		Name:       "mshd",
		Level:      logLevel,
		JSONFormat: opts.JSON,
	})

	l.Debug("pipe listen created", "adver", adver.String())

	if cfg.SoloPort != 0 {
		l.Info("listening for connections", "address", listenAddr)
	} else {
		l.Info("listening for connections", "network", opts.Network, "pipe", "mesh-shell", "id", opts.Id)
	}

	var keys backend.KeyRetrieval

	if cfg.Auth.Github.Org != "" {
		var gh github.Backend
		gh.GithubUser = cfg.Auth.Github.AuthUser
		gh.GithubToken = cfg.Auth.Github.AuthToken
		gh.GithubOrg = cfg.Auth.Github.Org
		gh.GroupMembership = cfg.Auth.Github.Teams
		keys = &gh
	} else {
		keys = &authorized_keys.Backend{}
	}

	serv := &Server{
		id:     opts.Id,
		inst:   inst,
		lp:     lp,
		log:    l,
		keys:   keys,
		config: cfg,
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
			id := uuid.NewV4().String()
			logger := s.log.With("id", id)

			err := s.handle(&ServerSession{pipe: pipe, log: logger})
			if err != nil {
				s.log.Error("error handling session", "error", err)
			}
		}()
	}
}

type ServerSession struct {
	pipe *pipe.Pipe
	user string

	log hclog.Logger
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

	sess.log.Debug("client connected", "ident", hello.Ident)

	var shello msg.Hello
	shello.Ident = "mshd v0.1"

	err = sess.send(ctx, &shello)
	if err != nil {
		return err
	}

	var (
		user string
		keys backend.Keys
		auth bool
	)

authLoop:
	for {
		var aa msg.AuthAttempt

		err = sess.recv(ctx, &aa)
		if err != nil {
			return err
		}

		sess.log.Debug("auth attempt", "type", aa.Type)

		if aa.Type == msg.PUBKEY {
			if user != aa.User {
				user = aa.User
				keys, err = s.keys.UserKeys(user)
				if err != nil {
					sess.log.Error("error reading keys", "error", err)
				}
			}

			sess.log.Debug("available keys", "count", len(keys))

			var found *backend.Key

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
			sess.log.Debug("issuing challenge", "key", base64.StdEncoding.EncodeToString(h[:]))

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

			sess.log.Debug("verifying signature")

			err = found.Key.Verify(nonce, sig)
			if err == nil {
				var ac msg.AuthChallenge
				ac.Type = msg.ACCEPT

				sess.log.Info("successful login", "request_user", aa.User, "key_comment", found.Comment, "key_options", found.Options)

				err = sess.send(ctx, &ac)
				if err != nil {
					return err
				}

				sess.user = aa.User
				auth = true
				break authLoop
			} else {
				var ac msg.AuthChallenge
				ac.Type = msg.REJECT

				sess.log.Debug("rejected signature")

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
		sess.log.Info("rejected auth attempt", "request_user", user)
		return nil
	}

	var req msg.RequestShell
	err = sess.recv(ctx, &req)
	if err != nil {
		return err
	}

	err = s.shell(ctx, sess, &req)

	var ctl msg.ControlMessage
	ctl.Code = msg.CLOSE

	sess.send(ctx, &ctl)

	sess.pipe.Close(ctx)

	sess.log.Debug("session ended")

	return err
}

func (s *Server) constructCommand(ctx context.Context, sess *ServerSession, userName string, rs *msg.RequestShell) (*exec.Cmd, error) {
	shell, err := shell.GetLoginShell(userName)
	if err != nil {
		return nil, err
	}

	u, err := user.Lookup(userName)
	if err != nil {
		return nil, err
	}

	remoteId := sess.pipe.RemoteID()
	port := "0"

	host, sport, err := net.SplitHostPort(remoteId)
	if err == nil {
		remoteId = host
		port = sport
	}

	identity := s.inst.Peer.Identity().Short()
	if s.config.SoloPort != 0 {
		identity = "0.0.0.0"
	}

	c := exec.CommandContext(ctx, shell)
	c.Env = []string{
		"LANG=en_US.UTF-8",
		"SHELL=" + shell,
		"USER=" + userName,
		getDefaultEnvPath(u.Uid, loginDefsPath),
		"MSH_PEER=" + sess.pipe.PeerIdentity().Short(),
		fmt.Sprintf("SSH_CLIENT=%s %s %d", remoteId, port, s.config.SoloPort),
		fmt.Sprintf("SSH_CONNECTION=%s %s %s %d", remoteId, port, identity, s.config.SoloPort),
	}

	c.Dir = u.HomeDir

	c.Env = append(c.Env, rs.Env...)

	// this configures shell to run in 'login' mode. from openssh source:
	// "If we have no command, execute the shell.  In this case, the shell
	// name to be passed in argv[0] is preceded by '-' to indicate that
	// this is a login shell."
	// https://github.com/openssh/openssh-portable/blob/master/session.c
	c.Args = []string{"-" + filepath.Base(shell)}

	c.SysProcAttr = &syscall.SysProcAttr{}

	me, err := user.Current()
	if err != nil {
		return nil, err
	}

	if me.Uid != u.Uid || me.Gid != u.Gid {
		userGroups, err := u.GroupIds()
		if err != nil {
			return nil, err
		}

		groups := make([]uint32, 0)
		for _, sgid := range userGroups {
			igid, err := strconv.Atoi(sgid)
			if err != nil {
				s.log.Warn("unable to interpret user group", "group", sgid)
			} else {
				groups = append(groups, uint32(igid))
			}
		}

		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			return nil, err
		}

		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			return nil, err
		}

		if len(groups) == 0 {
			groups = append(groups, uint32(gid))
		}

		c.SysProcAttr.Credential = &syscall.Credential{
			Uid:    uint32(uid),
			Gid:    uint32(gid),
			Groups: groups,
		}

		c.SysProcAttr.Setsid = true
	}

	return c, nil
}

func ptyStart(c *exec.Cmd) (*os.File, error) {
	pty, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	c.Stdout = tty
	c.Stdin = tty
	c.Stderr = tty
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setctty = true
	c.SysProcAttr.Setsid = true
	c.Env = append(c.Env, "SSH_TTY="+tty.Name())
	err = c.Start()
	if err != nil {
		pty.Close()
		return nil, err
	}
	return pty, err
}

func (s *Server) shell(ctx context.Context, sess *ServerSession, req *msg.RequestShell) error {
	user := sess.user

	if s.config.LocalUser.Force != "" {
		user = s.config.LocalUser.Force
	}

	c, err := s.constructCommand(ctx, sess, user, req)
	if err != nil {
		return err
	}

	f, err := ptyStart(c)
	if err != nil {
		return err
	}

	sess.log.Info("shell started", "local_user", user)

	pty.Setsize(f, &pty.Winsize{
		Rows: uint16(req.Rows),
		Cols: uint16(req.Cols),
	})

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
			case msg.WINCH:
				pty.Setsize(f, &pty.Winsize{
					Rows: uint16(ctl.Rows),
					Cols: uint16(ctl.Cols),
				})
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
