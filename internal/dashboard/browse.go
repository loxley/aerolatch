package dashboard

import (
	"context"
	"errors"
	"io"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/nearby"

	tea "github.com/charmbracelet/bubbletea"
)

type NearbyRunner func(context.Context, func(flight.NearbySnapshot) error) error
type DetailRunner func(context.Context, flight.Selection, func(flight.Snapshot) error) error

type browserRequest struct {
	generation uint64
	hex        string // Empty means the nearby list.
	selection  flight.Selection
}

type browserData struct {
	generation uint64
	snapshot   any
}

type browserDone struct {
	generation uint64
	err        error
}

// Browse owns one terminal/input reader for the entire navigation session.
// A coordinator cancels and joins each poller before starting the next one.
// Generation tags prevent queued observations from a previous view leaking in.
func Browse(ctx context.Context, input io.Reader, output io.Writer, initial flight.NearbySnapshot, interval time.Duration, scan NearbyRunner, track DetailRunner) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	requests := make(chan browserRequest, 1)
	m := browserModel{nearby: newNearbyModel(initial, interval, output), initial: initial, interval: interval, output: output, generation: 1}
	m.transition = func(req browserRequest) tea.Cmd {
		return func() tea.Msg {
			select {
			case requests <- req:
			case <-runCtx.Done():
			}
			return nil
		}
	}
	program := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(),
		tea.WithContext(runCtx), tea.WithoutSignalHandler(), tea.WithFPS(15))
	ended := make(chan error, 1)
	go func() {
		current := browserRequest{generation: 1}
		var stop context.CancelFunc
		var result chan error
		start := func(req browserRequest) {
			workerCtx, workerCancel := context.WithCancel(runCtx)
			stop = workerCancel
			workerResult := make(chan error, 1)
			result = workerResult
			send := func(snapshot any) error {
				if err := workerCtx.Err(); err != nil {
					return err
				}
				program.Send(browserData{generation: req.generation, snapshot: snapshot})
				return workerCtx.Err()
			}
			go func() {
				var err error
				if req.hex == "" {
					err = scan(workerCtx, func(s flight.NearbySnapshot) error { return send(s) })
				} else {
					err = track(workerCtx, req.selection, func(s flight.Snapshot) error { return send(s) })
				}
				workerResult <- err
			}()
		}
		start(current)
		var lastErr error
		for {
			select {
			case req := <-requests:
				if req.generation <= current.generation {
					continue
				}
				stop()
				if result != nil {
					<-result
				}
				current, lastErr = req, nil
				start(req)
			case err := <-result:
				lastErr, result = err, nil
				program.Send(browserDone{generation: current.generation, err: err})
			case <-runCtx.Done():
				stop()
				if result != nil {
					lastErr = <-result
				}
				if errors.Is(lastErr, context.Canceled) {
					lastErr = nil
				}
				ended <- lastErr
				return
			}
		}
	}()
	_, uiErr := program.Run()
	cancel()
	workerErr := <-ended
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if uiErr != nil {
		return uiErr
	}
	return workerErr
}

type browserModel struct {
	nearby     nearbyModel
	detail     model
	detailMode bool
	initial    flight.NearbySnapshot
	interval   time.Duration
	output     io.Writer
	generation uint64
	transition func(browserRequest) tea.Cmd
}

func (m browserModel) Init() tea.Cmd { return tick() }

func (m browserModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		n, _ := m.nearby.Update(msg)
		m.nearby = n.(nearbyModel)
		d, _ := m.detail.Update(msg)
		m.detail = d.(model)
		return m, nil
	case browserData:
		if msg.generation != m.generation {
			return m, nil
		}
		switch s := msg.snapshot.(type) {
		case flight.NearbySnapshot:
			message = nearbyMsg(s)
		case flight.Snapshot:
			message = snapshotMsg(s)
		}
	case browserDone:
		if msg.generation != m.generation {
			return m, nil
		}
		message = doneMsg{err: msg.err}
	}
	if m.detailMode {
		next, cmd := m.detail.Update(message)
		m.detail = next.(model)
		if m.detail.returnToNearby {
			selected := m.nearby.selected
			width, height := m.nearby.width, m.nearby.height
			m.nearby = newNearbyWithPalette(m.initial, m.interval, m.nearby.styles)
			m.nearby.selected, m.nearby.width, m.nearby.height = selected, width, height
			m.detailMode = false
			m.generation++
			return m, m.transition(browserRequest{generation: m.generation})
		}
		return m, cmd
	}
	next, cmd := m.nearby.Update(message)
	m.nearby = next.(nearbyModel)
	if hex := m.nearby.picked; hex != "" {
		selection := flight.Selection{ObservedAt: m.nearby.snapshot.CheckedAt}
		for _, row := range m.nearby.rows() {
			if row.Hex == hex {
				selection.Aircraft = row.Aircraft
				break
			}
		}
		m.detail = newModelWithPalette(nearby.InitialInspection(selection, m.initial.Source), m.interval, m.nearby.styles)
		m.detail.canReturn = true
		m.detail.width, m.detail.height = m.nearby.width, m.nearby.height
		m.detailMode = true
		m.generation++
		return m, m.transition(browserRequest{generation: m.generation, hex: hex, selection: selection})
	}
	return m, cmd
}

func (m browserModel) View() string {
	if m.detailMode {
		return m.detail.View()
	}
	return m.nearby.View()
}
