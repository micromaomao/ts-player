package main

import (
	"bytes"
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"image/draw"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

func doOpToVideo(opt options) {
	d := initPlayer(opt)
	ffmpeg_bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		panic(fmt.Errorf("failed to find ffmpeg."))
	}
	ffplay_bin, err := exec.LookPath("ffplay")
	if err != nil && opt.ffplay {
		panic(fmt.Errorf("failed to find ffplay."))
	}
	var videoCols, videoRows = opt.bufferSize.cols, opt.bufferSize.rows
	var fps = opt.fps
	var fontSizePoints float64 = 11
	var dpi float64 = opt.dpi
	var mediumFontFace = getFontFace(opt.fontFamily, "medium", fontSizePoints, dpi)
	var boldFontFace = getFontFace(opt.fontFamily, "bold", fontSizePoints, dpi)
	var cellWidth, cellHeight int
	var baseOff image.Point
	{
		const set string = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		bound, _ := font.BoundBytes(mediumFontFace, []byte(set))
		log("%v", bound)
		var width = bound.Max.X - bound.Min.X
		var height = bound.Max.Y - bound.Min.Y
		cellWidth = width.Ceil() / len(set)
		cellHeight = height.Ceil()
		baseOff.X = -bound.Min.X.Round()
		baseOff.Y = -bound.Max.Y.Round()
	}
	var videoWidth = videoCols * cellWidth
	var videoHeight = videoRows * cellHeight
	if videoWidth%16 != 0 {
		videoWidth = (videoWidth / 16) * 16
	}
	if videoHeight%16 != 0 {
		videoHeight = (videoHeight / 16) * 16
	}
	var videoSizeArg = fmt.Sprintf("%dx%d", videoWidth, videoHeight)
	var args = []string{"-f", "rawvideo", "-video_size", videoSizeArg,
		"-pixel_format", "rgb24", "-vcodec", "rawvideo", "-framerate", strconv.Itoa(fps),
		"-i", "pipe:0"}
	if !opt.ffplay {
		args = append(args, "-framerate", strconv.Itoa(fps), "-preset", "veryfast", "-crf", "20", opt.videoOutput)
	}
	bin := ffmpeg_bin
	if opt.ffplay {
		bin = ffplay_bin
	}
	if opt.colorProfileInput != "" {
		cf, err := processColorProfile(opt.colorProfileInput)
		if err != nil {
			panic(err)
		}
		d.translateColor = &cf
	} else {
		d.translateColor = nil
	}
	proc := exec.Command(bin, args...)
	var videoDataBuf = make([]byte, 0, 1000000)
	var videoDataBufLock = &sync.Mutex{}
	var eof = false
	proc.Stdin = &videoDataReader{buf: &videoDataBuf, lock: videoDataBufLock, eof: &eof}
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	err = proc.Start()
	if err != nil {
		panic(err)
	}
	defer func() {
		p := recover()
		if p != nil {
			if proc.Process != nil {
				proc.Process.Signal(syscall.SIGTERM)
				proc.Wait()
			}
			panic(p)
		}
	}()
	go func() {
		e := proc.Wait()
		if e != nil {
			os.Exit(1)
		} else {
			os.Exit(0)
		}
	}()
	var signalChannel = make(chan os.Signal)
	go func() {
		for {
			_ = <-signalChannel
			if proc.Process != nil {
				proc.Process.Signal(syscall.SIGTERM)
				proc.Wait()
			}
			os.Exit(1)
		}
	}()
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGINT)
	var bounds = image.Rect(0, 0, videoWidth, videoHeight)
	var canvas = image.NewRGBA(bounds)
	var videoFrame uint64 = 0
	var lastFrameId = d.index.GetCount() - 1
	var lastFrameIdDrawn uint64 = lastFrameId + 1
	var drawer = font.Drawer{
		Dst:  canvas,
		Src:  nil,
		Face: mediumFontFace,
		Dot:  fixed.Point26_6{},
	}
	for {
		var currentTimeOffset = float64(videoFrame) / float64(fps)
		currentFrameId, index := d.searchForFrame(currentTimeOffset)
		log("Frame %v => %v => %v/%v", videoFrame, currentTimeOffset, currentFrameId, lastFrameId)
		if currentFrameId == lastFrameIdDrawn {
			pushData(&videoDataBuf, videoDataBufLock, canvas)
			videoFrame++
			continue
		}
		if currentFrameId < opt.ss {
			videoFrame++
			continue
		}
		if opt.t != 0 && currentFrameId >= opt.ss+opt.t {
			videoDataBufLock.Lock()
			eof = true
			videoDataBufLock.Unlock()
			break
		}
		finfo, fcontent, err, _ := d.readFrameFromOffset(index.GetByteOffset())
		if err != nil {
			panic(err)
		}
		for row := 0; row < d.frameSize.rows; row++ {
			for col := 0; col < d.frameSize.cols; col++ {
				var frameCell = fcontent.getCellAt(row, col, &d.frameSize)
				var cellRect = image.Rect(col*cellWidth, row*cellHeight, (col+1)*cellWidth, (row+1)*cellHeight)
				var vtBg = frameCell.style.bg
				if !vtBg.IsRGB() {
					if d.translateColor == nil {
						panic(fmt.Sprintf("No color profile provided, but the recording does not encode color. Can't convert to video."))
					}
					panic("!")
				}
				bgR, bgG, bgB, _ := vtBg.GetRGB()
				var vtFg = frameCell.style.fg
				if !vtFg.IsRGB() {
					if d.translateColor == nil {
						panic(fmt.Sprintf("No color profile provided, but the recording does not encode color. Can't convert to video."))
					}
					panic("!")
				}
				fgR, fgG, fgB, _ := vtFg.GetRGB()
				var bgImg = image.NewUniform(color.RGBA{bgR, bgG, bgB, 255})
				var fgImg = image.NewUniform(color.RGBA{fgR, fgG, fgB, 255})
				draw.Draw(canvas, cellRect, bgImg, cellRect.Min, draw.Over)
				if frameCell.style.bold {
					drawer.Face = boldFontFace
				} else {
					drawer.Face = mediumFontFace
				}
				drawer.Src = fgImg
				drawer.Dot = fixed.P(cellRect.Min.X+baseOff.X, cellRect.Max.Y+baseOff.Y)
				drawer.DrawString(string(frameCell.chars))
			}
		}
		pushData(&videoDataBuf, videoDataBufLock, canvas)
		videoFrame++
		if currentFrameId == lastFrameId {
			log("EOF\n")
			for i := 0; i < int(finfo.duration*float64(fps)+1); i++ {
				pushData(&videoDataBuf, videoDataBufLock, canvas)
			}
			videoDataBufLock.Lock()
			eof = true
			videoDataBufLock.Unlock()
			break
		}
	}
	select {}
}

func pushData(buf *[]byte, lock *sync.Mutex, canvas *image.RGBA) {
	lock.Lock()
	defer lock.Unlock()
	var bd = canvas.Bounds()
	var sz uint64 = 0
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			var pix = canvas.RGBAAt(x, y)
			*buf = append(*buf, byte(pix.R), byte(pix.G), byte(pix.B))
			sz += 3
		}
	}
}

type videoDataReader struct {
	buf  *[]byte
	lock *sync.Mutex
	eof  *bool
	leak uint64
}

func (r *videoDataReader) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		panic("len(p) == 0")
	}
	r.lock.Lock()
	n = copy(p, *r.buf)
	if n == 0 {
		if *r.eof && len(*r.buf) == 0 {
			err = io.EOF
		}
		r.lock.Unlock()
		return
	}
	if len(p) >= len(*r.buf) {
		*r.buf = make([]byte, 0, cap(*r.buf))
		r.leak = 0
	} else {
		// newbuf := make([]byte, len(*r.buf)-len(p), cap(*r.buf))
		// start := time.Now()
		// n := copy(newbuf, (*r.buf)[len(p):])
		// log("Copying %v took %v", humanize.Bytes(uint64(n)), time.Now().Sub(start))
		// *r.buf = newbuf
		*r.buf = (*r.buf)[len(p):]
		r.leak += uint64(len(p))
		log("Leaking %v", humanize.Bytes(r.leak))
	}
	r.lock.Unlock()
	return
}

func findFont(family, weight string) string {
	var proc = exec.Command("fc-match", "-f", "%{file}\n", family+":fontformat=TrueType:spacing=mono:weight="+weight)
	var outBuffer = bytes.NewBuffer(make([]byte, 0, 10000))
	proc.Stdin = nil
	proc.Stdout = outBuffer
	proc.Stderr = os.Stderr
	proc.Start()
	if err := proc.Wait(); err != nil {
		panic(err)
	}
	outStr, err := outBuffer.ReadString('\n')
	if err != nil && err != io.EOF {
		panic(err)
	}
	if outStr == "" {
		panic("No fonts found.")
	}
	return strings.TrimSuffix(outStr, "\n")
}

func getFontFace(fontFam, weight string, fontSizePoints, dpi float64) font.Face {
	var fontFile = findFont(fontFam, weight)
	log("Using font %v for %v", fontFile, weight)
	fFont, err := os.OpenFile(fontFile, os.O_RDONLY, 0)
	if err != nil {
		panic(err)
	}
	defer fFont.Close()
	fontBuf, err := ioutil.ReadAll(fFont)
	if err != nil {
		panic(err)
	}
	fnt, err := truetype.Parse(fontBuf)
	if err != nil {
		panic(err)
	}
	return truetype.NewFace(fnt, &truetype.Options{
		Size:    fontSizePoints,
		DPI:     dpi,
		Hinting: font.HintingNone,
	})
}
