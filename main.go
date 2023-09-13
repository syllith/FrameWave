package main

import (
	"framewave/general"
	"framewave/globals"
	"framewave/ui"
	_ "net/http/pprof"
)

func main() {
	general.CreateFfmpeg()
	ui.Init()
	globals.Win.ShowAndRun()
}
