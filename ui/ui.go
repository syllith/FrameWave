package ui

import (
	"bytes"
	"context"
	"path/filepath"

	"fmt"
	"framewave/colormap"
	fynecustom "framewave/fyneCustom"
	"framewave/general"
	"framewave/globals"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// * Backend
var stream chan []byte
var server *http.Server
var stopChan chan bool
var ffmpegCmd *exec.Cmd
var bufferSize = 0
var ffmpegPath = filepath.Join(general.RoamingDir(), "FrameWave", "ffmpeg.exe")

// * Main view
var mainView = container.NewPadded(
	container.NewCenter(
		container.NewVBox(
			streamImg,
			container.New(&fynecustom.MinWidthFormLayout{MinColWidth: 200},
				&widget.Label{Text: "Camera"},
				cameraSelect,
				&widget.Label{Text: "Resolution"},
				resSelect,
				fpsLabel,
				fpsSlider,
				&widget.Label{Text: "Quality"},
				qualitySlider,
				&widget.Label{Text: "Port"},
				portEntry,
			),
			currentBufferLabel,
			currentFpsLabel,
			toggleButton,
		),
	),
)

// * Elements
var streamImg = &fynecustom.CustomImage{
	FixedWidth:  320,
	FixedHeight: 180,
	Image:       &canvas.Image{FillMode: canvas.ImageFillStretch},
}

var fpsSlider = &widget.Slider{
	Min:   1,
	Max:   30,
	Value: 30,
}

var fpsLabel = &widget.Label{
	Text: "FPS",
}

var currentFpsLabel = &canvas.Text{
	Text:     "FPS: N/A",
	Color:    colormap.White,
	TextSize: 14,
}

var currentBufferLabel = &canvas.Text{
	Text:     "Buffer Size: N/A",
	Color:    colormap.OffWhite,
	TextSize: 14,
}

var cameraSelect = &widget.Select{
	PlaceHolder: "Camera",
}

var resSelect = &widget.Select{
	PlaceHolder: "Resolution",
}

var portEntry = &widget.Entry{
	PlaceHolder: "Enter Port",
	Text:        "8080",
}

var toggleButton = &widget.Button{
	Text: "Start",
}

var qualitySlider = &widget.Slider{
	Min:   0,
	Max:   100,
	Value: 100,
}

// . Initalization
func Init() {
	//. Update FPS label on slider move
	fpsSlider.OnChanged = func(f float64) {
		fpsLabel.SetText(fmt.Sprintf("FPS (%v)", int(f)))
	}

	//. Toggle button tapped
	toggleButton.OnTapped = func() {
		if toggleButton.Text == "Start" {
			startStreaming()
		} else {
			stopStreaming()
		}
		toggleButton.Refresh()
	}

	//. Set buffer on resolution change
	resSelect.OnChanged = func(selected string) {
		re := regexp.MustCompile(`(\d+)x(\d+)`)
		matches := re.FindStringSubmatch(selected)

		width, _ := strconv.Atoi(matches[1])
		height, _ := strconv.Atoi(matches[2])

		uncompressedSize := width * height * 24 / 8
		estimatedJPEGSize := uncompressedSize / 10
		bufferSize = estimatedJPEGSize + int(0.3*float64(estimatedJPEGSize))
		currentBufferLabel.Text = fmt.Sprintf("Buffer Size: %s", general.HumanReadableSize(bufferSize))
		currentBufferLabel.Refresh()
	}

	//. Get camera friendly names
	cameraSelect.Options = getCameraNames()

	//. On camera change
	cameraSelect.OnChanged = func(selected string) {
		if cameraSelect.SelectedIndex() > -1 {
			//* Camera found, get resolution
			resolutions := getCameraResolutions(selected)
			resSelect.Options = resolutions
			if len(resolutions) > 0 {
				//* Set default resolution
				resSelect.SetSelected(resolutions[0])
				resSelect.Enable()
			}
		} else {
			//! No cameras found
			resSelect.Options = []string{}
			resSelect.Refresh()
			resSelect.Disable()
		}
	}

	//. Select camera
	if len(cameraSelect.Options) > 0 {
		//* Camera found, select first one
		cameraSelect.SetSelected(cameraSelect.Options[0])
	} else {
		//! No camera selected, disabled resolution
		resSelect.Disable()
	}

	//. Create stream buffer
	stream = make(chan []byte, 100)

	//. Create stream handle
	http.HandleFunc("/", serveMjpeg)

	//. Set window properties
	globals.Win.SetContent(mainView)
	globals.Win.Resize(fyne.NewSize(1, 1))
	globals.Win.SetFixedSize(true)
	globals.Win.CenterOnScreen()
	globals.Win.SetTitle("FrameWave v" + globals.Version)
	globals.Win.SetContent(mainView)
}

// . Server MJPEG stream
func serveMjpeg(w http.ResponseWriter, r *http.Request) {
	const boundary = "frame"

	//* Create response writer
	w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
	w.WriteHeader(http.StatusOK)

	//* Create MIME writer
	mw := multipart.NewWriter(w)
	mw.SetBoundary(boundary)
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "image/jpeg")

	for {
		select {
		case <-r.Context().Done():
			//! Client disconnected
			return
		case jpeg, ok := <-stream:
			if !ok || jpeg == nil {
				//! Failed to send jpeg to stream
				return
			}
			partWriter, err := mw.CreatePart(header)
			if err != nil {
				//! Failed to create MIME part
				log.Println("Error creating MIME part:", err)
				continue
			}
			if _, err = io.Copy(partWriter, bytes.NewReader(jpeg)); err != nil {
				//! Error writing to MIME part
				log.Println("Error writing to MIME part:", err)
			}
		}
	}
}

// . Start streaming
func startStreaming() {
	toggleButton.SetText("Stop")
	streamImg.Show()

	host := "0.0.0.0:" + portEntry.Text
	stopChan = make(chan bool)
	go mjpegCapture(cameraSelect.Selected, host)

	if server == nil {
		//* Create server
		server = &http.Server{Addr: host}
		go func() {
			//* Listen and serve
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				//! Failed to start server
				log.Println("Failed to start server:", err)
			}
		}()
	}
}

// . Stop streaming
func stopStreaming() {
	toggleButton.SetText("Start")
	streamImg.Hide()
	currentFpsLabel.Text = "FPS: N/A"
	currentFpsLabel.Color = colormap.OffWhite
	currentFpsLabel.Refresh()

	go func() {
		//* Signal stop channel
		if stopChan != nil {
			stopChan <- true
			close(stopChan)
			stopChan = nil
			for len(stream) > 0 {
				<-stream
			}
		}

		//* Stop FFmpeg process
		if ffmpegCmd != nil && ffmpegCmd.Process != nil {
			ffmpegCmd.Process.Signal(os.Interrupt)

			if ffmpegCmd.ProcessState == nil || !ffmpegCmd.ProcessState.Exited() {
				ffmpegCmd.Process.Kill()
			}

			ffmpegCmd = nil
		}

		//* Shutdown the HTTP server
		if server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
			server = nil
		}
	}()
}

// . FFMPEG Capture
func mjpegCapture(deviceName, host string) {
	//* Configure FFMPEG
	ffmpegArgs := []string{
		"-f", "dshow",
		"-rtbufsize", strconv.Itoa(bufferSize),
		"-probesize", "32",
		"-analyzeduration", "0",
		"-i", "video=" + deviceName,
		"-pix_fmt", "yuv420p",
		"-color_range", "2",
		"-vf", "scale=in_range=pc:out_range=pc,scale=" + resSelect.Selected + fmt.Sprintf(",fps=%v", fpsSlider.Value),
		"-c:v", "mjpeg",
		"-q:v", strconv.Itoa(100 - int(qualitySlider.Value)),
		"-f", "mjpeg", "-",
	}

	//* Build command

	ffmpegCmd = exec.Command(ffmpegPath, ffmpegArgs...)

	ffmpegCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stderrReader, stderrWriter := io.Pipe()
	ffmpegCmd.Stderr = stderrWriter
	ffmpegOut, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		//! Error setting up stdout pipe
		log.Println("Error setting up stdout pipe:", err)
		return
	}

	//* Start FFMPEG
	if err := ffmpegCmd.Start(); err != nil {
		//! Failed to start FFMPEG
		log.Println("Failed to start FFMPEG:", err)
		return
	}

	//. Monitor FPS frpm stderr
	go func() {
		defer stderrReader.Close()
		reFPS := regexp.MustCompile(`fps=\s*(\d+)`)
		buf := make([]byte, 1024)
		for {
			//* Read from stderr buffer
			n, err := stderrReader.Read(buf)
			if err != nil {
				if err != io.EOF {
					//! Failed to read from stderr
					log.Println("Failed to read error from stderr:", err)
				}
				return
			}

			//* Parse FPS
			matches := reFPS.FindStringSubmatch(string(buf[:n]))
			if len(matches) > 1 {
				intFPS, err := strconv.Atoi(matches[1])
				if err != nil {
					//! Failed to convert FPS to integer
					log.Println("Error converting FPS to integer:", err)
				} else {
					//* Set FPS label and color
					currentFpsLabel.Text = "FPS: " + matches[1]
					currentFpsLabel.Color = general.GetColorForFPS(intFPS)
					currentFpsLabel.Refresh()
				}
			}
		}
	}()

	//. Process Frames
	go func() {
		//* Create JPEG read buffer
		jpegEnd := []byte{0xFF, 0xD9}
		buffer := make([]byte, 0, bufferSize)
		readBuffer := make([]byte, bufferSize)
		for {
			select {
			case <-stopChan:
				//! Stop chan called, close ffmpegOut feed
				ffmpegOut.Close()
				return
			default:
			}

			//* Read data from ffmpegOut into readBuffer
			n, err := ffmpegOut.Read(readBuffer)
			if err != nil {
				return
			}
			buffer = append(buffer, readBuffer[:n]...)

			for {
				//* Find end of JPEG marker in 'buffer'
				idx := bytes.Index(buffer, jpegEnd)
				if idx == -1 {
					break
				}

				//. Extract a complete JPEG frame from buffer
				frame := buffer[:idx+2]
				select {
				case stream <- frame:
					streamImg.SetResource(fyne.NewStaticResource("frame.jpeg", frame))

					streamImg.Refresh()

				case <-stopChan:
					ffmpegOut.Close()
					return
				default:
				}

				//* Remove the processed frame from the 'buffer'
				buffer = buffer[idx+2:]
			}

			//* Limit the 'buffer' size by keeping only the most recent data
			if len(buffer) > bufferSize {
				buffer = buffer[len(buffer)-bufferSize:]
			}
		}
	}()
}

// . Get camera names
func getCameraNames() []string {
	//* Build command
	cmd := exec.Command(ffmpegPath, "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	//* Create buffer
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	//* Run command
	if err := cmd.Run(); err != nil {
		fmt.Println("Command Error:", err)
	}

	//* Parse data
	re := regexp.MustCompile(`"([^"]+)" \(video\)`)
	matches := re.FindAllStringSubmatch(out.String(), -1)

	//* Create slice
	cameraNames := make([]string, len(matches))
	for i, match := range matches {
		cameraNames[i] = match[1]
	}

	return cameraNames
}

// . Get camera resolution
func getCameraResolutions(deviceName string) []string {
	//* Build command
	cmd := exec.Command(ffmpegPath, "-list_options", "true", "-f", "dshow", "-i", "video="+deviceName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	//* Create buffer
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		fmt.Printf("Res Error with device %s: %v. Output: %s\n", deviceName, err, out.String())
	}

	//* Parse data
	re := regexp.MustCompile(`(\d+)x(\d+) fps=(\d+)`)
	matches := re.FindAllStringSubmatch(out.String(), -1)

	//* Get unique resolutions
	uniqueResolutions := make(map[string]bool)
	var resolutions []string
	var maxFps int

	//* Loop through resolutions
	for _, match := range matches {
		width, _ := strconv.Atoi(match[1])
		height, _ := strconv.Atoi(match[2])
		fps, _ := strconv.Atoi(match[3])

		//* Set max FPS
		if fps > maxFps {
			maxFps = fps
		}

		resKey := fmt.Sprintf("%dx%d", width, height)
		if !uniqueResolutions[resKey] {
			//* Unique resolution found
			resolutions = append(resolutions, resKey)
			uniqueResolutions[resKey] = true
		}
	}

	//. Update max FPS label and slider
	fpsSlider.Max = float64(maxFps)
	if maxFps < int(fpsSlider.Value) || fpsLabel.Text == "FPS" {
		fpsSlider.SetValue(float64(maxFps))
		fpsLabel.SetText(fmt.Sprintf("FPS (%v)", int(maxFps)))
	}

	//* Sort resolutions
	sort.Slice(resolutions, func(i, j int) bool {
		partsI := strings.Split(resolutions[i], "x")
		widthI, _ := strconv.Atoi(partsI[0])
		heightI, _ := strconv.Atoi(partsI[1])

		partsJ := strings.Split(resolutions[j], "x")
		widthJ, _ := strconv.Atoi(partsJ[0])
		heightJ, _ := strconv.Atoi(partsJ[1])

		if widthI != widthJ {
			return widthI < widthJ
		}
		return heightI < heightJ
	})

	return resolutions
}
