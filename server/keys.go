package server

import (
	"io/ioutil"
	"os/user"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

type Key struct {
	Key     ssh.PublicKey
	Comment string
	Options []string
}

type Keys []*Key

func KeysForUser(n string) (Keys, error) {
	u, err := user.Lookup(n)
	if err != nil {
		return nil, err
	}

	path := filepath.Join(u.HomeDir, ".ssh", "authorized_keys")

	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var keys Keys

	for len(data) > 0 {
		out, comment, options, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			return nil, err
		}

		keys = append(keys, &Key{
			Key:     out,
			Comment: comment,
			Options: options,
		})

		data = rest
	}

	return keys, nil
}
