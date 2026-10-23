// Package dashboard presents tracker snapshots in an interactive terminal.
// All flight interpretation and network polling remain in the tracker.
package dashboard

import (
	"context"
	"errors"
	"io"
	"time"

	"aerolatch/internal/flight"

	tea "github.com/charmbracelet/bubbletea"
)

// Runner must honor cancellation, including while emitting snapshots.
type Runner func(context.Context, func(flight.Snapshot) error) error

// Run owns the terminal and joins the tracker before returning. Backpressure
// preserves status transitions; cancellation unblocks sends and HTTP requests.
func Run(ctx context.Context, input io.Reader, output io.Writer, initial flight.Snapshot, interval time.Duration, track Runner) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	program := tea.NewProgram(newModel(initial, interval, output),
		tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(),
		tea.WithContext(runCtx), tea.WithoutSignalHandler(), tea.WithFPS(15))
	result := make(chan error, 1)
	go func() {
		err := track(runCtx, func(s flight.Snapshot) error {
			if err := runCtx.Err(); err != nil {
				return err
			}
			program.Send(snapshotMsg(s))
			return runCtx.Err()
		})
		result <- err
		program.Send(doneMsg{err: err})
	}()
	_, uiErr := program.Run()
	cancel()
	trackErr := <-result
	if err := ctx.Err(); err != nil {
		return err
	}
	if uiErr != nil {
		return uiErr
	}
	if trackErr != nil && !errors.Is(trackErr, context.Canceled) {
		return trackErr
	}
	return nil
}
