package fynecustom

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
)

// * Custom form
type MinWidthFormLayout struct {
	MinColWidth float32
}

func (f *MinWidthFormLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	padding := theme.Padding()
	innerPadding := theme.InnerPadding()

	table := f.tableCellsSize(objects, size.Width)
	y := float32(0)
	for i, o := range objects {
		if i%2 == 0 {
			row := i / 2
			tableRow := table[row]

			cellHeight := tableRow[0].Height
			offsetY := (cellHeight - o.MinSize().Height) / 2

			if _, ok := o.(*canvas.Text); ok {
				o.Move(fyne.NewPos(innerPadding, y+offsetY))
				o.Resize(fyne.NewSize(tableRow[0].Width-innerPadding*2, o.MinSize().Height))
			} else {
				o.Move(fyne.NewPos(0, y))
				o.Resize(fyne.NewSize(tableRow[0].Width, tableRow[0].Height))
			}
		} else {
			row := (i - 1) / 2
			tableRow := table[row]
			o.Move(fyne.NewPos(padding+tableRow[0].Width, y))
			o.Resize(fyne.NewSize(tableRow[1].Width, tableRow[0].Height))
			y += tableRow[0].Height + padding
		}
	}
}

func (f *MinWidthFormLayout) tableCellsSize(objects []fyne.CanvasObject, containerWidth float32) [][2]fyne.Size {
	rows := len(objects) / 2
	table := make([][2]fyne.Size, rows)
	labelCellMaxWidth := float32(0)
	contentCellMaxWidth := f.MinColWidth

	for i, o := range objects {
		if i%2 == 0 {
			labelWidth := o.MinSize().Width
			labelCellMaxWidth = max(labelCellMaxWidth, labelWidth)
		} else {
			contentWidth := o.MinSize().Width
			contentCellMaxWidth = max(contentCellMaxWidth, contentWidth)
		}
	}

	for row := 0; row < rows; row++ {
		label := objects[row*2]
		widget := objects[row*2+1]

		rowHeight := max(label.MinSize().Height, widget.MinSize().Height)

		table[row][0] = fyne.NewSize(labelCellMaxWidth, rowHeight)
		table[row][1] = fyne.NewSize(contentCellMaxWidth, rowHeight)
	}

	return table
}

// Utility function to get the maximum of two float32 values
func max(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func (f *MinWidthFormLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	table := f.tableCellsSize(objects, 0)
	padding := theme.Padding()
	minSize := fyne.NewSize(0, 0)

	if len(table) == 0 {
		return minSize
	}

	minSize.Width = table[0][0].Width + table[0][1].Width + padding

	added := false
	for row := 0; row < len(table); row++ {
		minSize.Height += table[row][0].Height
		if added {
			minSize.Height += padding
		}
		added = true
	}
	return minSize
}
