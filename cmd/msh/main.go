package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/evanphx/mesh-shell/client"
	"github.com/jessevdk/go-flags"
)

type opts struct {
	Verbose      bool     `short:"v" description:"set verbose output"`
	ForceTTY     bool     `short:"t" description:"force pseudo-terminal allocation"`
	ForwardAgent bool     `short:"A" description:"forward ssh-agent"`
	DisableAgent bool     `short:"a" description:"disables forwarding of ssh-agent"`
	Env          []string `short:"e" description:"environment variables to set remotely"`
}

type destination struct {
	user    string
	network string
	id      string
	port    int
}

func parseArg(arg string) (destination, error) {
	var dest destination

	if idx := strings.IndexByte(arg, '@'); idx != -1 {
		dest.user = arg[:idx]
		arg = arg[idx+1:]
	} else {
		cur, err := user.Current()
		if err != nil {
			return dest, err
		}

		dest.user = cur.Username
	}

	if idx := strings.IndexByte(arg, '%'); idx != -1 {
		dest.id = arg[:idx]
		dest.network = arg[idx+1:]
	} else {
		dest.id = arg
		dest.port = 8222
	}

	return dest, nil
}

func main() {
	var o opts

	parser := flags.NewNamedParser("msh", flags.Default|flags.PassAfterNonOption)
	parser.AddGroup("Options", "", &o)

	args, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}

	if len(args) < 1 {
		fmt.Printf("Usage: msh [<user>@]<id>[%%<network>]\n")
		os.Exit(1)
	}

	dest, err := parseArg(args[0])
	if err != nil {
		fmt.Printf("Unable to parse destination: %s\n", err)
		os.Exit(1)
	}

	c, err := client.NewClient(client.ClientOptions{
		Verbose: o.Verbose,
	})

	if err != nil {
		fmt.Printf("Error creating client: %s\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	if dest.port != 0 {
		err = c.ConnectSolo(ctx, dest.user, net.JoinHostPort(dest.id, strconv.Itoa(dest.port)))
	} else {
		err = c.Connect(ctx, dest.network, dest.id, dest.user)
	}

	if err != nil {
		fmt.Printf("Error connecting: %s\n", err)
		os.Exit(1)
	}

	if !o.ForwardAgent && !o.DisableAgent {
		o.ForwardAgent = true
	}

	opts := client.StartOptions{
		ForwardAgent: o.ForwardAgent,
		PTY:          o.ForceTTY,
		Env:          o.Env,
	}

	if len(args) > 1 {
		opts.Command = args[1]
		opts.Args = args[2:]
	}

	err = c.Start(ctx, opts)

	if err != nil {
		fmt.Printf("Error starting shell: %s\n", err)
		os.Exit(1)
	}
}
