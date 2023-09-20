package ui

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"sync"

	"fmt"
	"framewave/colormap"
	fynecustom "framewave/fyneCustom"
	"framewave/fyneTheme"
	"framewave/general"
	"framewave/globals"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

//go:embed nostream.png
var noStreamImg []byte

// * Backend
type CameraSettings struct {
	Name       string
	Res        string
	FPS        int
	Quality    int
	Port       string
	Enabled    bool
	BufferSize int
	MaxFPS     int
	Brightness int
	Contrast   int
	Saturation int
	Sharpness  int
}

var streams map[string]chan []byte
var servers map[string]*http.Server
var stopChan chan bool
var ffmpegCmds map[string]*exec.Cmd
var ffmpegPath = filepath.Join(general.RoamingDir(), "FrameWave", "ffmpeg.exe")
var cameras []CameraSettings
var selectedCamera string
var ffmpegCmdsMutex sync.Mutex

// * Main view
var mainView = container.NewBorder(container.NewVBox(streamImg, currentFpsLabel), container.NewVBox(toggleButton, openStreamButton), nil, nil, genTabs())

// * Elements
var streamImg = &fynecustom.CustomImage{
	FixedWidth:  384,
	FixedHeight: 216,
	Image:       &canvas.Image{FillMode: canvas.ImageFillOriginal},
}

var toggleButton = &widget.Button{
	Text: "Start",
}

var currentFpsLabel = &canvas.Text{
	Text:      "FPS: N/A",
	Color:     colormap.OffWhite,
	TextSize:  14,
	Alignment: fyne.TextAlignCenter,
}

// . Add a new button for opening the stream URL
var openStreamButton = &widget.Button{
	Text: "Open Stream URL",
	OnTapped: func() {
		// Find the camera settings for the selected camera
		var selectedCameraSettings CameraSettings
		for _, camera := range cameras {
			if camera.Name == selectedCamera {
				selectedCameraSettings = camera
				break
			}
		}

		// Check if a stream channel exists for the selected camera
		if _, streamRunning := streams[selectedCamera]; streamRunning {
			// Construct the stream URL using the selected camera's port
			url, _ := url.Parse("http://127.0.0.1:" + selectedCameraSettings.Port)
			globals.App.OpenURL(url)
		} else {
			// Stream is not running for the selected camera, handle accordingly (e.g., show a message)
			fmt.Println("No stream running for the selected camera.")
		}
	},
}

// . Initalization
func Init() {
	streams = make(map[string]chan []byte)
	servers = make(map[string]*http.Server)
	ffmpegCmds = make(map[string]*exec.Cmd)

	streamImg.SetResource(fyne.NewStaticResource("nostream.png", noStreamImg))
	streamImg.Refresh()

	toggleButton.Disable()
	openStreamButton.Disable()

	//. Set window properties
	globals.Win.SetContent(mainView)
	globals.Win.Resize(fyne.NewSize(1, 1))
	globals.Win.SetFixedSize(true)
	globals.Win.CenterOnScreen()
	globals.Win.SetTitle("FrameWave v" + globals.Version)
	globals.Win.SetContent(mainView)
	globals.App.Settings().SetTheme(fyneTheme.CustomTheme{})

	toggleButton.OnTapped = func() {
		if toggleButton.Text == "Start" {
			startStreaming()
		} else {
			stopStreaming()
		}
	}
}

// . Server MJPEG stream
func serveMjpeg(cameraName string, w http.ResponseWriter, r *http.Request) {
	const boundary = "frame"

	w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
	w.WriteHeader(http.StatusOK)

	mw := multipart.NewWriter(w)
	mw.SetBoundary(boundary)
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "image/jpeg")

	for {
		select {
		case <-r.Context().Done():
			return
		case jpeg, ok := <-streams[cameraName]:
			if !ok || jpeg == nil {
				return
			}
			partWriter, _ := mw.CreatePart(header)
			io.Copy(partWriter, bytes.NewReader(jpeg))
		}
	}
}

// . Start streaming
func startStreaming() {
	toggleButton.SetText("Stop")
	stopChan = make(chan bool)
	general.KillProcByName("ffmpeg.exe")

	for _, camera := range cameras {
		if camera.Enabled {
			streams[camera.Name] = make(chan []byte, 100)

			// Shut down the old server if it exists.
			if server, ok := servers[camera.Name]; ok {
				server.Close()
				delete(servers, camera.Name)
			}

			mux := http.NewServeMux()
			localCamera := camera
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				serveMjpeg(localCamera.Name, w, r) // Use the local copy instead
			})
			server := &http.Server{
				Addr:    "0.0.0.0:" + camera.Port,
				Handler: mux,
			}
			servers[camera.Name] = server
			go server.ListenAndServe()

			go mjpegCapture(camera)
		}
	}

	// Enable the "Open Stream URL" button for the selected camera
	if _, streamRunning := streams[selectedCamera]; streamRunning {
		openStreamButton.Enable()
	}
}

// . Stop streaming
// Stop streaming
func stopStreaming() {
	toggleButton.SetText("Start")
	currentFpsLabel.Text = "FPS: N/A"
	currentFpsLabel.Color = colormap.OffWhite
	currentFpsLabel.Refresh()
	streamImg.SetResource(fyne.NewStaticResource("nostream.png", noStreamImg))
	streamImg.Refresh()
	openStreamButton.Disable()

	// Create a new stop channel and close the old one if it exists
	newStopChan := make(chan bool)
	oldStopChan := stopChan
	stopChan = newStopChan

	go func() {
		// Close the old stop channel outside of this goroutine
		if oldStopChan != nil {
			close(oldStopChan)
		}

		for _, cmd := range ffmpegCmds {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
		ffmpegCmds = make(map[string]*exec.Cmd)

		for key, serv := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			serv.Shutdown(ctx)
			delete(servers, key)
		}
	}()
}

// . FFMPEG Capture
func mjpegCapture(camera CameraSettings) {
	//* Create stream channel
	if _, exists := streams[camera.Name]; !exists {
		streams[camera.Name] = make(chan []byte, 100)
	}
	stream := streams[camera.Name]

	//* Configure FFMPEG
	ffmpegArgs := []string{
		"-f", "dshow",
		"-rtbufsize", "100M",
		"-probesize", "32",
		"-i", "video=" + camera.Name,
		"-pix_fmt", "yuv420p",
		"-color_range", "2",
		"-vf", fmt.Sprintf("scale=in_range=pc:out_range=pc,scale=%s,fps=%v,eq=brightness=%.2f:contrast=%.2f:saturation=%.2f,unsharp=luma_msize_x=3:luma_msize_y=3:luma_amount=%.2f", camera.Res, camera.FPS, (float64(camera.Brightness)-50.0)/50.0, float64(camera.Contrast)/50.0, float64(camera.Saturation)/50.0, (float64(camera.Sharpness)-50.0)/50.0),
		"-c:v", "mjpeg",
		"-loglevel", "verbose",
		"-q:v", strconv.Itoa(2 + (100-camera.Quality)*(31-2)/(100-1)),
		"-f", "mjpeg", "-",
	}

	//* Build command
	ffmpegCmdsMutex.Lock()
	ffmpegCmds[camera.Name] = exec.Command(ffmpegPath, ffmpegArgs...)
	ffmpegCmdsMutex.Unlock()
	ffmpegCmds[camera.Name].SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stderrReader, stderrWriter := io.Pipe()
	ffmpegCmds[camera.Name].Stderr = stderrWriter
	ffmpegOut, err := ffmpegCmds[camera.Name].StdoutPipe()
	if err != nil {
		log.Println("Error setting up stdout pipe for", camera.Name, ":", err)
		return
	}

	//* Start FFMPEG for the specific camera
	ffmpegCmdsMutex.Lock()
	if err := ffmpegCmds[camera.Name].Start(); err != nil {
		log.Println("Failed to start FFMPEG for", camera.Name, ":", err)
		ffmpegCmdsMutex.Unlock()
		return
	}
	ffmpegCmdsMutex.Unlock()

	//. Monitor FPS from stderr
	go monitorFPS(stderrReader, camera)

	//. Process Frames
	go processFrames(ffmpegOut, camera, stream)
}

func monitorFPS(stderrReader io.ReadCloser, camera CameraSettings) {
	defer stderrReader.Close()

	reFPS := regexp.MustCompile(`fps=\s*(\d+)`)

	scanner := bufio.NewScanner(stderrReader)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		line := scanner.Text()
		matches := reFPS.FindStringSubmatch(line)
		if len(matches) > 1 {
			if selectedCamera == camera.Name && toggleButton.Text == "Stop" {
				currentFpsLabel.Text = "FPS: " + matches[1]
				intFPS, _ := strconv.Atoi(matches[1])
				currentFpsLabel.Color = general.GetColorForFPS(intFPS)
				currentFpsLabel.Refresh()
			}
		}
	}
}

func processFrames(ffmpegOut io.ReadCloser, camera CameraSettings, stream chan []byte) {
	jpegEnd := []byte{0xFF, 0xD9}
	reader := bufio.NewReader(ffmpegOut)
	var buffer []byte
	chunk := make([]byte, camera.BufferSize)

	for {
		//* Read bytes chunk by chunk
		n, err := reader.Read(chunk)
		if err != nil {
			return
		}
		buffer = append(buffer, chunk[:n]...)

		//* Check if we have a valid JPEG frame in the buffer
		for {
			idx := bytes.Index(buffer, jpegEnd)
			if idx == -1 {
				break
			}

			frame := buffer[:idx+2]
			buffer = buffer[idx+2:]

			if selectedCamera == camera.Name && toggleButton.Text == "Stop" {
				streamImg.SetResource(fyne.NewStaticResource("frame.jpeg", frame))
				streamImg.Refresh()
			}

			select {
			case stream <- frame:
			case <-stopChan:
				ffmpegOut.Close()
				return
			default:
			}
		}
	}
}

// func processFrames(ffmpegOut io.ReadCloser, camera CameraSettings, stream chan []byte) {
// 	jpegEnd := []byte{0xFF, 0xD9}
// 	var buffer []byte

// 	readBuffer := make([]byte, camera.BufferSize)

// 	for {
// 		startTime := time.Now() // Record the start time before processing each frame

// 		n, err := ffmpegOut.Read(readBuffer)
// 		if err != nil {
// 			return
// 		}
// 		buffer = append(buffer, readBuffer[:n]...)

// 		for {
// 			idx := bytes.Index(buffer, jpegEnd)
// 			if idx == -1 {
// 				break
// 			}

// 			frame := buffer[:idx+2]

// 			if selectedCamera == camera.Name && toggleButton.Text == "Stop" {
// 				streamImg.SetResource(fyne.NewStaticResource("frame.jpeg", frame))
// 				streamImg.Refresh()
// 			}

// 			select {
// 			case stream <- frame:
// 			case <-stopChan:
// 				ffmpegOut.Close()
// 				return
// 			default:
// 			}

// 			buffer = buffer[idx+2:]
// 		}

// 		if len(buffer) > camera.BufferSize {
// 			buffer = buffer[len(buffer)-camera.BufferSize:]
// 		}

// 		endTime := time.Now()                                   // Record the end time after processing each frame
// 		processingTime := endTime.Sub(startTime).Milliseconds() // Calculate the processing time in milliseconds
// 		log.Printf("Frame processing time: %d ms", processingTime)
// 	}
// }

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

// . Generate app tabs for each camera
func genTabs() *container.AppTabs {
	tabs := container.NewAppTabs()
	tabs.OnSelected = func(ti *container.TabItem) {
		currentFpsLabel.Text = "FPS: N/A"
		currentFpsLabel.Color = colormap.OffWhite
		currentFpsLabel.Refresh()

		streamImg.SetResource(fyne.NewStaticResource("nostream.png", noStreamImg))
		streamImg.Refresh()
		selectedCamera = ti.Text // Set the selected camera

		// Enable the "Open Stream URL" button if the selected camera is running
		if _, streamRunning := streams[selectedCamera]; streamRunning {
			openStreamButton.Enable()
		} else {
			openStreamButton.Disable()
		}

		globals.App.Settings().SetTheme(fyneTheme.CustomTheme{})
	}
	tabs.SetTabLocation(container.TabLocationLeading)
	names := getCameraNames()

	for _, name := range names {
		cameras = append(cameras, CameraSettings{Name: name})
		tabs.Append(container.NewTabItem(name, genConfigContainer(name)))
	}

	// Set the selected camera to the first camera initially
	if len(cameras) > 0 {
		selectedCamera = cameras[0].Name
	}

	return tabs
}

// . Generate configuration container for a camera
func genConfigContainer(cameraName string) *fyne.Container {
	var index int
	for i, cam := range cameras {
		if cam.Name == cameraName {
			index = i
			break
		}
	}

	var enabledCheck *widget.Check
	var resSelect *widget.Select
	var fpsLabel = widget.NewLabel(fmt.Sprintf("FPS (%v)", cameras[index].MaxFPS))
	var fpsSlider *widget.Slider
	var qualityLabel = widget.NewLabel("Quality (100)")
	var qualitySlider *widget.Slider
	var portLabel = widget.NewLabel("808" + strconv.Itoa(index))
	var brightnessLabel = widget.NewLabel("Brightness (50)")
	var brightnessSlider *widget.Slider
	var contrastLabel *widget.Label = widget.NewLabel("Contrast (50)")
	var contrastSlider *widget.Slider
	var saturationLabel = widget.NewLabel("Saturation (50)")
	var saturationSlider *widget.Slider
	var sharpnessLabel = widget.NewLabel("Sharpness (50)")
	var sharpnessSlider *widget.Slider

	//. Enabled checkbox
	enabledCheck = &widget.Check{
		OnChanged: func(checked bool) {
			cameras[index].Enabled = checked
			anyCameraEnabled := false
			for _, cam := range cameras {
				if cam.Enabled {
					anyCameraEnabled = true
					break
				}
			}

			if anyCameraEnabled {
				toggleButton.Enable()
			} else {
				toggleButton.Disable()
			}

			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}

		},
	}

	//. Resolution drop down
	resSelect = &widget.Select{
		PlaceHolder: "Resolution",
		Options:     getCameraResolutions(cameraName),
		OnChanged: func(selected string) {
			cameras[index].Res = selected

			re := regexp.MustCompile(`(\d+)x(\d+)`)
			matches := re.FindStringSubmatch(selected)

			width, _ := strconv.Atoi(matches[1])
			height, _ := strconv.Atoi(matches[2])

			uncompressedSize := width * height * 24 / 8
			estimatedJPEGSize := uncompressedSize / 20 // Using 5% of uncompressed size
			cameras[index].BufferSize = estimatedJPEGSize + int(0.2*float64(estimatedJPEGSize))

			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. FPS slider
	fpsSlider = &widget.Slider{
		Min:   2,
		Max:   30,
		Value: 30,
		OnChanged: func(f float64) {
			fpsLabel.SetText(fmt.Sprintf("FPS (%v)", int(f)))
		},
		OnChangeEnded: func(f float64) {
			cameras[index].FPS = int(f)
			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. Qaulity slider
	qualitySlider = &widget.Slider{
		Min:   1,
		Max:   100,
		Value: 100,
		OnChanged: func(q float64) {
			qualityLabel.SetText(fmt.Sprintf("Quality (%v)", int(q)))
		},
		OnChangeEnded: func(q float64) {
			cameras[index].Quality = int(q)
			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. Brightness slider
	brightnessSlider = &widget.Slider{
		Min:   0,
		Max:   100,
		Value: 50,
		OnChanged: func(b float64) {
			brightnessLabel.SetText(fmt.Sprintf("Brightness (%v)", int(b)))
		},
		OnChangeEnded: func(b float64) {
			cameras[index].Brightness = int(b)
			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. Contrast slider
	contrastSlider = &widget.Slider{
		Min:   0,
		Max:   100,
		Value: 50,
		OnChanged: func(c float64) {
			contrastLabel.SetText(fmt.Sprintf("Contrast (%v)", int(c)))
		},
		OnChangeEnded: func(c float64) {
			cameras[index].Contrast = int(c)
			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. Saturation slider
	saturationSlider = &widget.Slider{
		Min:   0,
		Max:   100,
		Value: 50,
		OnChanged: func(s float64) {
			saturationLabel.SetText(fmt.Sprintf("Saturation (%v)", int(s)))
		},
		OnChangeEnded: func(s float64) {
			cameras[index].Saturation = int(s)
			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. Sharpness slider
	sharpnessSlider = &widget.Slider{
		Min:   0,
		Max:   100,
		Value: 50,
		OnChanged: func(sh float64) {
			sharpnessLabel.SetText(fmt.Sprintf("Sharpness (%v)", int(sh)))
		},
		OnChangeEnded: func(sh float64) {
			cameras[index].Sharpness = int(sh)
			if toggleButton.Text == "Stop" {
				stopStreaming()
				startStreaming()
			}
		},
	}

	//. Set default resolutions
	resSelect.SetSelected(resSelect.Options[0])

	//. Set default camera setting
	cameras[len(cameras)-1].Res = resSelect.Options[0]
	cameras[len(cameras)-1].FPS = int(fpsSlider.Value)
	cameras[len(cameras)-1].Quality = int(qualitySlider.Value)
	cameras[len(cameras)-1].Port = portLabel.Text
	cameras[len(cameras)-1].Enabled = enabledCheck.Checked
	cameras[len(cameras)-1].Contrast = int(contrastSlider.Value)
	cameras[len(cameras)-1].Brightness = int(brightnessSlider.Value)
	cameras[len(cameras)-1].Saturation = int(saturationSlider.Value)
	cameras[len(cameras)-1].Sharpness = int(sharpnessSlider.Value)

	return container.NewCenter(
		container.New(&fynecustom.MinWidthFormLayout{MinColWidth: 150},
			&widget.Label{Text: "Enabled"},
			enabledCheck,
			&widget.Label{Text: "Resolution"},
			resSelect,
			fpsLabel,
			fpsSlider,
			qualityLabel,
			qualitySlider,
			brightnessLabel,
			brightnessSlider,
			contrastLabel,
			contrastSlider,
			saturationLabel,
			saturationSlider,
			sharpnessLabel,
			sharpnessSlider,
			&widget.Label{Text: "Port"},
			portLabel,
		),
	)
}
