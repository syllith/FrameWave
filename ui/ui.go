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
}

var streams map[string]chan []byte
var servers map[string]*http.Server
var stopChan chan bool
var ffmpegCmds map[string]*exec.Cmd
var ffmpegPath = filepath.Join(general.RoamingDir(), "FrameWave", "ffmpeg.exe")
var cameras []CameraSettings
var selectedCamera string
var cameraConfigElements []fyne.Disableable

// * Main view
var mainView = container.NewBorder(container.NewVBox(streamImg, currentFpsLabel), toggleButton, nil, nil, getTabs())

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

// . Initalization
func Init() {
	streams = make(map[string]chan []byte)
	servers = make(map[string]*http.Server)
	ffmpegCmds = make(map[string]*exec.Cmd)

	streamImg.SetResource(fyne.NewStaticResource("nostream.png", noStreamImg))
	streamImg.Refresh()

	toggleButton.Disable()

	//. Set window properties
	globals.Win.SetContent(mainView)
	globals.Win.Resize(fyne.NewSize(1, 1))
	globals.Win.SetFixedSize(true)
	globals.Win.CenterOnScreen()
	globals.Win.SetTitle("FrameWave v" + globals.Version)
	globals.Win.SetContent(mainView)
	globals.App.Settings().SetTheme(fyneTheme.CustomTheme{})
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

	for _, elem := range cameraConfigElements {
		elem.Disable()
	}

	for _, camera := range cameras {
		if camera.Enabled {
			streams[camera.Name] = make(chan []byte, 100)

			if _, ok := servers[camera.Name]; !ok {
				mux := http.NewServeMux()
				mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					serveMjpeg(camera.Name, w, r)
				})
				server := &http.Server{
					Addr:    "0.0.0.0:" + camera.Port,
					Handler: mux,
				}
				servers[camera.Name] = server
				go server.ListenAndServe()
			}
			go mjpegCapture(camera)
		}
	}
}

// . Stop streaming
func stopStreaming() {
	toggleButton.SetText("Start")
	currentFpsLabel.Text = "FPS: N/A"
	currentFpsLabel.Color = colormap.OffWhite
	currentFpsLabel.Refresh()
	streamImg.SetResource(fyne.NewStaticResource("nostream.png", noStreamImg))
	streamImg.Refresh()

	for _, elem := range cameraConfigElements {
		elem.Enable()
	}

	go func() {
		stopChan <- true
		close(stopChan)
		stopChan = nil

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
		"-vf", "scale=in_range=pc:out_range=pc,scale=" + camera.Res + fmt.Sprintf(",fps=%v", camera.FPS),
		"-c:v", "mjpeg",
		"-loglevel", "verbose",
		"-q:v", strconv.Itoa(2 + (100-camera.Quality)*(31-2)/(100-1)),
		"-f", "mjpeg", "-",
	}

	//* Build command
	ffmpegCmds[camera.Name] = exec.Command(ffmpegPath, ffmpegArgs...)
	ffmpegCmds[camera.Name].SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stderrReader, stderrWriter := io.Pipe()
	ffmpegCmds[camera.Name].Stderr = stderrWriter
	ffmpegOut, err := ffmpegCmds[camera.Name].StdoutPipe()
	if err != nil {
		log.Println("Error setting up stdout pipe for", camera.Name, ":", err)
		return
	}

	//* Start FFMPEG for the specific camera
	if err := ffmpegCmds[camera.Name].Start(); err != nil {
		log.Println("Failed to start FFMPEG for", camera.Name, ":", err)
		return
	}

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
	var bufferPool = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

	buf := bufferPool.Get().(*bytes.Buffer)
	defer bufferPool.Put(buf)

	readBuffer := make([]byte, camera.BufferSize)

	for {
		bytesToRead := camera.BufferSize - buf.Len()
		if bytesToRead > 0 {
			n, _ := ffmpegOut.Read(readBuffer[:bytesToRead])
			buf.Write(readBuffer[:n])
		}

		bufferBytes := buf.Bytes()

		for {
			idx := bytes.Index(bufferBytes, jpegEnd)
			if idx == -1 {
				break
			}

			frame := bufferBytes[:idx+2]

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

			buf.Next(idx + 2)
			bufferBytes = buf.Bytes()

			if buf.Len() > camera.BufferSize {
				remainingBytes := buf.Bytes()[buf.Len()-camera.BufferSize:]
				buf.Reset()
				buf.Write(remainingBytes)
			}
		}
	}
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

// . Generate app tabs for each camera
func getTabs() *container.AppTabs {
	tabs := container.NewAppTabs()
	tabs.OnSelected = func(ti *container.TabItem) {
		currentFpsLabel.Text = "FPS: N/A"
		currentFpsLabel.Color = colormap.OffWhite
		currentFpsLabel.Refresh()

		streamImg.SetResource(fyne.NewStaticResource("nostream.png", noStreamImg))
		streamImg.Refresh()
		selectedCamera = ti.Text
	}
	tabs.SetTabLocation(container.TabLocationLeading)
	names := getCameraNames()

	for _, name := range names {
		cameras = append(cameras, CameraSettings{Name: name})
		tabs.Append(container.NewTabItem(name, genConfigContainer(name)))
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

	//. Create enabled checkbox
	enabledCheck := &widget.Check{
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
		},
	}
	cameraConfigElements = append(cameraConfigElements, enabledCheck)

	//. Create resolution drop down
	resSelect := &widget.Select{
		PlaceHolder: "Resolution",
		Options:     getCameraResolutions(cameraName),
		OnChanged: func(selected string) {
			cameras[index].Res = selected

			re := regexp.MustCompile(`(\d+)x(\d+)`)
			matches := re.FindStringSubmatch(selected)

			width, _ := strconv.Atoi(matches[1])
			height, _ := strconv.Atoi(matches[2])

			uncompressedSize := width * height * 24 / 8
			estimatedJPEGSize := uncompressedSize / 10
			cameras[index].BufferSize = estimatedJPEGSize + int(0.3*float64(estimatedJPEGSize))
		},
	}
	cameraConfigElements = append(cameraConfigElements, resSelect)

	//. Set default resolutions
	resSelect.SetSelected(resSelect.Options[0])

	//. Create FPS slider and label
	fpsSlider := &widget.Slider{
		Min:   2,
		Max:   30,
		Value: 30,
	}

	fpsLabel := &widget.Label{
		Text: fmt.Sprintf("FPS (%v)", int(fpsSlider.Value)),
	}

	//. Update FPS label on slider move
	fpsSlider.OnChanged = func(f float64) {
		fpsLabel.SetText(fmt.Sprintf("FPS (%v)", int(f)))
		cameras[index].FPS = int(f)
	}

	//. Create quality slider
	qualitySlider := &widget.Slider{
		Min:   1,
		Max:   100,
		Value: 100,
	}

	qualityLabel := &widget.Label{
		Text: fmt.Sprintf("Quality (%v)", int(qualitySlider.Value)),
	}

	qualitySlider.OnChanged = func(f float64) {
		qualityLabel.SetText(fmt.Sprintf("Quality (%v)", int(f)))
		cameras[index].Quality = int(f)
	}

	//. Create port entry
	portLabel := &widget.Label{
		Text: "808" + strconv.Itoa(len(cameras)-1),
	}

	//. Toggle button tapped
	toggleButton.OnTapped = func() {
		toggleButton.Disable()
		go func() {
			time.Sleep(1 * time.Second)
			toggleButton.Enable()
		}()
		if toggleButton.Text == "Start" {
			startStreaming()
		} else {
			stopStreaming()
		}
		toggleButton.Refresh()
	}

	//. Set default camera setting
	cameras[len(cameras)-1].Res = resSelect.Options[0]
	cameras[len(cameras)-1].FPS = int(fpsSlider.Value)
	cameras[len(cameras)-1].Quality = int(qualitySlider.Value)
	cameras[len(cameras)-1].Port = portLabel.Text
	cameras[len(cameras)-1].Enabled = enabledCheck.Checked

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
			&widget.Label{Text: "Port"},
			portLabel,
		),
	)
}
