package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ManifestFile  = "theme.manifest"
	SchemaVersion = 1
)

type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	Files         []File    `json:"files"`
}

type File struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Module      string `json:"module"`
	StoredPath  string `json:"-"`
}

func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid theme name %q: use a single directory name", name)
	}
	if name == ManifestFile || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid theme name %q: reserved names are not allowed", name)
	}
	return nil
}

func Validate(m Manifest) error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported manifest schema version %d", m.SchemaVersion)
	}
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	if m.CreatedAt.IsZero() {
		return errors.New("manifest is missing created_at")
	}
	if len(m.Files) == 0 {
		return errors.New("manifest has no files")
	}

	sources, destinations := map[string]bool{}, map[string]bool{}
	for i, f := range m.Files {
		if err := ValidateSource(f.Source); err != nil {
			return fmt.Errorf("file %d source: %w", i+1, err)
		}
		if f.Module == "" {
			return fmt.Errorf("file %d is missing module", i+1)
		}
		if _, err := ExpandDestination(f.Destination); err != nil {
			return fmt.Errorf("file %d destination: %w", i+1, err)
		}
		if sources[filepath.Clean(f.Source)] {
			return fmt.Errorf("duplicate stored source %q", f.Source)
		}
		sources[filepath.Clean(f.Source)] = true

		dst, _ := ExpandDestination(f.Destination)
		if destinations[dst] {
			return fmt.Errorf("duplicate destination %q", f.Destination)
		}
		destinations[dst] = true
	}
	return nil
}

func ValidateSource(source string) error {
	if source == "" || filepath.IsAbs(source) || strings.ContainsRune(source, 0) {
		return fmt.Errorf("must be a non-empty relative path")
	}
	clean := filepath.Clean(filepath.FromSlash(source))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the theme directory")
	}
	return nil
}

func ExpandDestination(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("destination is empty or invalid")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	} else if !filepath.IsAbs(path) {
		return "", errors.New("destination must be absolute or start with ~/ ")
	}
	return filepath.Clean(path), nil
}

// InspectSource resolves a user source path without accepting symlinks in any component.
func InspectSource(source string) (string, os.FileInfo, error) {
	path, err := filepath.Abs(source)
	if err != nil {
		return "", nil, err
	}

	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	current := volume + string(filepath.Separator)
	for part := range strings.SplitSeq(strings.TrimLeft(rest, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("source path contains a symlink: %s", current)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}

	return path, info, nil
}

func ResolveSource(themeDir, source string) (string, error) {
	if err := ValidateSource(source); err != nil {
		return "", err
	}
	root, err := filepath.Abs(themeDir)
	if err != nil {
		return "", err
	}

	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}

	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", errors.New("theme directory must be a real directory")
	}
	path := filepath.Join(root, filepath.FromSlash(source))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("stored source escapes the theme directory")
	}
	// Reject symlink components so a manifest cannot redirect reads outside its theme.
	cur := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("stored source contains a symlink: %s", cur)
		}
	}
	return path, nil
}

func ReadManifest(themeDir string) (Manifest, error) {
	manifestPath := filepath.Join(themeDir, ManifestFile)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return Manifest{}, err
	}

	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return Manifest{}, errors.New("theme.manifest must be a regular file")
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}

	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}

	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Manifest{}, errors.New("manifest contains trailing data")
	}
	if err := Validate(m); err != nil {
		return Manifest{}, err
	}
	for _, entry := range m.Files {
		p, err := ResolveSource(themeDir, entry.Source)
		if err != nil {
			return Manifest{}, fmt.Errorf("validate source %q: %w", entry.Source, err)
		}

		info, err := os.Stat(p)
		if err != nil {
			return Manifest{}, fmt.Errorf("validate source %q: %w", entry.Source, err)
		}
		if !info.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("stored source %q is not a regular file", entry.Source)
		}
	}
	return m, nil
}
