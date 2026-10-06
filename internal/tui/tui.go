package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hyprtheme/internal/apply"
	"hyprtheme/internal/archive"
	"hyprtheme/internal/modules"
	"hyprtheme/internal/store"
	"hyprtheme/internal/theme"
)

type phase int

const (
	browse phase = iota
	nameEntry
	modulePick
	saveReview
	loadConfirm
	deleteConfirm
	exportPath
	importPath
	importName
	moduleManager
	moduleEditor
	moduleDelete
	rebootPrompt
)

type item struct{ manifest theme.Manifest }

func (i item) Title() string { return "• " + i.manifest.Name }
func (i item) Description() string {
	return fmt.Sprintf("%d configuration files", len(i.manifest.Files))
}
func (i item) FilterValue() string { return i.manifest.Name }

type model struct {
	store         store.Store
	list          list.Model
	phase         phase
	name          textinput.Model
	modules       []modules.Definition
	selected      []bool
	saveFiles     []theme.File
	order         []int
	cursor        int
	message       string
	width, height int
	archivePath   string
	moduleCursor  int
	moduleInputs  []textinput.Model
	moduleFocus   int
	editingModule int
	rebootCursor  int
}

var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7dd3fc"))
var selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a7f3d0"))
var availableHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#86efac"))
var missingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#777777"))

func Run(s store.Store) error {
	items, err := s.List()
	if err != nil {
		return err
	}

	rows := make([]list.Item, 0, len(items))
	for _, m := range items {
		rows = append(rows, item{m})
	}
	l := list.New(rows, list.NewDefaultDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)

	input := textinput.New()
	input.Placeholder = "theme name"
	input.CharLimit = 64
	configured, err := modules.Load()
	if err != nil {
		return err
	}

	m := model{store: s, list: l, name: input, modules: configured, selected: make([]bool, len(configured))}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd { return nil }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, max(1, msg.Height-8))
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.phase {
		case browse:
			switch msg.String() {
			case "q", "esc":
				return m, tea.Quit
			case "s":
				m.phase = nameEntry
				m.name.SetValue("")
				m.name.Focus()
				m.message = ""
			case "enter":
				if it, ok := m.list.SelectedItem().(item); ok {
					m.phase = loadConfirm
					m.message = ""
					_ = it
				}
			case "d":
				if _, ok := m.list.SelectedItem().(item); ok {
					m.phase = deleteConfirm
				}
			case "e":
				if it, ok := m.list.SelectedItem().(item); ok {
					m.name.SetValue(it.manifest.Name + ".theme")
					m.name.Focus()
					m.phase = exportPath
				}
			case "i":
				m.name.SetValue("")
				m.name.Focus()
				m.phase = importPath
			case "m":
				m.phase = moduleManager
				m.moduleCursor = 0
			default:
				var cmd tea.Cmd
				m.list, cmd = m.list.Update(msg)
				return m, cmd
			}
		case nameEntry:
			if msg.String() == "esc" {
				m.phase = browse
				return m, nil
			}
			if msg.String() == "enter" {
				if err := theme.ValidateName(m.name.Value()); err != nil {
					m.message = err.Error()
					return m, nil
				}
				m.phase = modulePick
				m.cursor = 0
				m.selected = make([]bool, len(m.modules))
				m.order = m.moduleOrder()
				m.message = ""
				return m, nil
			}
			var cmd tea.Cmd
			m.name, cmd = m.name.Update(msg)
			return m, cmd
		case modulePick:
			switch msg.String() {
			case "esc":
				m.phase = nameEntry
				return m, nil
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(m.order)-1 {
					m.cursor++
				}
			case " ":
				if len(m.order) == 0 {
					m.message = "No modules are configured."
					break
				}
				index := m.order[m.cursor]
				if !moduleAvailable(m.modules[index]) {
					m.message = "Missing config files can't be selected."
				} else {
					m.selected[index] = !m.selected[index]
					m.message = ""
				}
			case "enter":
				if countSelected(m.selected) == 0 {
					m.message = "Select at least one module."
				} else {
					definitions := make([]modules.Definition, 0, countSelected(m.selected))
					for i, yes := range m.selected {
						if yes {
							definitions = append(definitions, m.modules[i])
						}
					}
					files, err := modules.Expand(definitions)
					if err != nil {
						m.message = err.Error()
						break
					}
					m.saveFiles = files
					m.phase = saveReview
					m.message = ""
				}
			}
		case saveReview:
			switch msg.String() {
			case "esc":
				m.phase = modulePick
			case "n":
				m.phase = browse
			case "y", "enter":
				if err := m.store.Save(m.name.Value(), m.saveFiles); err != nil {
					m.message = err.Error()
					return m, nil
				}
				m.message = "Saved theme " + m.name.Value() + "."
				m.refresh()
				m.phase = browse
			}
		case loadConfirm:
			switch msg.String() {
			case "esc", "n":
				m.phase = browse
			case "y", "enter":
				it, _ := m.list.SelectedItem().(item)
				dir, err := m.store.SafeThemePath(it.manifest.Name)
				if err == nil {
					var result apply.Result
					result, err = apply.Load(context.Background(), dir, nil)
					if err == nil {
						m.message = "Installed " + it.manifest.Name
						for _, r := range result.Reloads {
							if r.Err != nil {
								m.message += fmt.Sprintf("; %s reload failed: %v", r.Module, r.Err)
							}
						}
						m.rebootCursor = 1
						m.phase = rebootPrompt
					}
				}
				if err != nil {
					m.message = err.Error()
					m.phase = browse
				}
			}
		case rebootPrompt:
			switch msg.String() {
			case "up", "k":
				if m.rebootCursor > 0 {
					m.rebootCursor--
				}
			case "down", "j":
				if m.rebootCursor < 2 {
					m.rebootCursor++
				}
			case "esc", "n":
				m.phase = browse
			case "enter", "y":
				if msg.String() == "y" {
					m.rebootCursor = 0
				}
				if m.rebootCursor == 2 {
					m.phase = browse
					return m, nil
				}

				action := "reboot"
				if m.rebootCursor == 1 {
					if IsSoftRebootAvailable() {
						action = "soft-reboot"
					}
				}

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				cmd := exec.CommandContext(ctx, "systemctl", action)
				output, err := cmd.CombinedOutput()
				if err != nil {
					m.message = fmt.Sprintf("%s; %s request failed: %v %s",
						m.message, action, err, strings.TrimSpace(string(output)))
					return m, nil
				}
				return m, tea.Quit
			}
		case deleteConfirm:
			switch msg.String() {
			case "esc", "n":
				m.phase = browse
			case "y", "enter":
				it, _ := m.list.SelectedItem().(item)
				dir, err := m.store.SafeThemePath(it.manifest.Name)
				if err == nil {
					err = os.RemoveAll(dir)
				}
				if err != nil {
					m.message = err.Error()
				} else {
					m.message = "Deleted theme " + it.manifest.Name + "."
					m.refresh()
				}
				m.phase = browse
			}
		case exportPath:
			if msg.String() == "esc" {
				m.phase = browse
				return m, nil
			}
			if msg.String() == "enter" {
				it, _ := m.list.SelectedItem().(item)
				dir, err := m.store.SafeThemePath(it.manifest.Name)
				if err == nil {
					err = archive.Export(dir, m.name.Value())
				}
				if err != nil {
					m.message = err.Error()
				} else {
					m.message = "Exported " + it.manifest.Name + " to " + m.name.Value()
				}
				m.phase = browse
				return m, nil
			}
			var cmd tea.Cmd
			m.name, cmd = m.name.Update(msg)
			return m, cmd
		case importPath:
			if msg.String() == "esc" {
				m.phase = browse
				return m, nil
			}
			if msg.String() == "enter" {
				m.archivePath = m.name.Value()
				if m.archivePath == "" {
					m.message = "Enter a .theme archive path."
					return m, nil
				}
				m.name.SetValue("")
				m.phase = importName
				return m, nil
			}
			var cmd tea.Cmd
			m.name, cmd = m.name.Update(msg)
			return m, cmd
		case importName:
			if msg.String() == "esc" {
				m.phase = importPath
				return m, nil
			}
			if msg.String() == "enter" {
				name := m.name.Value()
				if name != "" {
					if err := theme.ValidateName(name); err != nil {
						m.message = err.Error()
						return m, nil
					}
				}
				imported, err := archive.ImportAs(m.store, m.archivePath, name)
				if err != nil {
					m.message = err.Error()
				} else {
					m.message = "Imported theme " + imported + "."
					m.refresh()
				}
				m.phase = browse
				return m, nil
			}
			var cmd tea.Cmd
			m.name, cmd = m.name.Update(msg)
			return m, cmd
		case moduleManager:
			switch msg.String() {
			case "esc", "m":
				m.phase = browse
			case "up", "k":
				if m.moduleCursor > 0 {
					m.moduleCursor--
				}
			case "down", "j":
				if m.moduleCursor < len(m.modules)-1 {
					m.moduleCursor++
				}
			case "a":
				m.startModuleEditor(-1)
			case "e":
				if len(m.modules) > 0 {
					m.startModuleEditor(m.moduleCursor)
				}
			case "d":
				if len(m.modules) > 0 {
					m.phase = moduleDelete
				}
			}
		case moduleDelete:
			switch msg.String() {
			case "esc", "n":
				m.phase = moduleManager
			case "y", "enter":
				updated := append([]modules.Definition(nil), m.modules[:m.moduleCursor]...)
				updated = append(updated, m.modules[m.moduleCursor+1:]...)
				if err := modules.Save(updated); err != nil {
					m.message = err.Error()
				} else {
					m.modules, _ = modules.Load()
					m.message = "Module deleted."
				}
				if m.moduleCursor >= len(m.modules) && m.moduleCursor > 0 {
					m.moduleCursor--
				}
				m.phase = moduleManager
			}
		case moduleEditor:
			switch msg.String() {
			case "esc":
				m.phase = moduleManager
			case "tab", "enter":
				if m.moduleFocus < len(m.moduleInputs)-1 {
					m.moduleFocus++
					m.focusModuleInput()
				} else {
					m.saveModuleEditor()
				}
			case "shift+tab":
				if m.moduleFocus > 0 {
					m.moduleFocus--
					m.focusModuleInput()
				}
			case "ctrl+s":
				m.saveModuleEditor()
			default:
				var cmd tea.Cmd
				m.moduleInputs[m.moduleFocus], cmd = m.moduleInputs[m.moduleFocus].Update(msg)
				return m, cmd
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	var body string
	switch m.phase {
	case browse:
		body = m.list.View() + "\n↑/↓ move  Enter load  s save  d delete  e export  i import  m modules  q quit"
		if len(m.list.Items()) == 0 {
			body = "No saved themes yet. Press s to save one.\n" + "↑/↓ move  Enter load  s save  d delete  e export  i import  m modules  q quit"
		}
		if it, ok := m.list.SelectedItem().(item); ok {
			body += "\n\n" + titleStyle.Render(it.manifest.Name) + "\n"
			for _, f := range it.manifest.Files {
				body += fmt.Sprintf("• %-10s %s → %s\n", f.Module, f.Source, f.Destination)
			}
		}
		if m.message != "" {
			body += "\n" + m.message
		}
	case nameEntry:
		body = "Save a theme\n\nName: " + m.name.View() + "\n\nEnter continue · Esc cancel"
		if m.message != "" {
			body += "\n" + m.message
		}
	case modulePick:
		body = "Choose configuration files\n\n"
		if m.message != "" {
			body = "Choose configuration files · " + m.message + "\n\n"
		}
		labelWidth := 0
		for _, d := range m.modules {
			if len(d.Label) > labelWidth {
				labelWidth = len(d.Label)
			}
		}
		var available, missing []int
		for i, d := range m.modules {
			if modules.Available(d) {
				available = append(available, i)
			} else {
				missing = append(missing, i)
			}
		}
		body += availableHeaderStyle.Render("Available") + "\n"
		for _, i := range available {
			body += m.moduleRow(i, labelWidth, false)
		}
		if len(missing) > 0 {
			body += "\n" + missingStyle.Render("Missing") + "\n"
			for _, i := range missing {
				body += m.moduleRow(i, labelWidth, true)
			}
		}
		body += "\nSpace toggle · Enter review · Esc back"
	case saveReview:
		body = "Files to include in theme " + m.name.Value() + "\n\n"
		for _, file := range m.saveFiles {
			body += "• " + file.StoredPath + "\n"
		}
		body += "\nSave theme " + m.name.Value() + "?  Enter/y yes · n cancel · Esc back"
		if m.message != "" {
			body += "\n" + m.message
		}
	case loadConfirm:
		it, _ := m.list.SelectedItem().(item)
		body = "Replace these destinations with theme " + it.manifest.Name + "?\n\n"
		for _, f := range it.manifest.Files {
			d, _ := theme.ExpandDestination(f.Destination)
			body += "• " + d + "\n"
		}
		body += "\nEnter/y apply · n cancel"
	case deleteConfirm:
		it, _ := m.list.SelectedItem().(item)
		body = "Delete theme " + it.manifest.Name + " and its managed files?\n\nEnter y to delete · n to cancel"
	case exportPath:
		body = "Export theme archive\n\nPath: " + m.name.View() + "\n\nEnter export · Esc cancel"
	case importPath:
		body = "Import theme archive\n\nArchive path: " + m.name.View() + "\n\nEnter continue · Esc cancel"
	case importName:
		body = "Import under another name (leave blank to keep archive name)\n\nName: " + m.name.View() + "\n\nEnter import · Esc back"
	case moduleManager:
		body = "Modules\n\n"
		if len(m.modules) == 0 {
			body += "No modules configured. Press a to add one.\n"
		}
		for i, d := range m.modules {
			prefix := "  "
			if i == m.moduleCursor {
				prefix = "> "
			}
			body += fmt.Sprintf("%s%s — %s\n", prefix, d.Name, d.Label)
		}
		if len(m.modules) > 0 {
			d := m.modules[m.moduleCursor]
			body += fmt.Sprintf("\nName: %s\nLabel: %s\nSource: %s\nDestination: %s\n", d.Name, d.Label, d.Source, d.Destination)
		}
		body += "\n↑/↓ move · a add · e edit · d delete · Esc back"
		if m.message != "" {
			body += "\n" + m.message
		}
	case moduleEditor:
		labels := []string{"Name", "Label", "Source path", "Destination path"}
		body = "Add or edit module\n\n"
		for i, input := range m.moduleInputs {
			body += labels[i] + ": " + input.View() + "\n"
		}
		body += "\nTab/Enter next · Ctrl+S save · Esc cancel"
		if m.message != "" {
			body += "\n" + m.message
		}
	case moduleDelete:
		d := m.modules[m.moduleCursor]
		body = fmt.Sprintf("Delete module %q (%s)?\n\nThis only removes the module definition. Existing themes are unchanged.\n\nEnter/y delete · n cancel", d.Name, d.Label)
	case rebootPrompt:
		body = "Theme applied. It is recommended to reboot after applying a theme.\n\n"
		choices := []string{"Reboot fully now", "Soft-Reboot now", "Reboot later"}
		for i, choice := range choices {
			prefix := "  "
			if m.rebootCursor == i {
				prefix = "> "
			}
			body += prefix + choice + "\n"
		}
		body += "\n↑/↓ choose · Enter confirm"
		if m.message != "" {
			body += "\n" + m.message
		}
	}
	header := titleStyle.Render("HYPRTHEME")
	view := header + "\n" + body
	if m.height > 0 {
		lines := strings.Split(view, "\n")
		if len(lines) > m.height {
			view = strings.Join(lines[:m.height], "\n")
		}
	}
	return view
}

func (m *model) startModuleEditor(index int) {
	m.editingModule = index
	values := []string{"", "", "", ""}
	if index >= 0 {
		d := m.modules[index]
		values = []string{d.Name, d.Label, d.Source, d.Destination}
	}
	m.moduleInputs = make([]textinput.Model, len(values))
	placeholders := []string{"hyprland-lua", "Hyprland Lua", "~/.config/hypr/hyprland.lua", "~/.config/hypr/hyprland.lua"}
	for i, value := range values {
		input := textinput.New()
		input.SetValue(value)
		input.Placeholder = placeholders[i]
		input.CharLimit = 4096
		if i == 0 {
			input.Focus()
		} else {
			input.Blur()
		}
		m.moduleInputs[i] = input
	}
	m.moduleFocus = 0
	m.phase = moduleEditor
	m.message = ""
}

func (m *model) focusModuleInput() {
	for i := range m.moduleInputs {
		if i == m.moduleFocus {
			m.moduleInputs[i].Focus()
		} else {
			m.moduleInputs[i].Blur()
		}
	}
}

func (m *model) saveModuleEditor() {
	d := modules.Definition{Name: m.moduleInputs[0].Value(), Label: m.moduleInputs[1].Value(), Source: m.moduleInputs[2].Value(), Destination: m.moduleInputs[3].Value()}
	updated := append([]modules.Definition(nil), m.modules...)
	if m.editingModule < 0 {
		updated = append(updated, d)
	} else {
		updated[m.editingModule] = d
	}
	if err := modules.Save(updated); err != nil {
		m.message = err.Error()
		return
	}
	m.modules = updated
	if configured, err := modules.Load(); err == nil {
		m.modules = configured
	}
	if m.editingModule < 0 {
		m.moduleCursor = len(updated) - 1
	} else {
		m.moduleCursor = m.editingModule
	}
	m.message = "Module saved."
	m.phase = moduleManager
}

func (m model) moduleRow(i, labelWidth int, unavailable bool) string {
	d := m.modules[i]
	mark := "[ ]"
	if m.selected[i] {
		mark = "[x]"
	}
	prefix := "  "
	if m.cursor >= 0 && m.cursor < len(m.order) && i == m.order[m.cursor] {
		prefix = "> "
	}
	state := "available"
	if unavailable {
		state = "missing"
	}
	row := fmt.Sprintf("%s%s %-*s %s\n", prefix, mark, labelWidth, d.Label, state)
	if unavailable {
		return missingStyle.Render(row)
	}
	if m.cursor >= 0 && m.cursor < len(m.order) && i == m.order[m.cursor] {
		return selectedStyle.Render(strings.TrimSuffix(row, "\n")) + "\n"
	}
	return row
}

func (m model) moduleOrder() []int {
	available, missing := []int{}, []int{}
	for i, d := range m.modules {
		if modules.Available(d) {
			available = append(available, i)
		} else {
			missing = append(missing, i)
		}
	}
	return append(available, missing...)
}

func moduleAvailable(module modules.Definition) bool {
	return modules.Available(module)
}

func (m *model) refresh() {
	items, _ := m.store.List()
	rows := make([]list.Item, 0, len(items))
	for _, it := range items {
		rows = append(rows, item{it})
	}
	m.list.SetItems(rows)
}
func countSelected(values []bool) int {
	n := 0
	for _, v := range values {
		if v {
			n++
		}
	}
	return n
}

func IsSoftRebootAvailable() bool {
	_, err := exec.LookPath("systemctl")
	if err != nil {
		return false
	}

	cmd := exec.Command("systemctl", "list-unit-files", "soft-reboot.target")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return false
	}

	return strings.Contains(stdout.String(), "soft-reboot.target")
}
