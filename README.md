# hyprtheme

`hyprtheme` is a Linux terminal application for saving, browsing, exporting, and applying Hyprland configuration themes.

## Build and run

```sh
go build -o hyprtheme ./cmd/hyprtheme
./hyprtheme
./hyprtheme --version
```

To build and install or update the executable at `~/.local/bin/hyprtheme`, run:

```sh
./install.sh
```

The script replaces an existing executable at that path. Ensure `~/.local/bin` is on your `PATH` to run `hyprtheme` directly.

The interactive browser supports ↑/↓ navigation, Enter to load, `s` to save, `d` to delete, `e` to export, `i` to import, `m` to manage modules, and `q` to quit. Loading and deletion ask for confirmation. Save lets you choose the files to copy and review their source and destination paths.

The initial save modules include both `hyprland` (`hyprland.conf`) and `hyprland-lua` (`hyprland.lua`), plus `hyprpaper` and `hyprlock`. Their defaults use `${XDG_CONFIG_HOME:-~/.config}/hypr/`; each destination matches its source path. Use the `m` menu to view, add, edit, or delete module definitions. They are stored in `${XDG_CONFIG_HOME:-~/.config}/hyprtheme/modules.json`; source and destination paths may be absolute or start with `~/`, and may refer to a file or directory. For example, a `waybar` module can use `~/.config/waybar` for both paths; regular files such as `config.jsonc` and `style.css` are copied recursively with their relative paths preserved. A saved theme contains copied files and a `theme.manifest`; loading uses those manifest paths rather than the current module defaults. Both Hyprland config formats trigger `hyprctl reload` after installation.

Themes are stored under `${XDG_DATA_HOME:-~/.local/share}/hyprtheme/themes/`. Hyprland configs trigger `hyprctl reload` after installation. For hyprpaper and hyprlock, hyprtheme reports that a restart may be needed because it does not assume a portable reload command.

CLI commands are also available:

```text
hyprtheme list
hyprtheme save <name> <hyprland|hyprland-lua|hyprpaper|hyprlock> [...]
hyprtheme load <name>
hyprtheme export <name> <output.theme>
hyprtheme import <input.theme> [new-name]
```

Portable `.theme` files are ZIP archives. Import validates the manifest and archive paths, limits archive sizes, and extracts to a temporary managed directory before installing the theme. Importing a colliding name fails with an option to supply a new name.

    Copyright © 2026 TheFoxyyDev

    This program is free software: you can redistribute it and/or modify
    it under the terms of the GNU General Public License as published by
    the Free Software Foundation, either version 3 of the License, or
    (at your option) any later version.

    This program is distributed in the hope that it will be useful,
    but WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
    GNU General Public License for more details.

    You should have received a copy of the GNU General Public License
    along with this program.  If not, see <https://www.gnu.org/licenses/>.