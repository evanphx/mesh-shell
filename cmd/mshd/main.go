package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/evanphx/mesh-shell/server"
	"github.com/evanphx/mesh/instance"
)

var (
	fId      = flag.String("id", "", "identify to advertise (default to hostname)")
	fNetwork = flag.String("network", "", "network to advertise on")
)

func main() {
	flag.Parse()

	id := *fId

	if id == "" {
		name, err := os.Hostname()
		if err != nil {
			log.Fatal(err)
		}

		id = name
	}

	network := *fNetwork
	if network == "" {
		cfg := instance.LoadConfig()
		network = cfg.DefaultNetwork
		if network == "" {
			log.Fatalln("No network specified and no default network configured")
		}
	}

	serv, err := server.NewServer(id, network)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Advertised as %s@%s\n", id, network)

	ctx := context.Background()

	err = serv.Accept(ctx)
	if err != nil {
		log.Fatal(err)
	}
}
