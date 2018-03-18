package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/evanphx/mesh-shell/client"
)

var (
	fVerbose = flag.Bool("v", false, "set verbose output")
)

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
	flag.Parse()

	if len(flag.Args()) < 1 {
		fmt.Printf("Usage: msh [<user>@]<id>[%%<network>]\n")
		os.Exit(1)
	}

	dest, err := parseArg(flag.Arg(0))
	if err != nil {
		fmt.Printf("Unable to parse destination: %s\n", err)
		os.Exit(1)
	}

	c, err := client.NewClient(client.ClientOptions{
		Verbose: *fVerbose,
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

	err = c.StartShell(ctx)
	if err != nil {
		fmt.Printf("Error starting shell: %s\n", err)
		os.Exit(1)
	}
}
