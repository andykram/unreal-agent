package repl

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var contextColors = [...]string{"#87A9F6", "#BCA3F3", "#73C5B4", "#E3B96E", "#F49F9B", "#85BFE8"}

func (report contextReport) view(width, height int, noColor bool) string {
	width = max(1, width)
	color := func(text, shade string, bold bool) string {
		if noColor {
			return text
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(shade)).Bold(bold).Render(text)
	}
	lines := []string{
		color("◈  CONTEXT", contextColors[0], true) + color("   CURRENT SESSION", "#8A91A5", false),
		"",
	}
	if report.usageReported {
		if width < 55 {
			lines = append(lines,
				fmt.Sprintf("Input %d  ·  cached %d", report.inputTokens, report.cachedTokens),
				fmt.Sprintf("Output %d  ·  reasoning %d", report.outputTokens, report.reasoningTokens),
				fmt.Sprintf("%d completed responses", report.requests),
			)
		} else {
			lines = append(lines,
				color(fmt.Sprintf("%d", report.inputTokens), contextColors[0], true)+" input tokens   "+color(fmt.Sprintf("%d", report.outputTokens), contextColors[4], true)+" output tokens",
				fmt.Sprintf("%d responses  ·  %d cached input  ·  %d reasoning output", report.requests, report.cachedTokens, report.reasoningTokens),
			)
		}
	} else {
		lines = append(lines, "Provider token counts have not been reported yet.")
	}
	used := report.totalTokens()
	lines = append(lines, "")
	if report.capacity > 0 {
		available := max(0, report.capacity-used)
		lines = append(lines, fmt.Sprintf("Estimated context  %s / %s tokens  ·  %.1f%% used", formatTokenCount(used), formatTokenCount(report.capacity), 100*float64(used)/float64(report.capacity)))
		lines = append(lines, fmt.Sprintf("Available  %s tokens", formatTokenCount(available)))
		if report.capacitySource != "" {
			lines = append(lines, "Window: "+report.capacitySource)
		}
	} else {
		lines = append(lines, fmt.Sprintf("Estimated context  %s tokens  ·  window unknown", formatTokenCount(used)))
	}
	lines = append(lines, "", color("ESTIMATED CONTEXT TOKENS", "#8A91A5", true))
	barWidth := min(48, max(8, width-4))
	lines = append(lines, report.bar(barWidth, noColor))
	for index, part := range report.parts {
		label := fmt.Sprintf("%-17s %s tokens", part.label, formatTokenCount(part.tokens))
		if report.capacity > 0 {
			label = fmt.Sprintf("%-17s %5.1f%%  %s tokens", part.label, 100*float64(part.tokens)/float64(report.capacity), formatTokenCount(part.tokens))
		}
		if width < 40 {
			label = fmt.Sprintf("%-16s %s tok", part.label, formatTokenCount(part.tokens))
		}
		lines = append(lines, color("● ", contextColors[index%len(contextColors)], true)+label)
	}
	lines = append(lines, "", "Text tokens are estimated at four bytes per token.")
	if report.opaqueItems != 0 {
		lines = append(lines, fmt.Sprintf("%d opaque reasoning items and image payloads are not measured.", report.opaqueItems))
	} else {
		lines = append(lines, "Image payloads are not measured.")
	}
	footer := color("Esc / Enter", contextColors[0], true) + "  close"
	if len(lines) >= height {
		lines = lines[:max(0, height-1)]
	}
	lines = append(lines, footer)
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

func (report contextReport) totalTokens() int {
	total := 0
	for _, part := range report.parts {
		total += part.tokens
	}
	return total
}

func (report contextReport) bar(width int, noColor bool) string {
	total := max(report.capacity, report.totalTokens())
	if total == 0 {
		return strings.Repeat("░", width)
	}
	var result strings.Builder
	previous, run := -1, 0
	flush := func() {
		if run == 0 {
			return
		}
		segment := strings.Repeat("━", run)
		if previous == len(report.parts) {
			segment = strings.Repeat("░", run)
		}
		if !noColor {
			shade := "#8A91A5"
			if previous < len(report.parts) {
				shade = contextColors[previous%len(contextColors)]
			}
			segment = lipgloss.NewStyle().Foreground(lipgloss.Color(shade)).Render(segment)
		}
		result.WriteString(segment)
	}
	for cell := 0; cell < width; cell++ {
		position := (cell*total + total/2) / width
		cumulative, selected := 0, len(report.parts)
		for index, part := range report.parts {
			cumulative += part.tokens
			if position < cumulative {
				selected = index
				break
			}
		}
		if selected != previous {
			flush()
			previous, run = selected, 0
		}
		run++
	}
	flush()
	return result.String()
}

func formatTokenCount(count int) string {
	text := fmt.Sprint(count)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}
