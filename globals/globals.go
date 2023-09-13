package globals

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

var Version string = "1.00"
var App fyne.App = app.NewWithID("FrameWave")
var Win fyne.Window = App.NewWindow("FrameWave " + Version)
