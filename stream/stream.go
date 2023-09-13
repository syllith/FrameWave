package stream

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"fyne.io/fyne/v2/canvas"
	"github.com/hybridgroup/mjpeg"
	"gocv.io/x/gocv"
)

var (
	deviceID  int
	err       error
	webcam    *gocv.VideoCapture
	stream    *mjpeg.Stream // Define the stream variable here
	streaming bool
	mu        sync.Mutex
)

func StartStreaming(deviceID, host string, fpsLabel *canvas.Text) {
	// Open the webcam with the specified deviceID
	var err error // Declare err here to use webcam in the outer scope
	webcam, err = gocv.OpenVideoCapture(deviceID)
	if err != nil {
		fmt.Printf("Error opening capture device: %v\n", deviceID)
		return
	}
	defer webcam.Close()

	// Configure webcam settings, set resolutions, etc.

	// Create the mjpeg stream
	stream = mjpeg.NewStream()

	// Start capturing
	go mjpegCapture(fpsLabel)

	fmt.Println("Streaming started. Point your browser to " + host)

	// Start HTTP server
	http.Handle("/", stream)
	log.Fatal(http.ListenAndServe(host, nil))
}

func StopStreaming() {
	// Lock to ensure safe access to the streaming state
	mu.Lock()
	defer mu.Unlock()

	// Check if streaming is already stopped
	if !streaming {
		return
	}

	// Set the streaming flag to false to indicate that streaming is stopped
	streaming = false

	// Perform any additional logic to stop the stream if needed
	// For example, you can close the webcam and release other resources
	if webcam != nil {
		webcam.Close()
	}

	fmt.Println("Streaming stopped.")
}

func mjpegCapture(fpsLbl *canvas.Text) {
	img := gocv.NewMat()
	defer img.Close()

	// Parameters for JPEG encoding, 50 is the quality setting
	params := []int{gocv.IMWriteJpegQuality, 50}

	frameCounter := 0
	lastTime := time.Now()

	for {
		if ok := webcam.Read(&img); !ok {
			fmt.Printf("Device closed: %v\n", deviceID)
			return
		}
		if img.Empty() {
			continue
		}

		buf, _ := gocv.IMEncodeWithParams(".jpg", img, params)
		stream.UpdateJPEG(buf.GetBytes())
		buf.Close()

		// Update the frame counter and calculate the framerate
		frameCounter++
		currentTime := time.Now()
		elapsedTime := currentTime.Sub(lastTime).Seconds()
		if elapsedTime >= 1.0 {
			framerate := float64(frameCounter) / elapsedTime
			frameCounter = 0
			lastTime = currentTime

			// Send the FPS update to the channel
			fpsLbl.Text = fmt.Sprintf("FPS: %v", framerate)
			fpsLbl.Refresh()
		}
	}
}
