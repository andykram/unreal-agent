package repl

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The terminal supplies the typeface. Weight separates identity from utility text.
type terminalPalette struct{ text, muted, lilac, accent, rule, chip string }

func (model *uiModel) palette() terminalPalette {
	if model.config.Current().Appearance.Theme == "light" {
		return terminalPalette{"#283248", "#657086", "#69539D", "#9A562C", "#CBD0DC", "#E9E4F3"}
	}
	return terminalPalette{"#DDE3F0", "#929DB3", "#C2AEED", "#E5AF83", "#465168", "#302B43"}
}

// hexSGR converts a palette #RRGGBB color to an SGR parameter sequence for
// inline editor spans. It returns "" for colors it cannot parse.
func hexSGR(hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	red, err := strconv.ParseUint(hex[1:3], 16, 8)
	if err != nil {
		return ""
	}
	green, err := strconv.ParseUint(hex[3:5], 16, 8)
	if err != nil {
		return ""
	}
	blue, err := strconv.ParseUint(hex[5:7], 16, 8)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("38;2;%d;%d;%d", red, green, blue)
}

func (model *uiModel) ink(text, color string, bold bool) string {
	if model.noColor {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(bold).Render(text)
}

func (model *uiModel) composerRule(width int) string {
	label := " Compose "
	if !model.followTranscript && model.transcriptTop+model.transcriptHeight < len(model.transcriptRows) {
		label = " Scrollback · ctrl+end to return "
	}
	if model.busy {
		label = " Compose · enter to queue "
	}
	return model.ink(ansi.Truncate("╭─"+label+strings.Repeat("─", max(0, width-ansi.StringWidth(label)-2)), width, ""), model.palette().rule, false)
}

var imageChipPattern = regexp.MustCompile(`\[Image #[0-9]+\]`)
var attachmentEnvelope = regexp.MustCompile(`(?s)\n\n\[Attached image [0-9]+: "(?:[^"\\]|\\.)*"; [0-9]+ x [0-9]+; [a-z]+\]\nCall ViewImage with this local path before interpreting the image\. This is a file attachment, not an inline image message\.`)

func (model *uiModel) styleImageChips(line string) string {
	if model.noColor {
		return line
	}
	palette := model.palette()
	return imageChipPattern.ReplaceAllStringFunc(line, func(chip string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(palette.lilac)).Background(lipgloss.Color(palette.chip)).Bold(true).Render(chip)
	})
}

func (model *uiModel) addAttachment(attachment Attachment) {
	label := fmt.Sprintf("[Image #%d]", len(model.attachments)+1)
	if err := model.draft.Insert(label + " "); err != nil {
		model.message = err.Error()
		if !attachment.Submitted {
			_ = os.Remove(attachment.Path)
		}
		return
	}
	model.attachments = append(model.attachments, attachment)
	model.message = ""
	model.completion = nil
}

func (model *uiModel) removeImageChip(index int) {
	label := fmt.Sprintf("[Image #%d]", index)
	raw := model.draft.Source()
	at := strings.Index(raw, label)
	if at < 0 {
		return
	}
	cursor := model.draft.Cursor()
	if cursor > at {
		cursor = max(at, cursor-len(label))
	}
	_ = model.draft.Restore(raw[:at]+raw[at+len(label):], cursor)
}

func displayAttachmentPrompt(prompt string) string {
	count := len(attachmentEnvelope.FindAllStringIndex(prompt, -1))
	prompt = attachmentEnvelope.ReplaceAllString(prompt, "")
	for i := 1; i <= count; i++ {
		chip := fmt.Sprintf("[Image #%d]", i)
		if !strings.Contains(prompt, chip) {
			prompt += " " + chip
		}
	}
	return strings.TrimSpace(prompt)
}

// Older history entries stored attachments separately from their prompt text.
func (model *uiModel) ensureImageChips() {
	raw := model.draft.Source()
	cursor := model.draft.Cursor()
	for index := range model.attachments {
		label := fmt.Sprintf("[Image #%d]", index+1)
		if !strings.Contains(raw, label) {
			raw += " " + label
		}
	}
	_ = model.draft.Restore(raw, cursor)
}
