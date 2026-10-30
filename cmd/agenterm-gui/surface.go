package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// surface keeps rounded backgrounds and borders in sync with system appearance.
type surface struct {
	widget.BaseWidget
	content fyne.CanvasObject
	radius  float32
	fill    fyne.ThemeColorName
	border  bool
}

func newSurface(content fyne.CanvasObject, radius float32) *surface {
	s := &surface{content: content, radius: radius, fill: theme.ColorNameBackground, border: true}
	s.ExtendBaseWidget(s)
	return s
}

func (s *surface) CreateRenderer() fyne.WidgetRenderer {
	background := canvas.NewRectangle(nil)
	background.CornerRadius = s.radius
	background.StrokeWidth = 1
	r := &surfaceRenderer{surface: s, background: background}
	r.Refresh()
	return r
}

type surfaceRenderer struct {
	surface    *surface
	background *canvas.Rectangle
}

func (r *surfaceRenderer) Layout(size fyne.Size) {
	r.background.Resize(size)
	r.surface.content.Move(fyne.NewPos(1, 1))
	r.surface.content.Resize(fyne.NewSize(size.Width-2, size.Height-2))
}

func (r *surfaceRenderer) MinSize() fyne.Size {
	return r.surface.content.MinSize().Add(fyne.NewSize(2, 2))
}

func (r *surfaceRenderer) Refresh() {
	t := theme.CurrentForWidget(r.surface)
	v := fyne.CurrentApp().Settings().ThemeVariant()
	r.background.FillColor = t.Color(r.surface.fill, v)
	r.background.StrokeColor = t.Color(theme.ColorNameSeparator, v)
	r.background.StrokeWidth = 0
	if r.surface.border {
		r.background.StrokeWidth = 1
	}
	r.background.Refresh()
	r.surface.content.Refresh()
}

func (r *surfaceRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.background, r.surface.content}
}

func (r *surfaceRenderer) Destroy() {}
