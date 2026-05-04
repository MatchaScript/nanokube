// Package config loads NanoKubeConfig from disk, applies defaults, and validates.
package config

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/nanokube/internal/paths"
)

// Load reads the config at path, applies defaults, and validates it.
// Returns the fully-populated config on success.
func Load(path string) (*v1alpha1.NanoKubeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parse(data, path)
}

// LoadDefault reads the canonical /etc/nanokube/config.yaml.
func LoadDefault() (*v1alpha1.NanoKubeConfig, error) {
	return Load(paths.ConfigFile)
}

func parse(data []byte, source string) (*v1alpha1.NanoKubeConfig, error) {
	c := &v1alpha1.NanoKubeConfig{}
	if err := yaml.UnmarshalStrict(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	v1alpha1.SetDefaults(c)
	if err := v1alpha1.Validate(c); err != nil {
		return nil, fmt.Errorf("validate %s: %w", source, err)
	}
	return c, nil
}

// Marshal serialises the config as YAML for display or persistence.
func Marshal(c *v1alpha1.NanoKubeConfig) ([]byte, error) {
	return yaml.Marshal(c)
}
