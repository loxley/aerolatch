package dashboard

import (
	"context"
	"fmt"
	"io"
	"strings"

	"aerolatch/internal/flight"

	tea "github.com/charmbracelet/bubbletea"
)

// ChoosePlace uses a cancellable terminal picker. JSON/nonterminal callers
// instead receive candidate IDs from the CLI and never consume stdin.
func ChoosePlace(ctx context.Context, input io.Reader, output io.Writer, places []flight.Location) (*flight.Location, error) {
	m := placeModel{places: places, width: 80, height: 24}
	final, err := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(),
		tea.WithContext(ctx), tea.WithoutSignalHandler()).Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if result, ok := final.(placeModel); ok && result.chosen {
		place := result.places[result.index]
		return &place, nil
	}
	return nil, nil
}

type placeModel struct {
	places        []flight.Location
	index         int
	width, height int
	chosen        bool
}

func (m placeModel) Init() tea.Cmd { return nil }

func (m placeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.index = max(0, m.index-1)
		case "down", "j":
			m.index = min(max(0, len(m.places)-1), m.index+1)
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if len(m.places) > 0 {
				m.chosen = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m placeModel) View() string {
	lines := []string{"AEROLATCH / CHOOSE LOCATION", "Several places match. Distances use the selected city point.", ""}
	count := max(1, m.height-6)
	offset := max(0, m.index-count+1)
	for i := offset; i < min(len(m.places), offset+count); i++ {
		marker := "  "
		if i == m.index {
			marker = "▸ "
		}
		p := m.places[i]
		lines = append(lines, fmt.Sprintf("%s%s · %.5f, %.5f · ID %d", marker, clean(p.Label()), p.Latitude, p.Longitude, p.ID))
	}
	lines = append(lines, "", "↑↓ / j k select · Enter choose · Esc / q quit")
	return bounded(strings.Join(lines, "\n"), m.width, m.height)
}
