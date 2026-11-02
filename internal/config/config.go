// Package config: parser tập con YAML, thay ${VAR} và ${VAR:-mặc định} (trừ trong hooks),
// áp mặc định và kiểm tra cấu hình. Khóa lạ bị từ chối.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"cistern/internal/crypt"
	"cistern/internal/manifest"
)

const DefaultPath = "/etc/cistern/cistern.yaml"

// Path chọn file cấu hình: -c, rồi $CISTERN_CONFIG, rồi /etc/cistern/cistern.yaml.
func Path(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("CISTERN_CONFIG"); v != "" {
		return v
	}
	return DefaultPath
}

type Config struct {
	Repositories map[string]*Repository `yaml:"repositories"`
	Encryption   Encryption             `yaml:"encryption"`
	Compression  Compression            `yaml:"compression"`
	Metrics      Metrics                `yaml:"metrics"`
	Notify       Notify                 `yaml:"notify"`
	Scheduler    Scheduler              `yaml:"scheduler"`
	Defaults     Defaults               `yaml:"defaults"`
	Jobs         []*Job                 `yaml:"jobs"`

	// Điền khi nạp.
	Path       string             `yaml:"-"`
	Recipients []*crypt.Recipient `yaml:"-"`
}

type Repository struct {
	Type            string      `yaml:"type"` // local | s3
	Path            string      `yaml:"path"`
	Endpoint        string      `yaml:"endpoint"`
	Region          string      `yaml:"region"`
	Bucket          string      `yaml:"bucket"`
	Prefix          string      `yaml:"prefix"`
	AccessKeyID     string      `yaml:"access_key_id"`
	SecretAccessKey string      `yaml:"secret_access_key"`
	PathStyle       bool        `yaml:"path_style"`
	PartSizeMB      int         `yaml:"part_size_mb"`
	CAFile          string      `yaml:"ca_file"`
	InsecureSkip    bool        `yaml:"insecure_skip_verify"`
	ObjectLock      *ObjectLock `yaml:"object_lock"`
}

type ObjectLock struct {
	Mode string `yaml:"mode"` // GOVERNANCE | COMPLIANCE
	Days int    `yaml:"days"`
}

type Encryption struct {
	Recipients     []string `yaml:"recipients"`
	RecipientsFile string   `yaml:"recipients_file"`
	IdentityFile   string   `yaml:"identity_file"`
	AllowPlaintext bool     `yaml:"allow_plaintext"`
}

type Compression struct {
	Level *int `yaml:"level"` // gzip 1–9; 0 = không nén; mặc định 6
}

type Metrics struct {
	Listen         string `yaml:"listen"`
	PushgatewayURL string `yaml:"pushgateway_url"`
	TextfileDir    string `yaml:"textfile_dir"`
}

type Notify struct {
	Webhooks []*Webhook `yaml:"webhooks"`
}

type Webhook struct {
	URL    string   `yaml:"url"`
	Format string   `yaml:"format"` // slack | teams | telegram | json
	ChatID string   `yaml:"chat_id"`
	On     []string `yaml:"on"` // failure | success
}

type Scheduler struct {
	Timezone string `yaml:"timezone"`
}

type Retention struct {
	Last    int `yaml:"last"`
	Hourly  int `yaml:"hourly"`
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
	Yearly  int `yaml:"yearly"`
}

type Defaults struct {
	Repository       string        `yaml:"repository"`
	Timeout          time.Duration `yaml:"timeout"`
	Retention        *Retention    `yaml:"retention"`
	PruneAfterBackup bool          `yaml:"prune_after_backup"`
}

type Hooks struct {
	PreBackup   string `yaml:"pre_backup"`
	PostBackup  string `yaml:"post_backup"`
	PreRestore  string `yaml:"pre_restore"`
	PostRestore string `yaml:"post_restore"`
}

type Job struct {
	Name             string            `yaml:"name"`
	Driver           string            `yaml:"driver"`
	Repository       string            `yaml:"repository"`
	Schedule         string            `yaml:"schedule"`
	VerifySchedule   string            `yaml:"verify_schedule"`
	Timeout          time.Duration     `yaml:"timeout"`
	Retention        *Retention        `yaml:"retention"`
	PruneAfterBackup *bool             `yaml:"prune_after_backup"`
	Hooks            Hooks             `yaml:"hooks"`
	Params           map[string]string `yaml:"params"`
}

func (r *Retention) validate() error {
	if r == nil {
		return nil
	}
	for _, v := range []int{r.Last, r.Hourly, r.Daily, r.Weekly, r.Monthly, r.Yearly} {
		if v < 0 {
			return errors.New("counts must not be negative")
		}
	}
	if r.Last+r.Hourly+r.Daily+r.Weekly+r.Monthly+r.Yearly == 0 {
		return errors.New("policy keeps no snapshots; remove it to disable pruning")
	}
	return nil
}

// Location là múi giờ dùng cho lịch và retention (mặc định giờ máy).
func (c *Config) Location() *time.Location {
	if c.Scheduler.Timezone == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(c.Scheduler.Timezone)
	if err != nil {
		return time.Local // đã kiểm khi nạp
	}
	return loc
}

// Job tìm job theo tên.
func (c *Config) Job(name string) (*Job, error) {
	for _, j := range c.Jobs {
		if j.Name == name {
			return j, nil
		}
	}
	return nil, fmt.Errorf("job %q not found in %s", name, c.Path)
}

// CompressionLevel trả về mức gzip; 0 nghĩa là không nén.
func (c *Config) CompressionLevel() int { return *c.Compression.Level }

// Resolve trả về đường dẫn tương đối theo thư mục của file cấu hình.
func (c *Config) Resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(c.Path), p)
}

// Load đọc, thay biến môi trường, giải mã và kiểm tra cấu hình.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data, osLookup)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	if err := cfg.loadRecipients(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse giải mã và kiểm tra cấu hình (không đọc file recipient).
func Parse(data []byte, lookup func(string) (string, bool)) (*Config, error) {
	root, err := parseYAML(string(data))
	if err != nil {
		return nil, err
	}
	if root.kind != mappingNode {
		return nil, errors.New("top level must be a mapping")
	}
	if err := expandEnv(root, lookup); err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := decode(root, reflect.ValueOf(cfg).Elem(), ""); err != nil {
		return nil, err
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if len(c.Repositories) == 0 {
		add("repositories: at least one repository is required")
	}
	names := make([]string, 0, len(c.Repositories))
	for name := range c.Repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := c.Repositories[name]
		if r == nil {
			add("repositories.%s: empty", name)
			continue
		}
		switch r.Type {
		case "local":
			if r.Path == "" {
				add("repositories.%s.path: required for local repository", name)
			}
		case "s3":
			if r.Endpoint == "" || r.Bucket == "" {
				add("repositories.%s: endpoint and bucket are required for s3 repository", name)
			}
			if r.PartSizeMB == 0 {
				r.PartSizeMB = 16
			}
			if r.PartSizeMB < 5 {
				add("repositories.%s.part_size_mb: minimum is 5", name)
			}
		default:
			add("repositories.%s.type: must be local or s3, got %q", name, r.Type)
		}
	}

	if c.Compression.Level == nil {
		six := 6
		c.Compression.Level = &six
	} else if l := *c.Compression.Level; l < 0 || l > 9 {
		add("compression.level: must be 0–9, got %d", l)
	}
	if c.Metrics.Listen == "" {
		c.Metrics.Listen = ":9464"
	}
	if c.Scheduler.Timezone != "" {
		if _, err := time.LoadLocation(c.Scheduler.Timezone); err != nil {
			add("scheduler.timezone: %v", err)
		}
	}
	for i, w := range c.Notify.Webhooks {
		if w.URL == "" {
			add("notify.webhooks[%d].url: required", i)
		}
		switch w.Format {
		case "":
			w.Format = "json"
		case "slack", "teams", "telegram", "json":
		default:
			add("notify.webhooks[%d].format: unknown format %q", i, w.Format)
		}
		if w.Format == "telegram" && w.ChatID == "" {
			add("notify.webhooks[%d].chat_id: required for telegram", i)
		}
		if len(w.On) == 0 {
			w.On = []string{"failure"}
		}
		for _, on := range w.On {
			if on != "failure" && on != "success" {
				add("notify.webhooks[%d].on: must be failure or success, got %q", i, on)
			}
		}
	}
	if err := c.Defaults.Retention.validate(); err != nil {
		add("defaults.retention: %v", err)
	}

	if c.Defaults.Timeout == 0 {
		c.Defaults.Timeout = 6 * time.Hour
	}
	if c.Defaults.Repository == "" && len(c.Repositories) == 1 {
		c.Defaults.Repository = names[0]
	}
	if c.Defaults.Repository != "" && c.Repositories[c.Defaults.Repository] == nil {
		add("defaults.repository: unknown repository %q", c.Defaults.Repository)
	}

	seen := map[string]bool{}
	for i, j := range c.Jobs {
		p := fmt.Sprintf("jobs[%d]", i)
		if j == nil {
			add("%s: empty", p)
			continue
		}
		if err := manifest.ValidateJobName(j.Name); err != nil {
			add("%s.name: %v", p, err)
		} else if seen[j.Name] {
			add("%s.name: duplicate job %q", p, j.Name)
		}
		seen[j.Name] = true
		if j.Driver == "" {
			add("%s.driver: required", p)
		}
		if j.Repository == "" {
			j.Repository = c.Defaults.Repository
		}
		if j.Repository == "" {
			add("%s.repository: required (or set defaults.repository)", p)
		} else if c.Repositories[j.Repository] == nil {
			add("%s.repository: unknown repository %q", p, j.Repository)
		}
		if j.Timeout == 0 {
			j.Timeout = c.Defaults.Timeout
		}
		if err := j.Retention.validate(); err != nil {
			add("%s.retention: %v", p, err)
		}
		if j.Retention == nil {
			j.Retention = c.Defaults.Retention
		}
		if j.PruneAfterBackup == nil {
			v := c.Defaults.PruneAfterBackup
			j.PruneAfterBackup = &v
		}
		if j.Params == nil {
			j.Params = map[string]string{}
		}
	}
	return errors.Join(errs...)
}

func (c *Config) loadRecipients() error {
	for _, s := range c.Encryption.Recipients {
		r, err := crypt.ParseRecipient(s)
		if err != nil {
			return fmt.Errorf("encryption.recipients: %w", err)
		}
		c.Recipients = append(c.Recipients, r)
	}
	if f := c.Encryption.RecipientsFile; f != "" {
		fh, err := os.Open(c.Resolve(f))
		if err != nil {
			return fmt.Errorf("encryption.recipients_file: %w", err)
		}
		defer fh.Close()
		rs, err := crypt.ParseRecipients(fh)
		if err != nil {
			return fmt.Errorf("encryption.recipients_file: %w", err)
		}
		c.Recipients = append(c.Recipients, rs...)
	}
	if len(c.Recipients) == 0 && !c.Encryption.AllowPlaintext {
		return errors.New("encryption: no recipients configured (set allow_plaintext: true to store backups unencrypted)")
	}
	return nil
}

// LoadIdentities đọc private key từ encryption.identity_file (chỉ cần cho verify đầy đủ và restore).
func (c *Config) LoadIdentities() ([]*crypt.Identity, error) {
	f := c.Encryption.IdentityFile
	if f == "" {
		return nil, nil
	}
	fh, err := os.Open(c.Resolve(f))
	if err != nil {
		return nil, fmt.Errorf("encryption.identity_file: %w", err)
	}
	defer fh.Close()
	ids, err := crypt.ParseIdentities(fh)
	if err != nil {
		return nil, fmt.Errorf("encryption.identity_file: %w", err)
	}
	return ids, nil
}
