package evergreen

import (
	"github.com/evergreen-ci/evergreen/db"
	"github.com/mongodb/grip"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2/bson"
)

// UIConfig holds relevant settings for the UI server.
type UIConfig struct {
	Url            string `bson:"url" json:"url" yaml:"url"`
	HelpUrl        string `bson:"help_url" json:"help_url" yaml:"helpurl"`
	HttpListenAddr string `bson:"http_listen_addr" json:"http_listen_addr" yaml:"httplistenaddr"`
	// Secret to encrypt session storage
	Secret string `bson:"secret" json:"secret" yaml:"secret"`
	// Default project to assume when none specified, e.g. when using
	// the /waterfall route use this project, while /waterfall/other-project
	// then use `other-project`
	DefaultProject string `bson:"default_project" json:"default_project" yaml:"defaultproject"`
	// Cache results of template compilation, so you don't have to re-read files
	// on every request. Note that if this is true, changes to HTML templates
	// won't take effect until server restart.
	CacheTemplates bool `bson:"cache_templates" json:"cache_templates" yaml:"cachetemplates"`
	// SecureCookies sets the "secure" flag on user tokens. Evergreen
	// does not yet natively support SSL UI connections, but this option
	// is available, for example, for deployments behind HTTPS load balancers.
	SecureCookies bool `bson:"secure_cookies" json:"secure_cookies" yaml:"securecookies"`
	// CsrfKey is a 32-byte key used to generate tokens that validate UI requests
	CsrfKey string `bson:"csrf_key" json:"csrf_key" yaml:"csrfkey"`
}

func (c *UIConfig) SectionId() string { return "ui" }

func (c *UIConfig) Get() error {
	err := db.FindOneQ(ConfigCollection, db.Query(byId(c.SectionId())), c)
	if err != nil && err.Error() == errNotFound {
		*c = UIConfig{}
		return nil
	}
	return errors.Wrapf(err, "error retrieving section %s", c.SectionId())
}

func (c *UIConfig) Set() error {
	_, err := db.Upsert(ConfigCollection, byId(c.SectionId()), bson.M{
		"$set": bson.M{
			"url":              c.Url,
			"help_url":         c.HelpUrl,
			"http_listen_addr": c.HttpListenAddr,
			"secret":           c.Secret,
			"default_project":  c.DefaultProject,
			"cache_templates":  c.CacheTemplates,
			"secure_cookies":   c.SecureCookies,
			"csrf_key":         c.CsrfKey,
		},
	})
	return errors.Wrapf(err, "error updating section %s", c.SectionId())
}

func (c *UIConfig) ValidateAndDefault() error {
	catcher := grip.NewSimpleCatcher()
	if c.Secret == "" {
		catcher.Add(errors.New("UI Secret must not be empty"))
	}
	if c.DefaultProject == "" {
		catcher.Add(errors.New("You must specify a default project in UI"))
	}
	if c.Url == "" {
		catcher.Add(errors.New("You must specify a default UI url"))
	}
	if c.CsrfKey != "" && len(c.CsrfKey) != 32 {
		catcher.Add(errors.New("CSRF key must be 32 characters long"))
	}
	return catcher.Resolve()
}
