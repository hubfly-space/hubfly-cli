package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type listOption struct {
	Title string
	Desc  string
}

type listItem struct {
	title string
	desc  string
}

func (i listItem) Title() string       { return i.title }
func (i listItem) Description() string { return i.desc }
func (i listItem) FilterValue() string { return i.title + " " + i.desc }

type menuModel struct {
	list list.Model
}

func (m menuModel) Init() tea.Cmd { return nil }

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			return m, tea.Quit
		case "esc", "ctrl+c", "q":
			m.list.ResetSelected()
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m menuModel) View() string {
	return m.list.View()
}

func tuiPickOne(title, subtitle string, options []listOption) (int, bool, error) {
	items := make([]list.Item, 0, len(options))
	for _, opt := range options {
		items = append(items, listItem{title: opt.Title, desc: opt.Desc})
	}

	l := list.New(items, list.NewDefaultDelegate(), 100, 24)
	l.Title = title
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)
	if strings.TrimSpace(subtitle) != "" {
		l.NewStatusMessage(subtitle)
	}

	m := menuModel{list: l}
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return 0, false, err
	}
	finalModel, ok := result.(menuModel)
	if !ok {
		return 0, true, fmt.Errorf("unexpected menu result")
	}

	selected := finalModel.list.SelectedItem()
	if selected == nil {
		return 0, true, nil
	}

	selectedItem, ok := selected.(listItem)
	if !ok {
		return 0, true, fmt.Errorf("unexpected selected item type")
	}
	for i, opt := range options {
		if opt.Title == selectedItem.title && opt.Desc == selectedItem.desc {
			return i, false, nil
		}
	}
	return 0, true, nil
}

type multiModel struct {
	title     string
	subtitle  string
	options   []listOption
	cursor    int
	selected  map[int]bool
	confirmed bool
	cancelled bool
	width     int
	height    int
}

func newMultiModel(title, subtitle string, options []listOption) multiModel {
	return multiModel{
		title:    title,
		subtitle: subtitle,
		options:  options,
		selected: map[int]bool{},
	}
}

func (m multiModel) Init() tea.Cmd { return nil }

func (m multiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case "space":
			m.selected[m.cursor] = !m.selected[m.cursor]
		case "a":
			allSelected := true
			for i := range m.options {
				if !m.selected[i] {
					allSelected = false
					break
				}
			}
			for i := range m.options {
				m.selected[i] = !allSelected
			}
		case "enter":
			m.confirmed = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m multiModel) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n")
	if strings.TrimSpace(m.subtitle) != "" {
		b.WriteString(m.subtitle)
		b.WriteString("\n")
	}
	b.WriteString(hintStyle.Render("up/down: move | space: toggle | a: all | enter: confirm | q: cancel"))
	b.WriteString("\n\n")

	for i, opt := range m.options {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}
		mark := "[ ]"
		if m.selected[i] {
			mark = "[x]"
		}
		b.WriteString(fmt.Sprintf("%s%s %s", cursor, mark, opt.Title))
		if strings.TrimSpace(opt.Desc) != "" {
			b.WriteString(hintStyle.Render(" - " + opt.Desc))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func tuiPickMany(title, subtitle string, options []listOption) ([]int, bool, error) {
	if len(options) == 0 {
		return nil, true, nil
	}

	m := newMultiModel(title, subtitle, options)
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return nil, false, err
	}
	finalModel, ok := result.(multiModel)
	if !ok {
		return nil, false, fmt.Errorf("unexpected multi-select result")
	}
	if finalModel.cancelled || !finalModel.confirmed {
		return nil, true, nil
	}

	indices := make([]int, 0, len(finalModel.selected))
	for idx, selected := range finalModel.selected {
		if selected {
			indices = append(indices, idx)
		}
	}
	if len(indices) == 0 {
		return nil, true, nil
	}
	return indices, false, nil
}
