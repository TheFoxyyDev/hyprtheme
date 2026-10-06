package archive

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"hyprtheme/internal/store"
	"hyprtheme/internal/theme"
)

const (
	maxArchiveBytes  = 256 << 20
	maxExpandedBytes = 256 << 20
	maxFileBytes     = 64 << 20
	maxEntries       = 1024
	maxManifestBytes = 1 << 20
)

// Export writes a self-contained ZIP archive without replacing an existing path.
func Export(themeDir, output string) error {
	m, err := theme.ReadManifest(themeDir)
	if err != nil {
		return err
	}

	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}

	if filepath.Ext(output) != ".theme" {
		return errors.New("export path must end in .theme")
	}
	if _, err := os.Lstat(output); err == nil {
		return fmt.Errorf("archive %q already exists", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(output), ".hyprtheme-export-")
	if err != nil {
		return err
	}

	tempName := tmp.Name()
	defer os.Remove(tempName)

	zw := zip.NewWriter(tmp)
	paths := append([]string{theme.ManifestFile}, sources(m)...)

	for _, name := range paths {
		full := filepath.Join(themeDir, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if err != nil {
			zw.Close()
			tmp.Close()
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			zw.Close()
			tmp.Close()
			return fmt.Errorf("refusing to archive non-regular file %q", name)
		}

		in, err := os.Open(full)
		if err != nil {
			zw.Close()
			tmp.Close()
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err == nil {
			header.Name = filepath.ToSlash(name)
			header.Method = zip.Deflate
		}

		var out io.Writer
		if err == nil {
			out, err = zw.CreateHeader(header)
		}
		if err == nil {
			_, err = io.Copy(out, in)
		}

		closeErr := in.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			zw.Close()
			tmp.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
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
	// Link is atomic and fails if another process created the target meanwhile.
	if err := os.Link(tempName, output); err != nil {
		return fmt.Errorf("create archive %q: %w", output, err)
	}
	return nil
}

// Import validates every archive entry and the manifest before writing into the managed store.
func Import(s store.Store, input string) (string, error) {
	return ImportAs(s, input, "")
}

// ImportAs imports using an optional replacement theme name, which can resolve collisions.
func ImportAs(s store.Store, input, name string) (string, error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", err
	}

	if !info.Mode().IsRegular() || info.Size() > maxArchiveBytes {
		return "", errors.New("archive is not a regular file or exceeds the size limit")
	}

	zr, err := zip.OpenReader(input)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	if len(zr.File) == 0 || len(zr.File) > maxEntries {
		return "", errors.New("archive entry count is invalid")
	}

	files := map[string]*zip.File{}
	dirs := map[string]bool{}

	var expanded uint64
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
			return "", fmt.Errorf("unsafe archive path %q", f.Name)
		}

		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != strings.TrimSuffix(name, "/") {
			return "", fmt.Errorf("unsafe archive path %q", f.Name)
		}

		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return "", fmt.Errorf("archive entry %q is a special file", name)
		}
		if mode.IsDir() || strings.HasSuffix(name, "/") {
			if files[clean] != nil || dirs[clean] {
				return "", fmt.Errorf("duplicate or conflicting entry %q", name)
			}
			dirs[clean] = true
			continue
		}

		if files[clean] != nil || dirs[clean] {
			return "", fmt.Errorf("duplicate or conflicting entry %q", name)
		}
		if f.UncompressedSize64 > maxFileBytes {
			return "", fmt.Errorf("archive file %q exceeds the size limit", name)
		}
		expanded += f.UncompressedSize64
		if expanded > maxExpandedBytes {
			return "", errors.New("archive expanded size exceeds the limit")
		}
		files[clean] = f
	}
	for name := range files {
		for parent := filepath.Dir(filepath.FromSlash(name)); parent != "."; parent = filepath.Dir(parent) {
			if files[filepath.ToSlash(parent)] != nil {
				return "", fmt.Errorf("conflicting archive paths %q and %q", parent, name)
			}
		}
	}
	manifestFile := files[theme.ManifestFile]
	if manifestFile == nil {
		return "", errors.New("archive is missing theme.manifest")
	}
	if manifestFile.UncompressedSize64 > maxManifestBytes {
		return "", errors.New("manifest exceeds the size limit")
	}
	r, err := manifestFile.Open()
	if err != nil {
		return "", err
	}

	data, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	closeErr := r.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}

	if len(data) > maxManifestBytes {
		return "", errors.New("manifest exceeds the size limit")
	}
	var m theme.Manifest
	if err := decodeManifest(data, &m); err != nil {
		return "", err
	}

	if name != "" {
		m.Name = name
		data, err = json.MarshalIndent(m, "", "  ")
		if err != nil {
			return "", err
		}
		data = append(data, '\n')
	}
	if err := theme.Validate(m); err != nil {
		return "", err
	}

	wanted := map[string]bool{theme.ManifestFile: true}
	for _, entry := range m.Files {
		key := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Source)))
		if files[key] == nil {
			return "", fmt.Errorf("manifest source %q is missing from archive", entry.Source)
		}
		wanted[key] = true
	}
	for name := range files {
		if !wanted[name] {
			return "", fmt.Errorf("archive contains unreferenced file %q", name)
		}
	}
	if err := os.MkdirAll(s.ThemesDir, 0o755); err != nil {
		return "", err
	}

	final, err := s.SafeThemePath(m.Name)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(final); err == nil {
		return "", fmt.Errorf("theme %q already exists; choose another name", m.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	stage, err := os.MkdirTemp(s.ThemesDir, ".hyprtheme-import-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	for name, f := range files {
		if name == theme.ManifestFile {
			continue
		}

		out := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}

		reader, err := f.Open()
		if err != nil {
			return "", err
		}

		dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			reader.Close()
			return "", err
		}

		n, copyErr := io.Copy(dst, io.LimitReader(reader, maxFileBytes+1))
		readClose := reader.Close()
		writeClose := dst.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if readClose != nil {
			return "", readClose
		}
		if writeClose != nil {
			return "", writeClose
		}
		if n > maxFileBytes {
			return "", fmt.Errorf("archive file %q exceeds the size limit", name)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, theme.ManifestFile), data, 0o644); err != nil {
		return "", err
	}
	if _, err := theme.ReadManifest(stage); err != nil {
		return "", fmt.Errorf("imported manifest validation: %w", err)
	}
	if err := os.Rename(stage, final); err != nil {
		return "", err
	}
	return m.Name, nil
}

func sources(m theme.Manifest) []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, filepath.ToSlash(f.Source))
	}
	return out
}

// decodeManifest keeps the archive boundary independent of filesystem extraction.
func decodeManifest(data []byte, dst *theme.Manifest) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("manifest contains trailing data")
	}
	return nil
}
