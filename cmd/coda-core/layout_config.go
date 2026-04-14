package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type LayoutConfig struct {
	Direction string        `yaml:"direction"`
	Panes     []PaneConfig  `yaml:"panes"`
	Border    *BorderConfig `yaml:"border,omitempty"`
}

type PaneConfig struct {
	Command   string            `yaml:"command,omitempty"`
	Prefer    []string          `yaml:"prefer,omitempty"`
	Title     string            `yaml:"title,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Direction string            `yaml:"direction,omitempty"`
	Panes     []PaneConfig      `yaml:"panes,omitempty"`
	Size      string            `yaml:"size,omitempty"`
}

type BorderConfig struct {
	Status      string `yaml:"status,omitempty"`
	Lines       string `yaml:"lines,omitempty"`
	Style       string `yaml:"style,omitempty"`
	ActiveStyle string `yaml:"active_style,omitempty"`
	Format      string `yaml:"format,omitempty"`
}

func (p *PaneConfig) IsLeaf() bool {
	return len(p.Panes) == 0
}

func parseLayoutConfig(path string) (*LayoutConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading layout config: %w", err)
	}
	return parseLayoutConfigBytes(data)
}

func parseLayoutConfigBytes(data []byte) (*LayoutConfig, error) {
	var cfg LayoutConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing layout YAML: %w", err)
	}

	if err := validateLayoutConfig(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validateLayoutConfig(cfg *LayoutConfig) error {
	if len(cfg.Panes) == 0 {
		return fmt.Errorf("layout must have at least one pane")
	}

	dir := normalizeDirection(cfg.Direction)
	if dir == "" {
		return fmt.Errorf("layout direction must be 'horizontal' or 'vertical' (got %q)", cfg.Direction)
	}
	cfg.Direction = dir

	for i := range cfg.Panes {
		if err := validatePaneConfig(&cfg.Panes[i], fmt.Sprintf("panes[%d]", i)); err != nil {
			return err
		}
	}

	return nil
}

func validatePaneConfig(p *PaneConfig, path string) error {
	if p.IsLeaf() {
		if p.Command != "" && len(p.Prefer) > 0 {
			return fmt.Errorf("%s: cannot specify both 'command' and 'prefer'", path)
		}
		if p.Direction != "" {
			return fmt.Errorf("%s: leaf pane cannot have 'direction' without 'panes'", path)
		}
	} else {
		if p.Command != "" || len(p.Prefer) > 0 {
			return fmt.Errorf("%s: split pane cannot have 'command' or 'prefer'", path)
		}
		dir := normalizeDirection(p.Direction)
		if dir == "" {
			return fmt.Errorf("%s: split direction must be 'horizontal' or 'vertical' (got %q)", path, p.Direction)
		}
		p.Direction = dir

		for i := range p.Panes {
			if err := validatePaneConfig(&p.Panes[i], fmt.Sprintf("%s.panes[%d]", path, i)); err != nil {
				return err
			}
		}
	}

	if p.Size != "" {
		normalized, err := validateSize(p.Size)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		p.Size = normalized
	}

	for k := range p.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("%s: invalid env key %q (must match [A-Za-z_][A-Za-z0-9_]*)", path, k)
		}
	}

	return nil
}

func normalizeDirection(d string) string {
	switch strings.ToLower(d) {
	case "horizontal", "h":
		return "horizontal"
	case "vertical", "v":
		return "vertical"
	case "":
		return ""
	default:
		return ""
	}
}

func validateSize(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, nil
	}
	if strings.HasSuffix(s, "%") {
		numStr := strings.TrimSuffix(s, "%")
		pct, err := strconv.Atoi(numStr)
		if err != nil {
			return "", fmt.Errorf("invalid percentage size %q", s)
		}
		if pct < 1 || pct > 99 {
			return "", fmt.Errorf("percentage size must be between 1%% and 99%% (got %q)", s)
		}
		return fmt.Sprintf("%d%%", pct), nil
	}
	cells, err := strconv.Atoi(s)
	if err != nil {
		return "", fmt.Errorf("invalid size %q (use percentage like '80%%' or cell count like '20')", s)
	}
	if cells < 1 {
		return "", fmt.Errorf("cell size must be positive (got %q)", s)
	}
	return strconv.Itoa(cells), nil
}
