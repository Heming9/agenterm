package main

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// uiText has explicit typography without the implicit padding of a form label.
// It still resolves colours and fonts from its surrounding theme on refresh.
type uiText struct {
	widget.BaseWidget
	Text      string
	TextStyle fyne.TextStyle
	Alignment fyne.TextAlign
	size      float32
	colour    fyne.ThemeColorName
}

func newText(text string, size float32, colour fyne.ThemeColorName) *uiText {
	t := &uiText{Text: text, size: size, colour: colour}
	t.ExtendBaseWidget(t)
	return t
}

func (t *uiText) SetText(text string) {
	t.Text = text
	t.Refresh()
}

func (t *uiText) AccessibilityLabel() string             { return t.Text }
func (t *uiText) AccessibilityRole() fyne.AccessibleRole { return fyne.AccessibleRoleText }

func (t *uiText) CreateRenderer() fyne.WidgetRenderer {
	r := &uiTextRenderer{owner: t, glyph: canvas.NewText(t.Text, nil), dot: canvas.NewCircle(nil)}
	r.Refresh()
	return r
}

type uiTextRenderer struct {
	owner *uiText
	glyph *canvas.Text
	dot   *canvas.Circle
}

func (r *uiTextRenderer) Layout(size fyne.Size) {
	height := r.glyph.MinSize().Height
	x := float32(0)
	if r.dot.Visible() {
		x = 14
		r.dot.Move(fyne.NewPos(0, (size.Height-6)/2))
		r.dot.Resize(fyne.NewSize(6, 6))
	}
	r.glyph.Move(fyne.NewPos(x, (size.Height-height)/2))
	r.glyph.Resize(fyne.NewSize(fyne.Max(0, size.Width-x), height))
}

func (r *uiTextRenderer) MinSize() fyne.Size {
	min := r.glyph.MinSize()
	if r.dot.Visible() {
		min.Width += 14
	}
	min.Height = fyne.Max(min.Height, r.owner.size*1.55)
	return min
}

func (r *uiTextRenderer) Refresh() {
	t := r.owner
	th := theme.CurrentForWidget(t)
	r.glyph.Text, r.glyph.TextSize = t.Text, t.size
	if strings.HasPrefix(t.Text, "●") {
		r.glyph.Text = strings.TrimSpace(strings.TrimPrefix(t.Text, "●"))
		r.dot.Show()
	} else {
		r.dot.Hide()
	}
	r.glyph.TextStyle, r.glyph.Alignment = t.TextStyle, t.Alignment
	r.glyph.FontSource = th.Font(t.TextStyle)
	r.glyph.Color = th.Color(t.colour, fyne.CurrentApp().Settings().ThemeVariant())
	r.dot.FillColor = r.glyph.Color
	r.Layout(t.Size())
	r.glyph.Refresh()
	r.dot.Refresh()
}

func (r *uiTextRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.glyph, r.dot} }
func (r *uiTextRenderer) Destroy()                     {}

// Preserve native focus and keyboard behaviour while matching regular-weight
// prototype labels, including the system's Chinese font fallback.
type prototypeButton struct{ widget.Button }

func newButton(text string, icon fyne.Resource, tapped func()) *prototypeButton {
	b := &prototypeButton{Button: widget.Button{Text: text, Icon: icon, OnTapped: tapped}}
	b.ExtendBaseWidget(b)
	return b
}

func (b *prototypeButton) CreateRenderer() fyne.WidgetRenderer {
	r := &prototypeButtonRenderer{WidgetRenderer: b.Button.CreateRenderer()}
	r.Refresh()
	return r
}

type prototypeButtonRenderer struct{ fyne.WidgetRenderer }

func (r *prototypeButtonRenderer) Refresh() {
	r.WidgetRenderer.Refresh()
	for _, object := range r.WidgetRenderer.Objects() {
		if text, ok := object.(*widget.RichText); ok {
			for _, segment := range text.Segments {
				if label, ok := segment.(*widget.TextSegment); ok {
					label.Style.TextStyle.Bold = false
				}
			}
			text.Refresh()
		}
	}
}

// slot fixes small chrome elements to design dimensions, or caps a trailing
// form's width while leaving the window's remaining space as breathing room.
type slotLayout struct {
	width, height float32
	trailing      bool
}

func slot(object fyne.CanvasObject, width, height float32, trailing bool) *fyne.Container {
	return container.New(slotLayout{width: width, height: height, trailing: trailing}, object)
}

func (s slotLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	min := fyne.NewSize(0, 0)
	for _, object := range objects {
		if object.Visible() {
			min = min.Max(object.MinSize())
		}
	}
	if !s.trailing && s.width > 0 {
		min.Width = fyne.Max(min.Width, s.width)
	}
	if s.height > 0 {
		min.Height = fyne.Max(min.Height, s.height)
	}
	return min
}

func (s slotLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width, height := size.Width, size.Height
	if s.width > 0 {
		width = fyne.Min(width, s.width)
	}
	if s.height > 0 {
		height = fyne.Min(height, s.height)
	}
	x := float32(0)
	if s.trailing {
		x = size.Width - width
	}
	for _, object := range objects {
		object.Move(fyne.NewPos(x, (size.Height-height)/2))
		object.Resize(fyne.NewSize(width, height))
	}
}

func lineIcon(name, paths string, gap ...bool) fyne.Resource {
	viewBox := "0 0 24 24"
	if len(gap) > 0 {
		viewBox = "0 0 36 24"
		if gap[0] {
			paths = strings.ReplaceAll(paths, "<path ", `<path transform="translate(12 0)" `)
		}
	}
	return theme.NewThemedResource(fyne.NewStaticResource(name+".svg", []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="`+viewBox+`">`+paths+`</svg>`)))
}

var (
	// Fyne themes fill paths, so the thin strokes are outlined as filled paths
	// instead of relying on inherited SVG stroke attributes that it discards.
	terminalIcon = lineIcon("terminal", `<path d="M4.3 4.5 3 5.8 9.2 12 3 18.2l1.3 1.3 7.5-7.5zM13 18h8v1.8h-8z"/>`)
	unplugIcon   = lineIcon("unplug", `<path d="m4.4 3 16.6 16.6-1.4 1.4L3 4.4zm10.5-.2 1.3 1.3-3.3 3.3-1.3-1.3zm4 4 1.3 1.3-3.3 3.3-1.3-1.3zM9.2 7.6l1.3 1.3-3.4 3.4a3 3 0 0 0 4.3 4.3l3.4-3.4 1.3 1.3-3.4 3.4a4.8 4.8 0 0 1-6.8 0 4.8 4.8 0 0 1 0-6.8zm-3.5 9.7L7 18.6l-3.3 3.3-1.3-1.3z"/>`, false)
	trashIcon    = lineIcon("trash", `<path d="M9 2h6v2H9zM4 5h16v1.5H4zM6 8h1.5v12h9V8H18v13.5H6zM10 9h1.5v9H10zM13 9h1.5v9H13z"/>`, false)
	arrowIcon    = lineIcon("arrow-right", `<path d="m13 4 8 8-8 8-1.4-1.4 5.6-5.6H3v-2h14.2l-5.6-5.6z"/>`, true)
)
