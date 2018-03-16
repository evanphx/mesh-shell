package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/evanphx/mesh-shell/client"
)

var (
	fVerbose = flag.Bool("v", false, "set verbose output")
)

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("Usage: msh <network> <id>\n")
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
	err = c.Connect(ctx, os.Args[1], os.Args[2])
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
