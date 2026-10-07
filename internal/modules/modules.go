package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"hyprtheme/internal/theme"
)

type Definition struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func Defaults() []Definition {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}

	hyprConfig := filepath.Join(config, "hypr")
	return []Definition{
		{Name: "hyprland", Label: "Hyprland (.conf)", Source: filepath.Join(hyprConfig, "hyprland.conf"), Destination: filepath.Join(hyprConfig, "hyprland.conf")},
		{Name: "hyprland-lua", Label: "Hyprland (.lua)", Source: filepath.Join(hyprConfig, "hyprland.lua"), Destination: filepath.Join(hyprConfig, "hyprland.lua")},
		{Name: "hyprpaper", Label: "Hyprpaper", Source: filepath.Join(hyprConfig, "hyprpaper.conf"), Destination: filepath.Join(hyprConfig, "hyprpaper.conf")},
		{Name: "hyprlock", Label: "Hyprlock", Source: filepath.Join(hyprConfig, "hyprlock.conf"), Destination: filepath.Join(hyprConfig, "hyprlock.conf")},
		{Name: "waybar", Label: "Waybar", Source: filepath.Join(config, "waybar"), Destination: filepath.Join(config, "waybar")},
		{Name: "swaync", Label: "SwayNC", Source: filepath.Join(config, "swaync"), Destination: filepath.Join(config, "swaync")},
		{Name: "kitty", Label: "Kitty", Source: filepath.Join(config, "kitty"), Destination: filepath.Join(config, "kitty")},
		{Name: "wlogout", Label: "Wlogout", Source: filepath.Join(config, "wlogout"), Destination: filepath.Join(config, "wlogout")},
	}
}

// Available reports whether a module source is a regular file or directory.
func Available(d Definition) bool {
	info, err := os.Lstat(d.Source)
	return err == nil && (info.Mode().IsRegular() || info.IsDir())
}

// Expand converts file and directory modules into manifest entries, preserving paths
// relative to a directory source beneath files/<module>/ in the stored theme.
func Expand(definitions []Definition) ([]theme.File, error) {
	result := []theme.File{}
	for _, d := range definitions {
		source, err := theme.ExpandDestination(d.Source)
		if err != nil {
			return nil, fmt.Errorf("module %q source: %w", d.Name, err)
		}

		destination, err := theme.ExpandDestination(d.Destination)
		if err != nil {
			return nil, fmt.Errorf("module %q destination: %w", d.Name, err)
		}

		source, rootInfo, err := theme.InspectSource(source)
		if err != nil {
			return nil, fmt.Errorf("module %q source %q: %w", d.Name, source, err)
		}

		if rootInfo.Mode()&os.ModeSymlink != 0 || (!rootInfo.Mode().IsRegular() && !rootInfo.IsDir()) {
			return nil, fmt.Errorf("module %q source must be a regular file or directory", d.Name)
		}
		if rootInfo.IsDir() {
			before := len(result)
			err = filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}

				info, err := os.Lstat(current)
				if err != nil {
					return err
				}

				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("module %q contains symlink %q", d.Name, current)
				}
				if info.IsDir() {
					return nil
				}

				if !info.Mode().IsRegular() {
					return fmt.Errorf("module %q contains non-regular file %q", d.Name, current)
				}
				rel, err := filepath.Rel(source, current)
				if err != nil {
					return err
				}

				result = append(result, theme.File{Module: d.Name, Source: current, Destination: filepath.Join(destination, rel), StoredPath: path.Join("files", d.Name, filepath.ToSlash(rel))})
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("scan module %q: %w", d.Name, err)
			}
			if len(result) == before {
				return nil, fmt.Errorf("module %q directory contains no regular files", d.Name)
			}
		} else {
			result = append(result, theme.File{Module: d.Name, Source: source, Destination: destination, StoredPath: path.Join("files", d.Name, filepath.Base(source))})
		}
	}
	return result, nil
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func ConfigPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hyprtheme", "modules.json"), nil
}

// Load returns configured modules, or the built-in defaults before a config is created.
func Load() ([]Definition, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return nil, err
	}

	var result []Definition
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("read module config: %w", err)
	}
	if err := validate(result); err != nil {
		return nil, fmt.Errorf("invalid module config: %w", err)
	}

	for i := range result {
		result[i].Source, err = theme.ExpandDestination(result[i].Source)
		if err != nil {
			return nil, err
		}
		result[i].Destination, err = theme.ExpandDestination(result[i].Destination)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func Save(definitions []Definition) error {
	if err := validate(definitions); err != nil {
		return err
	}
	configPath, err := ConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(definitions, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".modules-*.tmp")
	if err != nil {
		return err
	}

	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), configPath)
}

func validate(definitions []Definition) error {
	if len(definitions) > 128 {
		return errors.New("at most 128 modules can be configured")
	}
	seen := map[string]bool{}
	for i, d := range definitions {
		if !validName.MatchString(d.Name) {
			return fmt.Errorf("module %d has invalid name %q", i+1, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("duplicate module name %q", d.Name)
		}

		seen[d.Name] = true
		if strings.TrimSpace(d.Label) == "" {
			return fmt.Errorf("module %q needs a label", d.Name)
		}

		if _, err := theme.ExpandDestination(d.Source); err != nil {
			return fmt.Errorf("module %q source: %w", d.Name, err)
		}
		if _, err := theme.ExpandDestination(d.Destination); err != nil {
			return fmt.Errorf("module %q destination: %w", d.Name, err)
		}
	}
	return nil
}
