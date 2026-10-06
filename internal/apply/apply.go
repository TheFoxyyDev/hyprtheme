package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"hyprtheme/internal/theme"
)

type ReloadResult struct {
	Module, Message string
	Err             error
}
type Reloader interface {
	Reload(context.Context, string) (string, error)
}
type HyprlandReloader struct{ Timeout time.Duration }

func (r HyprlandReloader) Reload(ctx context.Context, module string) (string, error) {
	switch module {
	case "hyprland", "hyprland-lua":
		timeout := r.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}

		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "hyprctl", "reload")

		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("hyprctl reload: %w: %s", err, string(output))
		}

		return "Hyprland config reloaded", nil
	case "hyprpaper", "hyprlock":
		return "file installed; restart " + module + " if it does not reload its config automatically", nil
	default:
		return "file installed; no reload action is configured for module " + module, nil
	}
}

type Result struct {
	Installed []string
	Reloads   []ReloadResult
}
type staged struct {
	destination, temp, backup string
	hadOriginal, installed    bool
}

func Load(ctx context.Context, themeDir string, reloader Reloader) (Result, error) {
	m, err := theme.ReadManifest(themeDir)
	if err != nil {
		return Result{}, err
	}
	items := make([]staged, 0, len(m.Files))
	cleanup := func() {
		for _, item := range items {
			if item.temp != "" {
				_ = os.Remove(item.temp)
			}
		}
	}
	defer cleanup()
	for _, entry := range m.Files {
		source, err := theme.ResolveSource(themeDir, entry.Source)
		if err != nil {
			return Result{}, err
		}

		src, err := os.Open(source)
		if err != nil {
			return Result{}, err
		}

		info, err := src.Stat()
		if err != nil {
			src.Close()
			return Result{}, err
		}

		if !info.Mode().IsRegular() {
			src.Close()
			return Result{}, fmt.Errorf("stored source %q is not a regular file", entry.Source)
		}

		destination, err := theme.ExpandDestination(entry.Destination)
		if err != nil {
			src.Close()
			return Result{}, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			src.Close()
			return Result{}, fmt.Errorf("create parent for %q: %w", destination, err)
		}

		mode := os.FileMode(0o644)
		dstInfo, statErr := os.Lstat(destination)
		hadOriginal := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			src.Close()
			return Result{}, statErr
		}

		if hadOriginal {
			if dstInfo.Mode()&os.ModeSymlink != 0 || !dstInfo.Mode().IsRegular() {
				src.Close()
				return Result{}, fmt.Errorf("refusing to replace non-regular destination %q", destination)
			}
			mode = dstInfo.Mode().Perm()
		}
		tmp, err := os.CreateTemp(filepath.Dir(destination), ".hyprtheme-new-")
		if err != nil {
			src.Close()
			return Result{}, err
		}

		item := staged{destination: destination, temp: tmp.Name(), hadOriginal: hadOriginal}
		items = append(items, item)
		_, copyErr := copyTo(tmp, src)
		srcErr := src.Close()
		closeErr := tmp.Close()
		if copyErr != nil {
			return Result{}, copyErr
		}
		if srcErr != nil {
			return Result{}, srcErr
		}
		if closeErr != nil {
			return Result{}, closeErr
		}
		if err := os.Chmod(item.temp, mode); err != nil {
			return Result{}, err
		}
	}
	rollback := func(cause error) error {
		var failures []error

		for i := len(items) - 1; i >= 0; i-- {
			item := &items[i]
			if item.installed {
				if err := os.Remove(item.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
					failures = append(failures, fmt.Errorf("remove partial %q: %w", item.destination, err))
				}
			}
			if item.backup != "" {
				if err := os.Rename(item.backup, item.destination); err != nil {
					failures = append(failures, fmt.Errorf("restore %q from %q: %w", item.destination, item.backup, err))
				} else {
					item.backup = ""
				}
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("install failed: %w; rollback problems: %v", cause, errors.Join(failures...))
		}
		return cause
	}
	for i := range items {
		item := &items[i]
		if item.hadOriginal {
			backupFile, err := os.CreateTemp(filepath.Dir(item.destination), ".hyprtheme-old-")
			if err != nil {
				return Result{}, rollback(err)
			}

			item.backup = backupFile.Name()
			if err := backupFile.Close(); err != nil {
				return Result{}, rollback(err)
			}
			if err := os.Remove(item.backup); err != nil {
				return Result{}, rollback(err)
			}

			if err := os.Rename(item.destination, item.backup); err != nil {
				item.backup = ""
				return Result{}, rollback(fmt.Errorf("backup %q: %w", item.destination, err))
			}
		}
		if err := os.Rename(item.temp, item.destination); err != nil {
			return Result{}, rollback(fmt.Errorf("install %q: %w", item.destination, err))
		}
		item.temp = ""
		item.installed = true
	}
	for i := range items {
		if items[i].backup != "" {
			if err := os.Remove(items[i].backup); err != nil {
				return Result{}, fmt.Errorf("files were installed but backup %q could not be removed: %w", items[i].backup, err)
			}
			items[i].backup = ""
		}
	}
	result := Result{}
	for _, entry := range m.Files {
		result.Installed = append(result.Installed, mustDestination(entry.Destination))
	}
	if reloader == nil {
		reloader = HyprlandReloader{}
	}

	seen := map[string]bool{}
	for _, entry := range m.Files {
		module := entry.Module
		if module == "hyprland-lua" {
			module = "hyprland"
		}
		if seen[module] {
			continue
		}

		seen[module] = true
		message, err := reloader.Reload(ctx, module)
		result.Reloads = append(result.Reloads, ReloadResult{Module: module, Message: message, Err: err})
	}
	return result, nil
}

func mustDestination(path string) string               { result, _ := theme.ExpandDestination(path); return result }
func copyTo(dst *os.File, src *os.File) (int64, error) { return io.Copy(dst, src) }
