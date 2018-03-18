package authorized_keys

import (
	"io/ioutil"
	"os/user"
	"path/filepath"

	"github.com/evanphx/mesh-shell/backend"
	"golang.org/x/crypto/ssh"
)

type Backend struct {
}

func (b *Backend) UserKeys(n string) (backend.Keys, error) {
	u, err := user.Lookup(n)
	if err != nil {
		return nil, err
	}

	path := filepath.Join(u.HomeDir, ".ssh", "authorized_keys")

	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var keys backend.Keys

	for len(data) > 0 {
		out, comment, options, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			return nil, err
		}

		keys = append(keys, &backend.Key{
			Key:     out,
			Comment: comment,
			Options: options,
		})

		data = rest
	}

	return keys, nil
}
