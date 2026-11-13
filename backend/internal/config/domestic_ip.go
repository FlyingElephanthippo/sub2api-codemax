package config

// DomesticIPConfig enables domestic-origin grants only after a versioned policy
// has been installed. The policy file contains operational IP data and must not
// be committed to the source repository.
type DomesticIPConfig struct {
	Enabled         bool     `mapstructure:"enabled"`
	SnapshotFile    string   `mapstructure:"snapshot_file"`
	SnapshotSHA256  string   `mapstructure:"snapshot_sha256"`
	TrustedProxyIPs []string `mapstructure:"trusted_proxy_ips"`
}
