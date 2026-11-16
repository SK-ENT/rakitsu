package config

import (
	"fmt"
	"io/fs"
	"strings"
)

type LocalRetrievalConfig struct {
	Directory string `mapstructure:"directory" yaml:"directory"`
	TopK      int    `mapstructure:"top_k,omitempty" yaml:"top_k,omitempty"`
}

func (r *LocalRetrievalConfig) Validate() error {
	if r == nil || !fs.ValidPath(r.Directory) || strings.Contains(r.Directory, "\\") {
		return fmt.Errorf("retrieval directory must be a relative path under RAKITSU_RETRIEVAL_ROOT")
	}
	if r.TopK < 0 || r.TopK > 8 {
		return fmt.Errorf("retrieval top_k must be between 1 and 8 (default 3)")
	}
	return nil
}
