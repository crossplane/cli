/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package terminal contains utilities for terminal interaction.
package terminal

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	bspinner "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossplane/cli/v2/internal/async"
)

var (
	// Crossplane teal for dark backgrounds, dark blue for light backgrounds.

	//nolint:gochecknoglobals // This is effectively a const.
	accentColor = lipgloss.AdaptiveColor{Dark: "#35D0BA", Light: "#183D54"}
	//nolint:gochecknoglobals // This is effectively a const.
	accentStyle = lipgloss.NewStyle().Foreground(accentColor)
)

// SpinnerPrinter prints spinners to the console.
type SpinnerPrinter interface {
	// NewSuccessSpinner returns a new success spinner.
	NewSuccessSpinner(msg string) *SuccessSpinner

	// WrapWithSuccessSpinner adds spinners around message and run function.
	WrapWithSuccessSpinner(msg string, f func() error) error

	// WrapAsyncWithSuccessSpinners runs a given function in a separate
	// goroutine, consuming events from its event channel and using them to
	// display a set of spinners on the terminal. One spinner will be generated
	// for each unique event text received. A success/failure indicator will be
	// displayed when each event completes.
	WrapAsyncWithSuccessSpinners(f func(ch async.EventChannel) error) error
}

type defaultSpinnerPrinter struct {
	pretty bool
	out    io.Writer
}

// NewSpinnerPrinter returns a new SpinnerPrinter. If pretty is true, animated
// spinners will be used; otherwise plain text output will be used.
func NewSpinnerPrinter(out io.Writer, pretty bool) SpinnerPrinter {
	return &defaultSpinnerPrinter{
		pretty: pretty,
		out:    out,
	}
}

func (p *defaultSpinnerPrinter) NewSuccessSpinner(msg string) *SuccessSpinner {
	return newSuccessSpinner(p.out, msg)
}

func (p *defaultSpinnerPrinter) WrapWithSuccessSpinner(msg string, f func() error) error {
	if p.pretty {
		return p.wrapPretty(msg, f)
	}
	return p.wrapPlain(msg, f)
}

func (p *defaultSpinnerPrinter) wrapPretty(msg string, f func() error) error {
	sp := newSuccessSpinner(p.out, msg)
	sp.Start()

	err := f()

	if err != nil {
		sp.Fail()
	} else {
		sp.Success()
	}

	return err
}

func (p *defaultSpinnerPrinter) wrapPlain(msg string, f func() error) error {
	_, _ = fmt.Fprintln(p.out, msg+"...")

	err := f()

	ind := "✓"
	if err != nil {
		ind = "✗"
	}
	_, _ = fmt.Fprintf(p.out, "%s %s\n", ind, msg)

	return err
}

func (p *defaultSpinnerPrinter) WrapAsyncWithSuccessSpinners(fn func(ch async.EventChannel) error) error {
	if p.pretty {
		return p.asyncPretty(fn)
	}

	return p.asyncPlain(fn)
}

func (p *defaultSpinnerPrinter) asyncPretty(fn func(ch async.EventChannel) error) error {
	var (
		updateChan = make(async.EventChannel, 10)
		doneChan   = make(chan error, 1)
	)

	go func() {
		err := fn(updateChan)
		close(updateChan)
		doneChan <- err
	}()
	multi := &MultiSpinner{
		out: p.out,
	}
	multi.Start()

	for update := range updateChan {
		switch update.Status {
		case async.EventStatusStarted:
			multi.Add(update.Text)
		case async.EventStatusSuccess:
			multi.Success(update.Text)
		case async.EventStatusFailure:
			multi.Fail(update.Text)
		}
	}
	err := <-doneChan

	multi.Stop()
	return err
}

func (p *defaultSpinnerPrinter) asyncPlain(fn func(ch async.EventChannel) error) error {
	var (
		updateChan = make(async.EventChannel, 10)
		doneChan   = make(chan error, 1)
	)

	go func() {
		err := fn(updateChan)
		close(updateChan)
		doneChan <- err
	}()

	statusMap := make(map[string]string)
	printed := make(map[string]bool)

	for update := range updateChan {
		prevStatus := statusMap[update.Text]
		switch update.Status {
		case async.EventStatusStarted:
			if !printed[update.Text] {
				_, _ = fmt.Fprintln(p.out, update.Text+"...")
				printed[update.Text] = true
				statusMap[update.Text] = "started"
			}
		case async.EventStatusSuccess:
			if prevStatus != "success" {
				_, _ = fmt.Fprintln(p.out, "✓ "+update.Text)
				statusMap[update.Text] = "success"
			}
		case async.EventStatusFailure:
			if prevStatus != "failure" {
				_, _ = fmt.Fprintln(p.out, "✗ "+update.Text)
				statusMap[update.Text] = "failure"
			}
		}
	}

	return <-doneChan
}

// MultiSpinner is a collection of independent spinners that get displayed
// together. Spinners can be dynamically added.
type MultiSpinner struct {
	spinners []*SuccessSpinner
	mu       sync.Mutex
	program  *tea.Program
	out      io.Writer
}

type tickMsg time.Time

func tick(t time.Time) tea.Msg {
	return tickMsg(t)
}

// Init satisfies tea.Model.
func (m *MultiSpinner) Init() tea.Cmd {
	return tea.Tick(bspinner.Dot.FPS, tick)
}

// Update satisfies tea.Model.
func (m *MultiSpinner) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := msg.(tickMsg); !ok {
		return m, nil
	}

	for _, sp := range m.spinners {
		_, _ = sp.Update(msg)
	}

	return m, tea.Tick(bspinner.Dot.FPS, tick)
}

// View satisfies tea.Model.
func (m *MultiSpinner) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	views := make([]string, len(m.spinners))
	for i, sp := range m.spinners {
		views[i] = sp.View()
	}

	return strings.Join(views, "\n") + "\n"
}

// Add adds a spinner to the multi-spinner.
func (m *MultiSpinner) Add(title string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sp := range m.spinners {
		if sp.title == title {
			return
		}
	}

	m.spinners = append(m.spinners, newSuccessSpinner(m.out, title))
}

// Success marks an existing spinner in the multi-spinner as having succeeded.
func (m *MultiSpinner) Success(title string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sp := range m.spinners {
		if sp.title != title {
			continue
		}
		sp.setSuccess(true)
		return
	}
}

// Fail marks an existing spinner in the multi-spinner as having failed.
func (m *MultiSpinner) Fail(title string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sp := range m.spinners {
		if sp.title != title {
			continue
		}
		sp.setSuccess(false)
		return
	}
}

// Start starts the spinners.
func (m *MultiSpinner) Start() {
	m.program = tea.NewProgram(m,
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
		tea.WithOutput(m.out),
	)

	go runProgramWithSignalHandler(m.program)
}

func runProgramWithSignalHandler(p *tea.Program) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()
	go func() {
		_, ok := <-sigCh
		if ok {
			_ = p.ReleaseTerminal()
			os.Exit(130)
		}
	}()

	_, _ = p.Run()
}

// Stop stops the spinners.
func (m *MultiSpinner) Stop() {
	if m.program == nil {
		return
	}

	m.program.Send(tick(time.Now()))
	m.program.Quit()
	m.program.Wait()
}

// SuccessSpinner is a spinner that can be marked as successful or failed and
// updates its view accordingly. It is used by MultiSpinner, but can also be
// used as a standalone spinner.
type SuccessSpinner struct {
	title string
	out   io.Writer

	success *bool
	spinner bspinner.Model
	log     []string
	mu      sync.Mutex

	program *tea.Program
}

func newSuccessSpinner(w io.Writer, msg string) *SuccessSpinner {
	return &SuccessSpinner{
		title: msg,
		out:   w,
		spinner: bspinner.New(
			bspinner.WithSpinner(bspinner.Dot),
			bspinner.WithStyle(accentStyle),
		),
	}
}

// Init satisfies tea.Model.
func (ss *SuccessSpinner) Init() tea.Cmd {
	return tea.Tick(bspinner.Dot.FPS, tick)
}

// Update satisfies tea.Model.
func (ss *SuccessSpinner) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if _, ok := msg.(tickMsg); !ok {
		return ss, nil
	}
	ss.spinner, _ = ss.spinner.Update(ss.spinner.Tick())

	return ss, tea.Tick(bspinner.Dot.FPS, tick)
}

// View satisfies tea.Model.
func (ss *SuccessSpinner) View() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ind := ss.spinner.View()
	if ss.success != nil {
		ind = accentStyle.Render("✓")
		if !*ss.success {
			ind = accentStyle.Render("✗")
		}
	}

	view := fmt.Sprintf("%s %s", ind, ss.title)
	if len(ss.log) > 0 {
		view += "\n" + strings.Join(ss.log, "\n") + "\n"
	}

	return view
}

// UpdateText updates the spinner's text.
func (ss *SuccessSpinner) UpdateText(msg string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.title = msg
}

// Success marks the spinner as having succeeded.
func (ss *SuccessSpinner) Success() {
	ss.setSuccess(true)
	ss.stop()
}

// Fail marks the spinner as having failed.
func (ss *SuccessSpinner) Fail() {
	ss.setSuccess(false)
	ss.stop()
}

func (ss *SuccessSpinner) setSuccess(v bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.success = &v
}

// Logf adds a formatted message to the log printed under the spinner.
func (ss *SuccessSpinner) Logf(format string, args ...any) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.log = append(ss.log, fmt.Sprintf("ℹ️ "+format, args...))
}

// Start starts the spinner.
func (ss *SuccessSpinner) Start() {
	ss.program = tea.NewProgram(ss,
		tea.WithOutput(ss.out),
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
	)

	go runProgramWithSignalHandler(ss.program)
}

func (ss *SuccessSpinner) stop() {
	if ss.program == nil {
		return
	}

	ss.program.Send(tick(time.Now()))
	ss.program.Quit()
	ss.program.Wait()

	_, _ = fmt.Fprintln(ss.out, ss.View())
}
