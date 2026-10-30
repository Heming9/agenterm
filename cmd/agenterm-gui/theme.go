package main

import (
	"image/color"
	"os"
	"path/filepath"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type monochromeTheme struct {
	fyne.Theme
	terminal bool
	inactive bool
}

const (
	colourSystem  fyne.ThemeColorName = "agentermLogSystem"
	colourCommand fyne.ThemeColorName = "agentermLogCommand"
	colourTag     fyne.ThemeColorName = "agentermTag"
	colourTagText fyne.ThemeColorName = "agentermTagText"
)

var systemSans, systemMono fyne.Resource

// The transparent label handles selection over the identically laid-out
// colored text underneath. Its selection highlight remains visible.
type logSelectionTheme struct{ fyne.Theme }

func (t logSelectionTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameForeground {
		return color.NRGBA{}
	}
	if name == theme.ColorNameSelection {
		return color.NRGBA{R: 80, G: 135, B: 180, A: 110}
	}
	return t.Theme.Color(name, variant)
}

type compactControlTheme struct{ fyne.Theme }

type buttonTheme struct{ fyne.Theme }

// Fyne anchors single-line entries using inner padding rather than centering
// their text when stretched. Match that padding to the fixed control height so
// placeholder, typed text, selection and caret share the same vertical centre.
type entryTheme struct {
	fyne.Theme
	height float32
}

func (t entryTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNameInnerPadding {
		line := fyne.MeasureText("M", t.Theme.Size(theme.SizeNameText), fyne.TextStyle{}).Height
		return fyne.Max(0, (t.height-line)/2)
	}
	return t.Theme.Size(name)
}

func (t buttonTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return fyne.CurrentApp().Settings().Theme().Color(name, variant)
}

func (t entryTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return fyne.CurrentApp().Settings().Theme().Color(name, variant)
}

func (t compactControlTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return fyne.CurrentApp().Settings().Theme().Color(name, variant)
}

func (t buttonTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNameInlineIcon {
		return 20
	}
	if name == theme.SizeNamePadding {
		return 7
	}
	return t.Theme.Size(name)
}

func (t compactControlTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNameInlineIcon {
		return 20
	}
	if name == theme.SizeNameInnerPadding {
		return 2
	}
	if name == theme.SizeNamePadding {
		return 7
	}
	return t.Theme.Size(name)
}

func init() {
	if runtime.GOOS == "darwin" && os.Getenv("FYNE_FONT") == "" {
		// SFNS lacks the UI's Chinese glyphs. Fyne resolves missing Han through
		// per-run fallback fonts, whose ascents can differ within one label.
		// Prefer an installed CJK sans face so the whole UI shares one baseline.
		candidates := []string{
			"/System/Library/Fonts/STHEITI.ttf",
			"/Library/Fonts/Arial Unicode.ttf",
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
		}
		assets, _ := filepath.Glob("/System/Library/AssetsV2/com_apple_MobileAsset_Font*/*/AssetData/STHEITI.ttf")
		candidates = append(assets, candidates...)
		for _, path := range candidates {
			resource, err := fyne.LoadResourceFromPath(path)
			if err == nil {
				systemSans = resource
				break
			}
		}
	}
	if runtime.GOOS == "darwin" && os.Getenv("FYNE_FONT_MONOSPACE") == "" {
		systemMono, _ = fyne.LoadResourceFromPath("/System/Library/Fonts/SFNSMono.ttf")
	}
}

func newMonochromeTheme(terminal bool) fyne.Theme {
	return &monochromeTheme{Theme: theme.DefaultTheme(), terminal: terminal}
}

func gray(value uint8) color.Color {
	return color.NRGBA{R: value, G: value, B: value, A: 255}
}

func (t *monochromeTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	dark := t.terminal && !t.inactive || variant == theme.VariantDark
	bg, fg, muted, line, soft := gray(255), gray(23), gray(115), gray(229), gray(245)
	if dark {
		bg, fg, muted, line, soft = gray(17), gray(237), gray(163), gray(43), gray(26)
	}
	if t.terminal && !t.inactive {
		bg = gray(10)
		if variant == theme.VariantLight {
			bg = gray(16)
		}
	} else if t.inactive {
		bg = soft
	}
	switch name {
	case colourSystem:
		if dark {
			return color.NRGBA{R: 153, G: 177, B: 195, A: 255}
		}
		return color.NRGBA{R: 75, G: 103, B: 122, A: 255}
	case colourCommand:
		if dark {
			return color.NRGBA{R: 133, G: 201, B: 155, A: 255}
		}
		return color.NRGBA{R: 37, G: 116, B: 69, A: 255}
	case colourTag:
		if dark {
			return color.NRGBA{R: 25, G: 65, B: 43, A: 255}
		}
		return color.NRGBA{R: 230, G: 247, B: 234, A: 255}
	case colourTagText:
		if dark {
			return color.NRGBA{R: 159, G: 224, B: 174, A: 255}
		}
		return color.NRGBA{R: 47, G: 133, B: 74, A: 255}
	case theme.ColorNameBackground, theme.ColorNameInputBackground:
		return bg
	case theme.ColorNameForeground, theme.ColorNamePrimary, theme.ColorNameHyperlink,
		theme.ColorNameSuccess, theme.ColorNameWarning:
		return fg
	case theme.ColorNameError:
		if dark {
			return color.NRGBA{R: 255, G: 138, B: 138, A: 255}
		}
		return color.NRGBA{R: 197, G: 48, B: 48, A: 255}
	case theme.ColorNameFocus:
		if dark {
			return color.NRGBA{R: 255, G: 255, B: 255, A: 40}
		}
		return color.NRGBA{A: 32}
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnError,
		theme.ColorNameForegroundOnSuccess, theme.ColorNameForegroundOnWarning:
		return bg
	case theme.ColorNameButton:
		return bg
	case theme.ColorNameMenuBackground,
		theme.ColorNameOverlayBackground, theme.ColorNameHeaderBackground:
		return soft
	case theme.ColorNameInputBorder, theme.ColorNameSeparator,
		theme.ColorNameInnerWindowBorder, theme.ColorNameInnerWindowBorderInactive:
		return line
	case theme.ColorNamePlaceHolder, theme.ColorNameDisabled, theme.ColorNameScrollBar:
		return muted
	case theme.ColorNameDisabledButton, theme.ColorNameScrollBarBackground:
		return soft
	case theme.ColorNameHover, theme.ColorNameSelection, theme.ColorNamePressed:
		if dark {
			return gray(60)
		}
		return gray(216)
	case theme.ColorNameShadow:
		return color.NRGBA{A: 20}
	default:
		return t.Theme.Color(name, variant)
	}
}

func (t *monochromeTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 12
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameHeadingText:
		return 22
	case theme.SizeNameInnerPadding:
		if t.terminal {
			return 0
		}
		return 6
	case theme.SizeNamePadding:
		return 0
	case theme.SizeNameInlineIcon:
		return 14
	case theme.SizeNameLineSpacing:
		return 5
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius:
		return 7
	case theme.SizeNameInputBorder, theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameScrollBar:
		return 8
	default:
		return t.Theme.Size(name)
	}
}

func (t *monochromeTheme) Font(style fyne.TextStyle) fyne.Resource {
	if style.Monospace && systemMono != nil {
		return systemMono
	}
	if !style.Monospace && !style.Symbol && systemSans != nil {
		return systemSans
	}
	return t.Theme.Font(style)
}
