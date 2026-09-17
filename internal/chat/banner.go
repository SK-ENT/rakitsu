package chat

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/SK-ENT/rakitsu/internal/brand"
)

// renderBanner draws the startup banner shown once at the top of a new chat
// session's scrollback: the brand mark (internal/brand) beside the
// version/agent/model identity otherwise only visible in the one-line title
// bar. width is the terminal width (Model.width); the banner never exceeds
// it. Returns "" if width is too narrow to fit the (fixed-size) mark without
// wrapping it.
func renderBanner(width int, version, agentName, modelName string) string {
	const minWidth = 50 // mark (34 cols) + spacer + a readable sliver of info text
	if width < minWidth {
		return ""
	}

	glyph := brand.Render()

	var versionLine string
	if version != "" {
		versionLine = titleStyle.Render("rakitsu " + version)
	} else {
		versionLine = titleStyle.Render("rakitsu")
	}
	info := strings.Join([]string{
		versionLine,
		"",
		fmt.Sprintf("Agent: %s", agentName),
		fmt.Sprintf("Model: %s", modelName),
	}, "\n")

	// Center: the mark (17 rows) is much taller than the info block (4
	// lines), so the info needs to sit at its vertical middle, not its top.
	body := lipgloss.JoinHorizontal(lipgloss.Center, glyph, "   ", info)

	// Size the box to its content (+2 cols for Padding(0, 1)) but never
	// wider than the terminal allows (-2 cols for the border itself).
	contentWidth := lipgloss.Width(body)
	boxWidth := contentWidth + 2
	if maxWidth := width - 2; boxWidth > maxWidth {
		boxWidth = maxWidth
	}
	if boxWidth < contentWidth {
		boxWidth = contentWidth // never truncate the content itself
	}
	box := borderStyle.Width(boxWidth).Padding(0, 1).Render(body)
	return box + "\n\n"
}
