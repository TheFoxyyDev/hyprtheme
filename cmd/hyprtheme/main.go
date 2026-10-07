package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"hyprtheme/internal/apply"
	"hyprtheme/internal/archive"
	"hyprtheme/internal/modules"
	"hyprtheme/internal/store"
	"hyprtheme/internal/theme"
	"hyprtheme/internal/tui"
)

const version = "0.3"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hyprtheme:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	tstore, err := store.Default()

	if err != nil {
		return err
	}
	if len(args) == 0 {
		return tui.Run(tstore)
	}

	if args[0] == "-v" || args[0] == "--version" {
		fmt.Printf("hyprtheme %s\n", version)
		return nil
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return nil
	}

	switch args[0] {
	case "list":
		items, err := tstore.List()
		if err != nil {
			return err
		}

		if len(items) == 0 {
			fmt.Println("No themes saved yet.")
			return nil
		}
		for _, item := range items {
			fmt.Printf("• %s (%d files)\n", item.Name, len(item.Files))
		}
		return nil
	case "save":
		if len(args) < 3 {
			return fmt.Errorf("usage: hyprtheme save <name> <module> [module ...]")
		}

		configured, err := modules.Load()

		if err != nil {
			return err
		}

		defs := map[string]modules.Definition{}

		for _, d := range configured {
			defs[d.Name] = d
		}

		selected := make([]modules.Definition, 0, len(args)-2)

		for _, name := range args[2:] {
			d, ok := defs[name]
			if !ok {
				return fmt.Errorf("unknown module %q (check your configured modules)", name)
			}
			selected = append(selected, d)
		}

		files, err := modules.Expand(selected)

		if err != nil {
			return err
		}
		if err := tstore.Save(args[1], files); err != nil {
			return err
		}
		fmt.Printf("Saved theme %q.\n", args[1])
		return nil
	case "load":
		if len(args) != 2 {
			return fmt.Errorf("usage: hyprtheme load <name>")
		}

		dir, err := tstore.SafeThemePath(args[1])
		if err != nil {
			return err
		}

		manifest, err := theme.ReadManifest(dir)
		if err != nil {
			return err
		}

		fmt.Printf("Load theme %q? These paths will be replaced:\n", manifest.Name)
		for _, f := range manifest.Files {
			destination, _ := theme.ExpandDestination(f.Destination)
			fmt.Printf("  • %s\n", destination)
		}

		fmt.Print("Type yes to continue: ")
		answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && len(answer) == 0 {
			return err
		}

		if strings.TrimSpace(answer) != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}

		result, err := apply.Load(context.Background(), dir, nil)
		if err != nil {
			return err
		}

		for _, path := range result.Installed {
			fmt.Printf("Installed %s\n", path)
		}
		for _, reload := range result.Reloads {
			if reload.Err != nil {
				fmt.Fprintf(os.Stderr, "%s reload failed: %v\n", reload.Module, reload.Err)
			} else {
				fmt.Printf("%s: %s\n", reload.Module, reload.Message)
			}
		}
		return nil
	case "export":
		if len(args) != 3 {
			return fmt.Errorf("usage: hyprtheme export <name> <output.theme>")
		}

		dir, err := tstore.SafeThemePath(args[1])
		if err != nil {
			return err
		}
		if err := archive.Export(dir, args[2]); err != nil {
			return err
		}
		fmt.Printf("Exported %q to %s.\n", args[1], args[2])
		return nil
	case "import":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: hyprtheme import <input.theme> [new-name]")
		}

		name := ""
		if len(args) == 3 {
			name = args[2]
		}

		imported, err := archive.ImportAs(tstore, args[1], name)
		if err != nil {
			return err
		}
		fmt.Printf("Imported theme %q.\n", imported)
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Println("hyprtheme — save and load Hyprland configuration themes")
	fmt.Printf("Version: %s\n", version)
	fmt.Println("Usage:")
	fmt.Println("  hyprtheme --version, -v")
	fmt.Println("  hyprtheme list")
	fmt.Println("  hyprtheme save <name> <hyprland|hyprland-lua|hyprpaper|hyprlock> [...]")
	fmt.Println("  hyprtheme load <name>")
	fmt.Println("  hyprtheme export <name> <output.theme>")
	fmt.Println("  hyprtheme import <input.theme> [new-name]")
	fmt.Println("  hyprtheme")
	fmt.Println()
	fmt.Println("Author: @TheFoxyyDev")
}
