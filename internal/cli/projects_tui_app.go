package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type projectsView int

const (
	viewProjects projectsView = iota
	viewProjectMenu
	viewContainers
	viewContainerMenu
	viewTunnelsSingle
	viewTunnelsMulti
	viewMultiPortMode
	viewPortInput
	viewRunningSingle
	viewRunningMulti
)

type portInputMode int

const (
	portInputNone portInputMode = iota
	portInputCreate
	portInputSingle
	portInputMultiCustom
)

type appItem struct {
	title string
	desc  string
	idx   int
}

func (i appItem) Title() string       { return i.title }
func (i appItem) Description() string { return i.desc }
func (i appItem) FilterValue() string { return i.title + " " + i.desc }

type projectsLoadedMsg struct {
	projects []project
	err      error
}

type containersLoadedMsg struct {
	containers []container
	err        error
}

type tunnelsLoadedMsg struct {
	tunnels []tunnel
	err     error
}

type tunnelCreatedMsg struct {
	err error
}

type singleSSHDoneMsg struct {
	err error
}

type singleStartMsg struct {
	cmd       *exec.Cmd
	localPort int
	err       error
}

type multiTunnelPlan struct {
	tunnel    tunnel
	localPort int
	keyPath   string
}

type multiStartMsg struct {
	cmds   []*exec.Cmd
	plans  []multiTunnelPlan
	events chan multiEvent
	err    error
}

type multiEvent struct {
	index int
	err   error
}

type multiEventMsg struct {
	event multiEvent
}

type projectsApp struct {
	token string

	list  list.Model
	input textinput.Model
	view  projectsView

	projects          []project
	selectedProject   project
	containers        []container
	selectedContainer container
	tunnels           []tunnel
	selectedTunnel    tunnel

	multiSelectedIdxs map[int]bool
	singleRunningCmd  *exec.Cmd
	singleRunningPort int
	multiCustomList   []tunnel
	multiCustomPorts  []int
	multiCustomIndex  int
	multiRunningCmds  []*exec.Cmd
	multiRunningPlans []multiTunnelPlan
	multiRunningState []string
	multiEvents       chan multiEvent

	portMode        portInputMode
	portInputPrompt string
	portInputDef    int

	status string
	errMsg string
	width  int
	height int
}

func runProjectsTUI(token string) error {
	m := newProjectsApp(token)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func newProjectsApp(token string) projectsApp {
	d := list.NewDefaultDelegate()
	d.ShowDescription = true
	l := list.New([]list.Item{}, d, 100, 24)
	l.SetFilteringEnabled(true)
	l.SetShowStatusBar(true)
	l.SetShowHelp(true)
	l.Title = "Hubfly Projects"

	ti := textinput.New()
	ti.Prompt = "> "
	ti.Placeholder = ""
	ti.CharLimit = 10
	ti.Width = 20

	return projectsApp{
		token:             token,
		list:              l,
		input:             ti,
		view:              viewProjects,
		multiSelectedIdxs: map[int]bool{},
	}
}

func (m projectsApp) Init() tea.Cmd {
	return fetchProjectsCmd(m.token)
}

func (m projectsApp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, max(10, msg.Height-6))
		if m.view == viewPortInput {
			m.input.Width = max(10, msg.Width-20)
		}
		return m, nil
	case projectsLoadedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.projects = msg.projects
		if len(msg.projects) == 0 {
			m.status = "No projects found. Press q to quit."
			m.list.SetItems([]list.Item{})
			m.list.Title = "Hubfly Projects"
			m.view = viewProjects
			return m, nil
		}
		m.errMsg = ""
		m.status = "Select a project"
		m.view = viewProjects
		m.setProjectItems()
		return m, nil
	case containersLoadedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Failed to load containers"
			return m, nil
		}
		m.errMsg = ""
		m.containers = msg.containers
		m.view = viewContainers
		m.status = "Select a container"
		m.setContainerItems()
		return m, nil
	case tunnelsLoadedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Failed to load tunnels"
			return m, nil
		}
		m.errMsg = ""
		m.tunnels = filterContainerTunnels(msg.tunnels, m.selectedContainer.ID)
		m.view = viewContainerMenu
		m.status = fmt.Sprintf("%d tunnel(s) loaded", len(m.tunnels))
		m.setContainerActionItems()
		return m, nil
	case tunnelCreatedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Tunnel creation failed"
		} else {
			m.errMsg = ""
			m.status = "Tunnel created"
		}
		m.view = viewContainerMenu
		m.setContainerActionItems()
		return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
	case singleStartMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Failed to start tunnel"
			m.view = viewContainerMenu
			m.setContainerActionItems()
			return m, nil
		}
		m.errMsg = ""
		m.singleRunningCmd = msg.cmd
		m.singleRunningPort = msg.localPort
		m.view = viewRunningSingle
		m.status = fmt.Sprintf("Tunnel open: localhost:%d -> %s:%d", msg.localPort, m.selectedTunnel.TargetNetwork.IPAddress, m.selectedTunnel.TargetPort)
		return m, waitSingleTunnelDoneCmd(msg.cmd)
	case singleSSHDoneMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Tunnel session ended with error"
		} else {
			m.errMsg = ""
			m.status = "Tunnel session closed"
		}
		m.singleRunningCmd = nil
		m.singleRunningPort = 0
		m.view = viewContainerMenu
		m.setContainerActionItems()
		return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
	case multiStartMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Failed to start multi tunnel"
			m.view = viewContainerMenu
			m.setContainerActionItems()
			return m, nil
		}
		m.errMsg = ""
		m.multiRunningCmds = msg.cmds
		m.multiRunningPlans = msg.plans
		m.multiEvents = msg.events
		m.multiRunningState = make([]string, len(msg.cmds))
		for i := range m.multiRunningState {
			m.multiRunningState[i] = "running"
		}
		m.view = viewRunningMulti
		m.status = fmt.Sprintf("%d tunnel process(es) running", len(msg.cmds))
		return m, waitMultiEventCmd(msg.events)
	case multiEventMsg:
		if msg.event.index >= 0 && msg.event.index < len(m.multiRunningState) {
			if msg.event.err != nil {
				m.multiRunningState[msg.event.index] = "error"
				m.errMsg = msg.event.err.Error()
			} else {
				m.multiRunningState[msg.event.index] = "exited"
			}
		}
		allDone := true
		for _, st := range m.multiRunningState {
			if st == "running" {
				allDone = false
				break
			}
		}
		if allDone {
			m.status = "All multi tunnels exited"
			m.view = viewContainerMenu
			m.setContainerActionItems()
			m.multiRunningCmds = nil
			m.multiRunningPlans = nil
			m.multiRunningState = nil
			return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
		}
		return m, waitMultiEventCmd(m.multiEvents)
	}

	if m.view == viewPortInput {
		switch key := msg.(type) {
		case tea.KeyMsg:
			switch key.String() {
			case "esc":
				m.errMsg = ""
				if m.portMode == portInputMultiCustom {
					m.view = viewMultiPortMode
					m.setMultiPortModeItems()
				} else if m.portMode == portInputSingle {
					m.view = viewTunnelsSingle
					m.setTunnelSingleItems()
				} else {
					m.view = viewContainerMenu
					m.setContainerActionItems()
				}
				return m, nil
			case "enter":
				port, err := strconv.Atoi(strings.TrimSpace(m.input.Value()))
				if err != nil || port <= 0 {
					m.errMsg = "Invalid port"
					return m, nil
				}
				m.errMsg = ""
				switch m.portMode {
				case portInputCreate:
					m.view = viewContainerMenu
					m.setContainerActionItems()
					m.status = "Creating tunnel..."
					return m, createTunnelWithKeyCmd(m.token, m.selectedProject.ID, m.selectedContainer, port)
				case portInputSingle:
					m.status = "Starting tunnel session..."
					return m, startSingleTunnelCmd(
						m.selectedTunnel,
						filepath.Join(keysDir(), "tunnel-"+m.selectedTunnel.TunnelID),
						port,
						m.selectedTunnel.TargetPort,
					)
				case portInputMultiCustom:
					m.multiCustomPorts = append(m.multiCustomPorts, port)
					m.multiCustomIndex++
					if m.multiCustomIndex < len(m.multiCustomList) {
						next := m.multiCustomList[m.multiCustomIndex]
						m.setPortInput(portInputMultiCustom, fmt.Sprintf("Local port for %s", next.TunnelID), next.TargetPort)
						return m, nil
					}
					plans := make([]multiTunnelPlan, 0, len(m.multiCustomList))
					for i, t := range m.multiCustomList {
						plans = append(plans, multiTunnelPlan{
							tunnel:    t,
							localPort: m.multiCustomPorts[i],
							keyPath:   filepath.Join(keysDir(), "tunnel-"+t.TunnelID),
						})
					}
					m.status = "Starting multiple tunnels..."
					return m, startMultiTunnelsCmd(plans)
				}
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch key := msg.(type) {
	case tea.KeyMsg:
		switch key.String() {
		case "ctrl+c":
			if len(m.multiRunningCmds) > 0 {
				m.stopAllMulti()
			}
			if m.singleRunningCmd != nil {
				_ = stopSSHProcess(m.singleRunningCmd)
				m.singleRunningCmd = nil
			}
			return m, tea.Quit
		case "q":
			if len(m.multiRunningCmds) > 0 {
				m.stopAllMulti()
			}
			if m.singleRunningCmd != nil {
				_ = stopSSHProcess(m.singleRunningCmd)
				m.singleRunningCmd = nil
			}
			if m.view == viewProjects {
				return m, tea.Quit
			}
		}

		switch m.view {
		case viewProjects:
			if key.String() == "enter" {
				item, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				m.selectedProject = m.projects[item.idx]
				m.view = viewProjectMenu
				m.setProjectActionItems()
				m.status = "Project selected"
				return m, nil
			}
		case viewProjectMenu:
			if key.String() == "esc" {
				m.view = viewProjects
				m.setProjectItems()
				return m, nil
			}
			if key.String() == "enter" {
				item, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				switch item.idx {
				case 0:
					m.status = "Loading containers..."
					return m, fetchContainersCmd(m.token, m.selectedProject.ID)
				case 1:
					m.status = "Refreshing project..."
					return m, fetchContainersCmd(m.token, m.selectedProject.ID)
				default:
					m.view = viewProjects
					m.setProjectItems()
					return m, nil
				}
			}
		case viewContainers:
			if key.String() == "esc" {
				m.view = viewProjectMenu
				m.setProjectActionItems()
				return m, nil
			}
			if key.String() == "enter" {
				item, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				m.selectedContainer = m.containers[item.idx]
				m.status = "Loading tunnels..."
				return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
			}
		case viewContainerMenu:
			if key.String() == "esc" {
				m.view = viewContainers
				m.setContainerItems()
				return m, nil
			}
			if key.String() == "enter" {
				item, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				switch item.idx {
				case 0:
					m.setPortInput(portInputCreate, "Target container port", 80)
					return m, nil
				case 1:
					if len(m.tunnels) == 0 {
						m.status = "No tunnels available"
						return m, nil
					}
					m.view = viewTunnelsSingle
					m.setTunnelSingleItems()
					return m, nil
				case 2:
					if len(m.tunnels) == 0 {
						m.status = "No tunnels available"
						return m, nil
					}
					m.view = viewTunnelsMulti
					m.setTunnelMultiItems(nil)
					return m, nil
				case 3:
					m.status = "Refreshing tunnels..."
					return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
				default:
					m.view = viewContainers
					m.setContainerItems()
					return m, nil
				}
			}
		case viewTunnelsSingle:
			if key.String() == "esc" {
				m.view = viewContainerMenu
				m.setContainerActionItems()
				return m, nil
			}
			if key.String() == "enter" {
				item, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				m.selectedTunnel = m.tunnels[item.idx]
				m.setPortInput(portInputSingle, "Local forward port", m.selectedTunnel.TargetPort)
				return m, nil
			}
		case viewTunnelsMulti:
			if key.String() == "esc" {
				m.view = viewContainerMenu
				m.setContainerActionItems()
				return m, nil
			}
			if key.String() == "space" {
				it, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				m.multiSelectedIdxs[it.idx] = !m.multiSelectedIdxs[it.idx]
				m.setTunnelMultiItems(&it.idx)
				return m, nil
			}
			if key.String() == "a" {
				all := true
				for idx := range m.tunnels {
					if !m.multiSelectedIdxs[idx] {
						all = false
						break
					}
				}
				for idx := range m.tunnels {
					m.multiSelectedIdxs[idx] = !all
				}
				m.setTunnelMultiItems(nil)
				return m, nil
			}
			if key.String() == "enter" {
				picked := make([]tunnel, 0)
				for idx, ok := range m.multiSelectedIdxs {
					if ok && idx >= 0 && idx < len(m.tunnels) {
						picked = append(picked, m.tunnels[idx])
					}
				}
				if len(picked) == 0 {
					m.status = "Select at least one tunnel"
					return m, nil
				}
				m.multiCustomList = picked
				m.view = viewMultiPortMode
				m.setMultiPortModeItems()
				return m, nil
			}
		case viewMultiPortMode:
			if key.String() == "esc" {
				m.view = viewTunnelsMulti
				m.setTunnelMultiItems(nil)
				return m, nil
			}
			if key.String() == "enter" {
				item, ok := m.list.SelectedItem().(appItem)
				if !ok {
					return m, nil
				}
				switch item.idx {
				case 0:
					plans := make([]multiTunnelPlan, 0, len(m.multiCustomList))
					for _, t := range m.multiCustomList {
						plans = append(plans, multiTunnelPlan{
							tunnel:    t,
							localPort: t.TargetPort,
							keyPath:   filepath.Join(keysDir(), "tunnel-"+t.TunnelID),
						})
					}
					m.status = "Starting multiple tunnels..."
					return m, startMultiTunnelsCmd(plans)
				case 1:
					m.multiCustomPorts = nil
					m.multiCustomIndex = 0
					if len(m.multiCustomList) == 0 {
						m.view = viewContainerMenu
						m.setContainerActionItems()
						return m, nil
					}
					first := m.multiCustomList[0]
					m.setPortInput(portInputMultiCustom, fmt.Sprintf("Local port for %s", first.TunnelID), first.TargetPort)
					return m, nil
				default:
					m.view = viewTunnelsMulti
					m.setTunnelMultiItems(nil)
					return m, nil
				}
			}
		case viewRunningSingle:
			if key.String() == "s" || key.String() == "enter" || key.String() == "esc" {
				if m.singleRunningCmd != nil {
					_ = stopSSHProcess(m.singleRunningCmd)
					m.singleRunningCmd = nil
				}
				m.singleRunningPort = 0
				m.view = viewContainerMenu
				m.setContainerActionItems()
				m.status = "Stopped tunnel session"
				return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
			}
		case viewRunningMulti:
			if key.String() == "s" || key.String() == "enter" || key.String() == "esc" {
				m.stopAllMulti()
				m.view = viewContainerMenu
				m.setContainerActionItems()
				m.status = "Stopped all running multi tunnels"
				return m, fetchTunnelsCmd(m.token, m.selectedProject.ID)
			}
		}
	}

	if m.view == viewRunningSingle || m.view == viewRunningMulti {
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m projectsApp) View() string {
	header := "Hubfly CLI - Projects TUI\n"
	if m.selectedProject.ID != "" {
		header += fmt.Sprintf("Project: %s (%s)\n", m.selectedProject.Name, m.selectedProject.ID)
	}
	if m.selectedContainer.ID != "" {
		header += fmt.Sprintf("Container: %s (%s) | CPU %.2f | RAM %.0fMB | Storage %.0fGB\n",
			m.selectedContainer.Name,
			m.selectedContainer.ID,
			m.selectedContainer.Resources.CPU,
			m.selectedContainer.Resources.RAM,
			m.selectedContainer.Resources.Storage,
		)
	}
	if strings.TrimSpace(m.status) != "" {
		header += "Status: " + m.status + "\n"
	}
	if strings.TrimSpace(m.errMsg) != "" {
		header += "Error: " + m.errMsg + "\n"
	}
	header += strings.Repeat("-", 80) + "\n"

	if m.view == viewPortInput {
		return header + "\n" + m.portInputPrompt + "\n" + m.input.View() + "\n\nEnter to confirm, Esc to cancel"
	}

	if m.view == viewRunningSingle {
		var b strings.Builder
		b.WriteString(header)
		b.WriteString("\nSingle tunnel session is active:\n\n")
		b.WriteString(fmt.Sprintf("- %s | localhost:%d -> %s:%d\n",
			m.selectedTunnel.TunnelID,
			m.singleRunningPort,
			m.selectedTunnel.TargetNetwork.IPAddress,
			m.selectedTunnel.TargetPort,
		))
		b.WriteString("\nUse this endpoint locally now.\n")
		b.WriteString("Press 's' (or Enter/Esc) to stop and return.\n")
		return b.String()
	}

	if m.view == viewRunningMulti {
		var b strings.Builder
		b.WriteString(header)
		b.WriteString("\nRunning multi-tunnel sessions:\n\n")
		for i, plan := range m.multiRunningPlans {
			state := "running"
			if i < len(m.multiRunningState) {
				state = m.multiRunningState[i]
			}
			b.WriteString(fmt.Sprintf("- %s | localhost:%d -> %s:%d | %s\n",
				plan.tunnel.TunnelID,
				plan.localPort,
				plan.tunnel.TargetNetwork.IPAddress,
				plan.tunnel.TargetPort,
				state,
			))
		}
		b.WriteString("\nPress 's' (or Enter/Esc) to stop all and return.\n")
		return b.String()
	}

	return header + "\n" + m.list.View()
}

func (m *projectsApp) setListItems(title string, items []list.Item, status string, filter bool) {
	m.list.Title = title
	m.list.SetItems(items)
	m.list.ResetFilter()
	m.list.SetFilteringEnabled(filter)
	if strings.TrimSpace(status) != "" {
		m.list.NewStatusMessage(status)
	}
}

func (m *projectsApp) setProjectItems() {
	m.selectedContainer = container{}
	items := make([]list.Item, 0, len(m.projects))
	for i, p := range m.projects {
		items = append(items, appItem{
			title: p.Name,
			desc:  fmt.Sprintf("%s | %s | role=%s | spent=%s | %s", p.Region.Name, p.Status, p.Role, valueOrDash(p.Spent), p.ID),
			idx:   i,
		})
	}
	m.setListItems("Projects", items, "Type to filter, Enter select, q quit", true)
}

func (m *projectsApp) setProjectActionItems() {
	items := []list.Item{
		appItem{title: "Manage Containers", desc: "Open containers for selected project", idx: 0},
		appItem{title: "Refresh Project", desc: "Reload containers and metadata", idx: 1},
		appItem{title: "Back", desc: "Return to projects list", idx: 2},
	}
	m.setListItems("Project Actions", items, "Enter select, Esc back", false)
}

func (m *projectsApp) setContainerItems() {
	items := make([]list.Item, 0, len(m.containers))
	for i, c := range m.containers {
		items = append(items, appItem{
			title: c.Name,
			desc:  fmt.Sprintf("%s | CPU %.2f | RAM %.0fMB | ports %d | %s", c.Status, c.Resources.CPU, c.Resources.RAM, len(c.Networking.Ports), c.ID),
			idx:   i,
		})
	}
	m.setListItems("Containers", items, "Type to filter, Enter select, Esc back", true)
}

func (m *projectsApp) setContainerActionItems() {
	items := []list.Item{
		appItem{title: "Create New Tunnel", desc: "Create tunnel with generated SSH key", idx: 0},
		appItem{title: "Connect One Tunnel", desc: "Open one SSH tunnel session", idx: 1},
		appItem{title: "Connect Multiple Tunnels", desc: "Run many SSH tunnels concurrently", idx: 2},
		appItem{title: "Refresh Tunnels", desc: "Reload current tunnel list", idx: 3},
		appItem{title: "Back", desc: "Return to container list", idx: 4},
	}
	m.setListItems("Container Actions", items, "Enter select, Esc back", false)
}

func (m *projectsApp) setTunnelSingleItems() {
	items := make([]list.Item, 0, len(m.tunnels))
	for i, t := range m.tunnels {
		items = append(items, appItem{
			title: t.TunnelID,
			desc:  fmt.Sprintf("%s:%d -> %s:%d | %s", t.SSHHost, t.SSHPort, t.TargetNetwork.IPAddress, t.TargetPort, tunnelState(t.ExpiresAt)),
			idx:   i,
		})
	}
	m.setListItems("Pick Tunnel", items, "Type to filter, Enter select, Esc back", true)
}

func (m *projectsApp) setTunnelMultiItems(preserveIdx *int) {
	cursor := m.list.Index()
	if preserveIdx != nil {
		cursor = *preserveIdx
	}
	items := make([]list.Item, 0, len(m.tunnels))
	for i, t := range m.tunnels {
		mark := "[ ]"
		if m.multiSelectedIdxs[i] {
			mark = "[x]"
		}
		items = append(items, appItem{
			title: fmt.Sprintf("%s %s", mark, t.TunnelID),
			desc:  fmt.Sprintf("%s:%d -> %s:%d", t.SSHHost, t.SSHPort, t.TargetNetwork.IPAddress, t.TargetPort),
			idx:   i,
		})
	}
	m.setListItems("Multi Tunnel Selection", items, "space toggle, a all, enter continue, esc back", true)
	if cursor >= 0 && cursor < len(items) {
		m.list.Select(cursor)
	}
}

func (m *projectsApp) setMultiPortModeItems() {
	items := []list.Item{
		appItem{title: "Use Target Ports", desc: "Use each tunnel target port as local port", idx: 0},
		appItem{title: "Custom Local Ports", desc: "Set a custom local port per tunnel", idx: 1},
		appItem{title: "Back", desc: "Return to tunnel selection", idx: 2},
	}
	m.setListItems("Multi Tunnel Port Mode", items, "Enter select, Esc back", false)
}

func (m *projectsApp) setPortInput(mode portInputMode, prompt string, def int) {
	m.view = viewPortInput
	m.portMode = mode
	m.portInputPrompt = prompt
	m.portInputDef = def
	m.input.SetValue(strconv.Itoa(def))
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *projectsApp) stopAllMulti() {
	for _, cmd := range m.multiRunningCmds {
		_ = stopSSHProcess(cmd)
	}
	m.multiRunningCmds = nil
	m.multiRunningPlans = nil
	m.multiRunningState = nil
	m.multiEvents = nil
}

func fetchProjectsCmd(token string) tea.Cmd {
	return func() tea.Msg {
		projects, err := fetchProjects(token)
		return projectsLoadedMsg{projects: projects, err: err}
	}
}

func fetchContainersCmd(token, projectID string) tea.Cmd {
	return func() tea.Msg {
		details, err := fetchProject(token, projectID)
		if err != nil {
			return containersLoadedMsg{err: err}
		}
		return containersLoadedMsg{containers: details.Containers}
	}
}

func fetchTunnelsCmd(token, projectID string) tea.Cmd {
	return func() tea.Msg {
		tunnels, err := fetchTunnels(token, projectID)
		return tunnelsLoadedMsg{tunnels: tunnels, err: err}
	}
}

func createTunnelWithKeyCmd(token, projectID string, c container, targetPort int) tea.Cmd {
	return func() tea.Msg {
		err := createTunnelWithKey(token, projectID, c, targetPort)
		return tunnelCreatedMsg{err: err}
	}
}

func createTunnelWithKey(token, projectID string, c container, targetPort int) error {
	tempID := fmt.Sprintf("temp-%d-%d-%d", syscall.Getpid(), targetPort, time.Now().UnixNano())
	publicKey, err := generateKeyPairAndSave(tempID)
	if err != nil {
		return err
	}

	t, err := createTunnel(token, createTunnelRequest{
		ProjectID:       projectID,
		TargetContainer: c.Name,
		ContainerID:     c.ID,
		TargetPort:      targetPort,
		PublicKey:       publicKey,
	})
	if err != nil {
		_ = removeKeyPair(tempID)
		return err
	}

	_, err = renameKeyFiles(tempID, "tunnel-"+t.TunnelID)
	return err
}

func startSingleTunnelCmd(t tunnel, keyPath string, localPort, targetPort int) tea.Cmd {
	return func() tea.Msg {
		cmd, err := startTunnelConnectionBackground(t, keyPath, localPort, targetPort)
		if err != nil {
			return singleStartMsg{err: err}
		}
		return singleStartMsg{cmd: cmd, localPort: localPort}
	}
}

func waitSingleTunnelDoneCmd(cmd *exec.Cmd) tea.Cmd {
	return func() tea.Msg {
		return singleSSHDoneMsg{err: cmd.Wait()}
	}
}

func startMultiTunnelsCmd(plans []multiTunnelPlan) tea.Cmd {
	return func() tea.Msg {
		cmds := make([]*exec.Cmd, 0, len(plans))
		events := make(chan multiEvent, len(plans)*2)
		for idx, plan := range plans {
			cmd, err := startTunnelConnectionBackground(plan.tunnel, plan.keyPath, plan.localPort, plan.tunnel.TargetPort)
			if err != nil {
				for _, started := range cmds {
					_ = stopSSHProcess(started)
				}
				return multiStartMsg{err: err}
			}
			cmds = append(cmds, cmd)
			go func(i int, c *exec.Cmd) {
				events <- multiEvent{index: i, err: c.Wait()}
			}(idx, cmd)
		}
		return multiStartMsg{cmds: cmds, plans: plans, events: events}
	}
}

func waitMultiEventCmd(events chan multiEvent) tea.Cmd {
	return func() tea.Msg {
		ev := <-events
		return multiEventMsg{event: ev}
	}
}

func filterContainerTunnels(tunnels []tunnel, containerID string) []tunnel {
	filtered := make([]tunnel, 0)
	for _, t := range tunnels {
		if t.TargetContainerID == containerID {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
