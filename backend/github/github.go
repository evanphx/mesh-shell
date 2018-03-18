package github

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	"github.com/evanphx/mesh-shell/backend"
	"golang.org/x/crypto/ssh"
)

type Backend struct {
	GithubUser      string
	GithubToken     string
	GithubOrg       string
	GroupMembership []string

	mu         sync.Mutex
	teams      []githubTeam
	lastUpdate time.Time
}

type githubTeam struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

var teamCacheTime = time.Minute

func (b *Backend) UserKeys(user string) (backend.Keys, error) {
	b.mu.Lock()

	if b.lastUpdate.IsZero() || time.Since(b.lastUpdate) > teamCacheTime {
		req, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/orgs/%s/teams", b.GithubOrg), nil)
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth(b.GithubUser, b.GithubToken)

		req.Header.Set("Accept", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		defer resp.Body.Close()

		var teams []githubTeam

		err = json.NewDecoder(resp.Body).Decode(&teams)
		if err != nil {
			return nil, err
		}

		b.teams = teams
		b.lastUpdate = time.Now()
	}

	b.mu.Unlock()

	for _, team := range b.teams {
		for _, authTeam := range b.GroupMembership {
			if team.Name == authTeam {
				ok, err := b.checkMembership(team.ID, user)
				if err != nil {
					return nil, err
				}

				if ok {
					return b.getUserKeys(user)
				}
			}
		}
	}

	return nil, nil
}

type membership struct {
	State string `json:"state"`
}

func (b *Backend) checkMembership(team int, user string) (bool, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/teams/%d/memberships/%s", team, user), nil)
	if err != nil {
		return false, err
	}

	req.SetBasicAuth(b.GithubUser, b.GithubToken)

	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false, nil
	}

	var mem membership

	err = json.NewDecoder(resp.Body).Decode(&mem)
	if err != nil {
		return false, err
	}

	return mem.State == "active", nil
}

func (b *Backend) getUserKeys(user string) (backend.Keys, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://github.com/%s.keys", user), nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, nil
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var keys backend.Keys

	for len(data) > 0 {
		key, comment, options, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			return nil, err
		}

		if comment == "" {
			comment = fmt.Sprintf("%s@github", user)
		} else {
			comment = fmt.Sprintf("%s, %s@github", comment, user)
		}

		keys = append(keys, &backend.Key{
			Key:     key,
			Comment: comment,
			Options: options,
		})

		data = rest
	}

	return keys, nil
}
