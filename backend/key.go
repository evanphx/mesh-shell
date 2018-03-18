package backend

import "golang.org/x/crypto/ssh"

type Key struct {
	Key     ssh.PublicKey
	Comment string
	Options []string
}

type Keys []*Key
