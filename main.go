package main

import (
	_ "embed"
	"fmt"
	"framewave/general"
	"framewave/globals"
	"framewave/ui"
	_ "net/http/pprof"
)

func main() {
	fmt.Println("Moving FFMPEG")
	general.CreateFfmpeg()
	fmt.Println("Done moving FFMPEG")
	ui.Init()
	globals.Win.ShowAndRun()
}
