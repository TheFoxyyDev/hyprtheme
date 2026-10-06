package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hyprtheme/internal/theme"
)

type Store struct{ ThemesDir string }

func Default() (Store, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return Store{ThemesDir: filepath.Join(base, "hyprtheme", "themes")}, nil
}

func (s Store) List() ([]theme.Manifest, error) {
	entries, err := os.ReadDir(s.ThemesDir)
	if errors.Is(err, os.ErrNotExist) {
		return []theme.Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}

	result := make([]theme.Manifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := theme.ReadManifest(filepath.Join(s.ThemesDir, entry.Name()))
		if err != nil {
			continue
		}

		if m.Name != entry.Name() {
			continue
		}
		result = append(result, m)
	}
	return result, nil
}

// Save copies the selected source files into a staged, self-contained theme directory.
func (s Store) Save(name string, files []theme.File) error {
	if err := theme.ValidateName(name); err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("select at least one config file")
	}
	if err := os.MkdirAll(s.ThemesDir, 0o755); err != nil {
		return err
	}

	final := filepath.Join(s.ThemesDir, name)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("theme %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	stage, err := os.MkdirTemp(s.ThemesDir, ".hyprtheme-stage-")
	if err != nil {
		return err
	}

	defer os.RemoveAll(stage)
	m := theme.Manifest{SchemaVersion: theme.SchemaVersion, Name: name, CreatedAt: time.Now().UTC(), Files: make([]theme.File, 0, len(files))}
	seenDest, seenSource := map[string]bool{}, map[string]bool{}
	for _, entry := range files {
		if entry.Module == "" {
			return errors.New("module name cannot be empty")
		}
		src, info, err := theme.InspectSource(entry.Source)
		if err != nil {
			return fmt.Errorf("source %q: %w", entry.Source, err)
		}

		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("source %q must be a regular file, not a symlink", entry.Source)
		}

		dst, err := theme.ExpandDestination(entry.Destination)
		if err != nil {
			return err
		}

		if seenDest[dst] {
			return fmt.Errorf("multiple selected files target %q", dst)
		}
		seenDest[dst] = true
		if seenSource[src] {
			return fmt.Errorf("source %q was selected more than once", src)
		}
		seenSource[src] = true
		stored := entry.StoredPath
		if stored == "" {
			stored = filepath.ToSlash(filepath.Join("files", entry.Module, filepath.Base(src)))
		}
		if err := theme.ValidateSource(stored); err != nil {
			return err
		}

		stored = filepath.ToSlash(filepath.Clean(filepath.FromSlash(stored)))
		for _, old := range m.Files {
			if old.Source == stored {
				return fmt.Errorf("stored path collision at %q", stored)
			}
		}
		if err := copyRegular(src, filepath.Join(stage, filepath.FromSlash(stored)), info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy %q: %w", src, err)
		}
		entry.Source = stored
		entry.Destination = dst
		m.Files = append(m.Files, entry)
	}
	if err := theme.Validate(m); err != nil {
		return err
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(stage, theme.ManifestFile)
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(stage, final); err != nil {
		return fmt.Errorf("install theme: %w", err)
	}
	return nil
}

func copyRegular(source, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}

	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return errors.New("source changed and is no longer a regular file")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// SafeThemePath returns a managed path after validating the supplied name.
func (s Store) SafeThemePath(name string) (string, error) {
	if err := theme.ValidateName(name); err != nil {
		return "", err
	}
	root, err := filepath.Abs(s.ThemesDir)
	if err != nil {
		return "", err
	}

	path := filepath.Join(root, name)
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("theme path escapes managed directory")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("managed theme path is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}
