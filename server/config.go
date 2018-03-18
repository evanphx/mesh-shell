package server

type Config struct {
	Id       string `yaml:"id"`
	Network  string `yaml:"network"`
	Verbose  string `yaml:"verbose"`
	SoloPort int    `yaml:"solo_port"`

	Auth struct {
		Github struct {
			AuthUser  string   `yaml:"auth_user"`
			AuthToken string   `yaml:"auth_token"`
			Org       string   `yaml:"org"`
			Teams     []string `yaml:"teams"`
		} `yaml:"github"`

		Local struct {
			AuthorizedKeys bool `yaml:"authorized_keys"`
		} `yaml:"local"`
	} `yaml:"auth"`

	LocalUser struct {
		Force string `yaml:"force"`
	} `yaml:"local_user"`
}
