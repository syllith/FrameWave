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
type CameraSettings struct {
	Name       string
	Res        string
	FPS        int
	Quality    int
	Port       int
	Enabled    bool
	BufferSize int
	MaxFPS     int
}

var stream chan []byte
var server *http.Server
var stopChan chan bool
var ffmpegCmd *exec.Cmd
var ffmpegPath = filepath.Join(general.RoamingDir(), "FrameWave", "ffmpeg.exe")
var cameras []CameraSettings

// * Main viewd
var mainView = container.NewBorder(nil, toggleButton, nil, nil, getTabs())

// * Elements
var streamImg = &fynecustom.CustomImage{
	FixedWidth:  320,
	FixedHeight: 180,
	Image:       &canvas.Image{FillMode: canvas.ImageFillStretch},
}

var toggleButton = &widget.Button{
	Text: "Start",
}

var currentFpsLabel = &canvas.Text{
	Text:     "FPS: N/A",
	Color:    colormap.OffWhite,
	TextSize: 14,
}

// . Initalization
func Init() {
	go func() {
		for {
			time.Sleep(3 * time.Second)
			fmt.Println(cameras)
		}
	}()
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

	for _, camera := range cameras {
		if camera.Enabled {
			host := "0.0.0.0:" + strconv.Itoa(camera.Port)
			go mjpegCapture(camera)

			if server == nil {
				// Create server
				server = &http.Server{Addr: host}
				go func() {
					if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						log.Println("Failed to start server:", err)
					}
				}()
			}
		}
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
func mjpegCapture(camera CameraSettings) {
	//* Configure FFMPEG
	ffmpegArgs := []string{
		"-f", "dshow",
		"-i", fmt.Sprintf("video=%s", camera.Name),
		"-vf", fmt.Sprintf("fps=%d", camera.FPS),
		"-video_size", camera.Res,
		"-bufsize", fmt.Sprintf("%d", camera.BufferSize),
		"-c:v", "mjpeg",
		"-q:v", "5",
		"-f", "mjpeg",
		"-",
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
		buffer := make([]byte, 0, camera.BufferSize)
		readBuffer := make([]byte, camera.BufferSize)
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
			if len(buffer) > camera.BufferSize {
				buffer = buffer[len(buffer)-camera.BufferSize:]
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
	for i, cam := range cameras {
		if cam.Name == deviceName {
			cameras[i].MaxFPS = maxFps
			break
		}
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

func genCameraContainer(cameraName string) *fyne.Container {
	//. Create enabled checkbox
	enabledCheck := &widget.Check{
		OnChanged: func(checked bool) {
			for i, cam := range cameras {
				if cam.Name == cameraName {
					cameras[i].Enabled = checked
					break
				}
			}
		},
	}

	//. Create resolution drop down
	resSelect := &widget.Select{
		PlaceHolder: "Resolution",
		Options:     getCameraResolutions(cameraName),
		OnChanged: func(selected string) {
			for i, cam := range cameras {
				if cam.Name == cameraName {
					cameras[i].Res = selected

					re := regexp.MustCompile(`(\d+)x(\d+)`)
					matches := re.FindStringSubmatch(selected)

					width, _ := strconv.Atoi(matches[1])
					height, _ := strconv.Atoi(matches[2])

					uncompressedSize := width * height * 24 / 8
					estimatedJPEGSize := uncompressedSize / 10
					cameras[i].BufferSize = estimatedJPEGSize + int(0.3*float64(estimatedJPEGSize))
					break
				}
			}
		},
	}

	//. Create FPS slider and label
	fpsSlider := &widget.Slider{
		Min:   1,
		Max:   30,
		Value: 30,
	}

	fpsLabel := &widget.Label{
		Text: fmt.Sprintf("FPS (%v)", int(fpsSlider.Value)),
	}

	//. Create quality slider
	qualitySlider := &widget.Slider{
		Min:   0,
		Max:   100,
		Value: 100,
		OnChanged: func(f float64) {
			for i, cam := range cameras {
				if cam.Name == cameraName {
					cameras[i].Quality = int(f)
					break
				}
			}
		},
	}

	//. Create port entry
	portEntry := &widget.Entry{
		PlaceHolder: "Enter Port",
		Text:        "8080",
	}

	//. Set default resolutions
	if resSelect.Options != nil && len(resSelect.Options) > 0 {
		resSelect.SetSelected(resSelect.Options[0])
	}

	//. Update FPS label on slider move
	fpsSlider.OnChanged = func(f float64) {
		fpsLabel.SetText(fmt.Sprintf("FPS (%v)", int(f)))
		for i, cam := range cameras {
			if cam.Name == cameraName {
				cameras[i].FPS = int(f)
				break
			}
		}
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

	// Return the VBox containing all these widgets for this camera.
	return container.NewPadded(
		container.New(&fynecustom.MinWidthFormLayout{MinColWidth: 200},
			&widget.Label{Text: "Enabled"},
			enabledCheck,
			&widget.Label{Text: "Resolution"},
			resSelect,
			fpsLabel,
			fpsSlider,
			&widget.Label{Text: "Quality"},
			qualitySlider,
			&widget.Label{Text: "Port"},
			portEntry,
		),
	)
}

// Dynamic function to get the tabs based on available cameras
func getTabs() *container.AppTabs {
	tabs := container.NewAppTabs()
	tabs.SetTabLocation(container.TabLocationLeading)
	cameraNames := getCameraNames()

	for _, name := range cameraNames {
		cameras = append(cameras, CameraSettings{Name: name})
		tabs.Append(container.NewTabItem(name, genCameraContainer(name)))
	}

	return tabs
}
