package general

import (
	_ "embed"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
)

//go:embed ffmpeg.exe
var ffmpegEXE []byte

func HumanReadableSize(bytes int) string {
	const (
		_          = iota // ignore first value by assigning to blank identifier
		KB float64 = 1 << (10 * iota)
		MB
		GB
		TB
		PB
		EB
	)

	byteFloat := float64(bytes)

	switch {
	case byteFloat >= EB:
		return fmt.Sprintf("%.2f EB", byteFloat/EB)
	case byteFloat >= PB:
		return fmt.Sprintf("%.2f PB", byteFloat/PB)
	case byteFloat >= TB:
		return fmt.Sprintf("%.2f TB", byteFloat/TB)
	case byteFloat >= GB:
		return fmt.Sprintf("%.2f GB", byteFloat/GB)
	case byteFloat >= MB:
		return fmt.Sprintf("%.2f MB", byteFloat/MB)
	case byteFloat >= KB:
		return fmt.Sprintf("%.2f KB", byteFloat/KB)
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

func GetColorForFPS(fps int) color.RGBA {
	if fps <= 15 {
		r := uint8(255)
		g := uint8(255.0 * float64(fps) / 15.0)
		return color.RGBA{r, g, 0, 255}
	} else {
		r := uint8(255.0 - (255.0 * float64(fps-15) / 15.0))
		g := uint8(255)
		return color.RGBA{r, g, 0, 255}
	}
}

func RoamingDir() string {
	roaming, _ := os.UserConfigDir()
	return roaming
}

func CreateFfmpeg() {
	framewavePath := filepath.Join(RoamingDir(), "FrameWave")

	// Check if framewave directory exists, create if not
	if _, err := os.Stat(framewavePath); os.IsNotExist(err) {
		if err := os.Mkdir(framewavePath, 0755); err != nil {
			return
		}
	}

	ffmpegPath := filepath.Join(framewavePath, "ffmpeg.exe")
	if _, err := os.Stat(ffmpegPath); os.IsNotExist(err) {
		// Only write the file if it doesn't exist already
		_ = os.WriteFile(ffmpegPath, ffmpegEXE, 0755)
	}
}
