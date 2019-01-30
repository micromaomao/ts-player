package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/mattn/go-isatty"
	"github.com/micromaomao/go-libvterm"
	"github.com/valyala/gozstd"
	"image/color"
	"io"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type decoderState struct {
	frameSize      sizeStruct
	index          *ITSIndex
	lastFrameId    uint64
	compressed     bool
	ddict          *gozstd.DDict
	file           *os.File
	translateColor *colorProfile

	renderingFrameId     uint64
	updateSignal         *sync.Cond
	renderCache          map[uint64]frameToRender
	renderCacheLock      sync.Locker
	paused               bool
	nextTimeForceRedraw  bool
	showControlBarBefore *time.Time

	exiting bool

	updateTimer *time.Timer
}

type frameToRender struct {
	frameId      uint64
	frameContent frameContent
	duration     float64
}

func doOpPlay(opt options) {
	if !opt.evenIfNotTty && (!isatty.IsTerminal(0) || !isatty.IsTerminal(1)) {
		fmt.Fprintf(os.Stderr, "Stdin and/or stdout are not terminals!\n")
		os.Exit(1)
	}
	d := initPlayer(opt)
	if isatty.IsTerminal(2) {
		os.Stderr.Close()
	}
	initTtyAttr := termSetRaw()
	fmt.Fprintf(os.Stdout, "\033[1049h")
	go d.uiThread()
	signalChannel := make(chan os.Signal)
	go func() {
		for {
			sig := <-signalChannel
			if sig == syscall.SIGWINCH {
				d.updateSignal.L.Lock()
				d.updateSignal.Broadcast()
				d.updateSignal.L.Unlock()
			} else if sig == syscall.SIGTERM || sig == syscall.SIGINT {
				d.updateSignal.L.Lock()
				d.exiting = true
				d.updateSignal.Broadcast()
				d.updateSignal.L.Unlock()
			}
		}
	}()
	signal.Notify(signalChannel, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGINT)
	go d.inputReaderThread()
	go d.loadFramesThread()
	for {
		d.updateSignal.L.Lock()
		if d.exiting {
			d.updateSignal.L.Unlock()
			break
		}
		d.updateSignal.Broadcast()
		d.updateSignal.L.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintf(os.Stdout, "\033[1049l")
	termRestore(initTtyAttr)
}

func initPlayer(opt options) *decoderState {
	fIts, err := os.OpenFile(opt.itsInput, os.O_RDONLY, 0)
	if err != nil {
		panic(err)
	}
	magicBuffer := make([]byte, len(FileMagic))
	n, err := fIts.Read(magicBuffer)
	if err != nil {
		panic(err)
	}
	if n != len(FileMagic) || bytes.Compare(magicBuffer, []byte(FileMagic)) != 0 {
		panic("Not a its file: magic wrong.")
	}
	var headerLen uint32
	binary.Read(fIts, binary.BigEndian, &headerLen)
	if headerLen > 10000 || headerLen < 1 {
		panic("Invalid headerLen")
	}
	headerBuff := make([]byte, headerLen)
	n, err = fIts.Read(headerBuff)
	if uint32(n) < headerLen || err == io.EOF {
		panic("Permature EOF")
	}
	header := &ITSHeader{}
	err = proto.Unmarshal(headerBuff, header)
	if err != nil {
		panic(err)
	}
	if header.GetVersion() != 1 {
		panic("Invalid file version. Please update this player.")
	}

	d := &decoderState{}
	d.frameSize.rows = int(header.GetRows())
	d.frameSize.cols = int(header.GetCols())
	if d.frameSize.rows*d.frameSize.cols <= 0 {
		panic("Invalid dimension")
	}
	indexOffset := header.GetIndexOffset()
	if indexOffset <= 12 {
		panic("Invalid indexOffset")
	}
	_, err = fIts.Seek(int64(indexOffset), os.SEEK_SET)
	if err != nil {
		panic(err)
	}
	var indexLen uint64
	err = binary.Read(fIts, binary.BigEndian, &indexLen)
	if err != nil {
		panic(err)
	}
	fileStat, err := fIts.Stat()
	if err != nil {
		panic(err)
	}
	fileLen := uint64(fileStat.Size())
	if indexOffset+indexLen > fileLen+10000 {
		panic("Invalid indexLen")
	}
	indexBuf := make([]byte, indexLen)
	n, err = fIts.Read(indexBuf)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if uint64(n) < indexLen {
		panic("Permature EOF")
	}
	switch header.GetCompressionMode() {
	case ITSHeader_COMPRESSION_ZSTD:
		d.compressed = true
		compressedDictBuf := header.GetCompressionDict()
		if len(compressedDictBuf) > 0 {
			dictBuf, err := gozstd.Decompress(nil, compressedDictBuf)
			if err != nil {
				panic(err)
			}
			d.ddict, err = gozstd.NewDDict(dictBuf)
			if err != nil {
				panic(err)
			}
		} else {
			d.ddict = nil
		}

		indexBuf, err = gozstd.Decompress(nil, indexBuf)
		if err != nil {
			panic(err)
		}
	case ITSHeader_COMPRESSION_NONE:
		d.compressed = false
	default:
		panic("Unknown compression mode")
	}
	d.index = &ITSIndex{}
	err = proto.Unmarshal(indexBuf, d.index)
	if err != nil {
		panic(err)
	}
	if d.index.GetCount() <= 0 || len(d.index.GetFrames()) <= 0 {
		panic("Empty index")
	}
	if d.index.GetCount() != uint64(len(d.index.GetFrames())) {
		panic("Wrong index count")
	}
	d.lastFrameId = d.index.GetCount() - 1
	d.file = fIts
	d.renderingFrameId = 0
	d.renderCache = make(map[uint64]frameToRender)
	d.renderCacheLock = &sync.Mutex{}
	d.updateSignal = sync.NewCond(&sync.Mutex{})

	d.renderingFrameId = 0
	d.paused = false

	if opt.colorProfileInput != "" {
		cf, err := processColorProfile(opt.colorProfileInput)
		if err != nil {
			panic(err)
		}
		d.translateColor = &cf
	} else {
		d.translateColor = nil
	}
	return d
}

func (d *decoderState) searchForFrame(time float64) (frameId uint64, indexEntry *ITSIndex_FrameIndex) {
	frames := d.index.GetFrames()
	if frames[0].GetTimeOffset()+0.0001 >= time {
		return 0, frames[0]
	}
	if frames[len(frames)-1].GetTimeOffset()-0.0001 < time {
		return uint64(len(frames) - 1), frames[len(frames)-1]
	}
	var i, j int
	j = len(frames)
	for {
		if i >= j || i >= len(frames) {
			if j-1 < 0 {
				j = 1
			}
			if j > len(frames) {
				j = len(frames)
			}
			return uint64(j - 1), frames[j-1]
		}
		mid := (i + j) / 2
		foundTime := frames[mid].GetTimeOffset()
		if math.Abs(foundTime-time) < 0.0001 {
			return uint64(mid), frames[mid]
		}
		if foundTime < time {
			i = mid + 1
		} else {
			j = mid - 1
		}
	}
}

func (d *decoderState) frameIdLookup(frameId uint64) (byteOffset uint64, timeOffset float64) {
	indexEntry := d.index.GetFrames()[frameId]
	return indexEntry.GetByteOffset(), indexEntry.GetTimeOffset()
}

func (d *decoderState) readFrameFromOffset(byteOffset uint64) (frameInfo frame, content frameContent, err error, nextOffset uint64) {
	var frameStruct *ITSFrame
	frameStruct, nextOffset, err = d.readFrameStructFromOffset(byteOffset)
	if err != nil {
		return
	}
	defer func() {
		p := recover()
		if p != nil {
			errP, ok := p.(error)
			if ok {
				err = errP
				return
			} else {
				panic(p)
			}
		}
	}()
	frameInfo, content, err = d.decodeFrameStruct(frameStruct)
	return
}

func (d *decoderState) readFrameBytesFromOffset(byteOffset uint64) (buf []byte, nextOffset uint64, err error) {
	_, err = d.file.Seek(int64(byteOffset), os.SEEK_SET)
	if err != nil {
		return
	}
	var frameByteLen uint32
	err = binary.Read(d.file, binary.BigEndian, &frameByteLen)
	if err != nil {
		return
	}
	if frameByteLen > 1024*1024*50 {
		// gaurd against DNS, arbitrary value.
		err = fmt.Errorf("invalid frame byteLength near %x", byteOffset)
		return
	}
	buf = make([]byte, frameByteLen)
	d.file.Read(buf)
	nextOffset = byteOffset + 4 + uint64(frameByteLen)
	if d.compressed {
		if d.ddict != nil {
			buf, err = gozstd.DecompressDict(nil, buf, d.ddict)
			if err != nil {
				return
			}
		} else {
			buf, err = gozstd.Decompress(nil, buf)
			if err != nil {
				return
			}
		}
	}
	return
}

func (d *decoderState) readFrameStructFromOffset(byteOffset uint64) (frameStruct *ITSFrame, nextOffset uint64, err error) {
	var buf []byte
	buf, nextOffset, err = d.readFrameBytesFromOffset(byteOffset)
	if err != nil {
		return
	}
	frameStruct = &ITSFrame{}
	err = proto.Unmarshal(buf, frameStruct)
	if err != nil {
		return
	}
	return
}

func (d *decoderState) decodeFrameStruct(frameStruct *ITSFrame) (frameInfo frame, content frameContent, err error) {
	if frameStruct.GetType() != ITSFrame_FRAMETYPE_K {
		err = errors.New("Unrecognized frame type")
		return
	}
	frameInfo = frame{}
	frameInfo.index = frameStruct.GetFrameId()
	frameInfo.time = frameStruct.GetTimeOffset()
	frameInfo.duration = frameStruct.GetDuration()
	content = make(frameContent, d.frameSize.rows*d.frameSize.cols)
	body := frameStruct.GetBodyK()
	i := 0
	for row := 0; row < d.frameSize.rows; row++ {
		for col := 0; col < d.frameSize.cols; col++ {
			cell := frameCell{}
			cell.chars = []rune(body.GetContents()[i])
			cell.fromAttrCode(body.GetAttrs()[i])
			content.setCellAt(row, col, cell, &d.frameSize)
			i++
		}
	}
	return
}

func (c *frameCell) equalsTo(c2 *frameCell) bool {
	if string(c.chars) != string(c2.chars) {
		return false
	}
	if c.attrCode(nil) != c2.attrCode(nil) {
		return false
	}
	return true
}

func (d *decoderState) renderFrameContent(perv, next frameContent, out io.Writer, dx, dy, dw, dh int, frameSize sizeStruct) {
	var cursorRow, cursorCol int
	fmt.Fprintf(out, "\033[%v;%vH", dy+1, dx+1)
	var lastAttr uint64
	for row := 0; row < frameSize.rows && row < dh; row++ {
		if row+dy < 0 {
			continue
		}
		for col := 0; col < frameSize.cols && col < dw; col++ {
			if col+dx < 0 {
				continue
			}
			if perv != nil && perv.getCellAt(row, col, &frameSize).equalsTo(next.getCellAt(row, col, &frameSize)) {
				continue
			}
			if cursorRow != row || cursorCol != col {
				fmt.Fprintf(out, "\033[%d;%dH", row+dy+1, col+dx+1)
				cursorRow = row
				cursorCol = col
			}
			cell := next.getCellAt(row, col, &frameSize)
			if row != 0 && col != 0 && cell.attrCode(nil) == lastAttr {
				// no need to output attr
				out.Write([]byte(string(cell.chars)))
			} else {
				out.Write([]byte(cell.toOutput(d.translateColor)))
			}
			cursorCol++
		}
	}
}

func (d *decoderState) uiThread() {
	var lastFrameRendered *frameToRender = nil
	var lastFrameStaysBefore time.Time = time.Now()
	var perviousSize sizeStruct
	renderTo := os.Stdout
	firstRender := true
	controlBarShowedLastFrame := false
	var lastControlBarFC frameContent = nil
	for {
		needsForcedRedraw := false
		if !firstRender {
			d.updateSignal.L.Lock()
			d.updateSignal.Wait()
			if d.exiting {
				d.updateSignal.L.Unlock()
				return
			}
			d.updateSignal.L.Unlock()
		} else {
			// first render
			needsForcedRedraw = true
			firstRender = false
		}
		termSz := termGetSize()
		if perviousSize != termSz {
			perviousSize = termSz
			needsForcedRedraw = true
		}
		d.updateSignal.L.Lock()
		if d.nextTimeForceRedraw {
			d.nextTimeForceRedraw = false
			needsForcedRedraw = true
		}
		if lastFrameRendered != nil {
			if d.renderingFrameId == lastFrameRendered.frameId && (time.Now().After(lastFrameStaysBefore) && !d.paused && d.lastFrameId > d.renderingFrameId) {
				d.renderingFrameId++
				log("advancing frame pointer to %v", d.renderingFrameId)
				d.updateSignal.Broadcast()
			}
		}
		currentRenderingFrameId := d.renderingFrameId
		showingControlBar := false
		if d.paused {
			showingControlBar = true
		} else if d.showControlBarBefore != nil && d.showControlBarBefore.After(time.Now()) {
			showingControlBar = true
		}
		if controlBarShowedLastFrame && !showingControlBar {
			needsForcedRedraw = true
		}
		d.updateSignal.L.Unlock()
		if lastFrameRendered == nil || currentRenderingFrameId != lastFrameRendered.frameId {
			d.renderCacheLock.Lock()
			frameToDraw, gotFrame := d.renderCache[currentRenderingFrameId]
			d.renderCacheLock.Unlock()
			if gotFrame {
				if needsForcedRedraw || lastFrameRendered == nil || lastFrameRendered.frameId != currentRenderingFrameId {
					var pervFrameContent frameContent = nil
					if !needsForcedRedraw && lastFrameRendered != nil {
						pervFrameContent = lastFrameRendered.frameContent
					}
					if lastFrameRendered != nil && lastFrameStaysBefore.Before(time.Now().Add(-100*time.Millisecond)) {
						log("lagging on frame %v (%v)!", lastFrameRendered.frameId, time.Now().Sub(lastFrameStaysBefore))
					}
					fmt.Fprintf(renderTo, "\033[1;1H")
					if pervFrameContent == nil {
						fmt.Fprintf(renderTo, "\033[J")
					}
					d.renderFrameContent(pervFrameContent, frameToDraw.frameContent, renderTo, 0, 0, termSz.cols, termSz.rows, d.frameSize)
					lastControlBarFC = nil
					lastFrameRendered = &frameToDraw
					nextFrameWithin := time.Duration(frameToDraw.duration*1000) * time.Millisecond
					lastFrameStaysBefore = time.Now().Add(nextFrameWithin)
					d.updateSignal.L.Lock()
					d.updateWithin(nextFrameWithin)
					d.updateSignal.L.Unlock()
				}
			} else {
				if lastFrameRendered == nil && needsForcedRedraw {
					fmt.Fprintf(renderTo, "\033[1;1H\033[J\033[%v;%vH\033[0;4;1mRendering...\033[0m\033[%v;%vH", termSz.rows/4, termSz.cols/2-6, termSz.rows/4+2, termSz.cols/2)
					lastControlBarFC = nil
					d.updateSignal.L.Lock()
					d.updateWithin(100 * time.Millisecond)
					d.updateSignal.L.Unlock()
				} else if lastFrameRendered != nil && needsForcedRedraw {
					fmt.Fprintf(renderTo, "\033[1;1H\033[J")
					d.renderFrameContent(nil, lastFrameRendered.frameContent, renderTo, 0, 0, termSz.cols, termSz.rows, d.frameSize)
					lastControlBarFC = nil
				}
			}
		} else if needsForcedRedraw {
			fmt.Fprintf(renderTo, "\033[1;1H\033[J")
			d.renderFrameContent(nil, lastFrameRendered.frameContent, renderTo, 0, 0, termSz.cols, termSz.rows, d.frameSize)
			lastControlBarFC = nil
		}
		if showingControlBar {
			x := 10
			w := termSz.cols - x - 10
			if w <= 3 {
				w = termSz.cols
				x = 0
			}
			y := termSz.rows - 5
			h := 2
			if y < 0 {
				y = 0
			}
			sz := sizeStruct{rows: h, cols: w}
			leftText := ""
			d.updateSignal.L.Lock()
			_, currentTimeOffset := d.frameIdLookup(d.renderingFrameId)
			_, totalTime := d.frameIdLookup(d.lastFrameId)
			progress := currentTimeOffset / totalTime
			xThreshold := int(progress * float64(sz.cols))
			if d.paused {
				leftText = fmt.Sprintf(" paused at frame %v (%vs/%vs)", currentRenderingFrameId, uint64(currentTimeOffset), uint64(totalTime))
			} else {
				leftText = fmt.Sprintf(" playing at frame %v (%vs/%vs)", currentRenderingFrameId, uint64(currentTimeOffset), uint64(totalTime))
			}
			d.updateSignal.L.Unlock()
			controlBarFc := make(frameContent, w*h)
			for i := 0; i < len(controlBarFc); i++ {
				controlBarFc[i].style.fg = vterm.NewVTermColorRGB(color.RGBA{88, 30, 137, 255})
				controlBarFc[i].style.bg = vterm.NewVTermColorRGB(color.RGBA{255, 255, 255, 255})
				controlBarFc[i].chars = []rune{' '}
				if i >= sz.cols {
					// second row
					controlBarFc[i].style.underline = true
					x := i - sz.cols
					if x < len(leftText) {
						controlBarFc[i].chars = []rune{rune(leftText[x])}
					}
				} else {
					// first row, current x = i
					if i < xThreshold {
						controlBarFc[i].chars = []rune("=")
					} else if i == xThreshold {
						controlBarFc[i].chars = []rune(">")
					}
				}
			}
			d.renderFrameContent(lastControlBarFC, controlBarFc, renderTo, x, y, w, h, sz)
			lastControlBarFC = controlBarFc
		} else {
			lastControlBarFC = nil
		}
		controlBarShowedLastFrame = showingControlBar
	}
}

func (d *decoderState) inputReaderThread() {
	inBuf := bufio.NewReader(os.Stdin)
	for {
		if d.exiting {
			return
		}
		leader, err := inBuf.ReadByte()
		if d.exiting {
			return
		}
		if err != nil {
			panic(err)
		}
		if leader == '\x03' || leader == 'q' {
			d.updateSignal.L.Lock()
			d.exiting = true
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
			return
		} else if leader == 12 {
			d.updateSignal.L.Lock()
			d.nextTimeForceRedraw = true
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		} else if leader == ' ' || leader == 'k' {
			d.updateSignal.L.Lock()
			if d.paused {
				before := time.Now().Add(time.Second)
				d.showControlBarBefore = &before
			}
			d.paused = !d.paused
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		} else if leader == ',' || leader == '.' {
			d.updateSignal.L.Lock()
			d.paused = true
			if leader == ',' && d.renderingFrameId > 0 {
				// perv frame
				d.renderingFrameId--
			} else if leader == '.' && d.renderingFrameId < d.lastFrameId {
				d.renderingFrameId++
			}
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		} else if leader == 'j' || leader == 'l' {
			d.updateSignal.L.Lock()
			_, currentTimeOffset := d.frameIdLookup(d.renderingFrameId)
			if leader == 'j' {
				currentTimeOffset -= 5
			} else if leader == 'l' {
				currentTimeOffset += 5
			}
			nextFrameId, _ := d.searchForFrame(currentTimeOffset)
			if d.renderingFrameId != nextFrameId {
				d.renderingFrameId = nextFrameId
			}
			before := time.Now().Add(time.Second)
			d.showControlBarBefore = &before
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		} else if leader == '$' {
			d.updateSignal.L.Lock()
			d.renderingFrameId = d.lastFrameId
			d.paused = true
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		} else if leader == '^' || leader == '0' {
			d.updateSignal.L.Lock()
			d.renderingFrameId = 0
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		}
	}
}

func (d *decoderState) loadFramesThread() {
	var frameCacheSize uint64 = 10
	for {
		d.updateSignal.L.Lock()
		d.updateSignal.Wait()
		if d.exiting {
			d.updateSignal.L.Unlock()
			return
		}
		updated := false
		currentFrameId := d.renderingFrameId
		d.renderCacheLock.Lock()
		for foundFrameId, _ := range d.renderCache {
			if foundFrameId < currentFrameId || foundFrameId >= currentFrameId+frameCacheSize {
				delete(d.renderCache, foundFrameId)
				log("gcing cached frame %v", foundFrameId)
				updated = true
			}
		}
		framesToLoad := make([]uint64, 0, frameCacheSize)
		for i := currentFrameId; i < currentFrameId+frameCacheSize && i <= d.lastFrameId; i++ {
			_, exists := d.renderCache[i]
			if !exists {
				framesToLoad = append(framesToLoad, i)
			}
		}
		d.renderCacheLock.Unlock()
		if updated {
			d.updateSignal.Broadcast()
			updated = false
		}
		d.updateSignal.L.Unlock()

		for _, frameToLoad := range framesToLoad {
			log("loading frame %v", frameToLoad)
			byteOffset, _ := d.frameIdLookup(frameToLoad)
			finfo, content, err, _ := d.readFrameFromOffset(byteOffset)
			if err != nil {
				panic(err) // TODO
			}
			d.renderCacheLock.Lock()
			d.renderCache[finfo.index] = frameToRender{frameId: finfo.index, frameContent: content, duration: finfo.duration}
			d.renderCacheLock.Unlock()
			d.updateSignal.L.Lock()
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		}
	}
}

func (d *decoderState) updateWithin(sometime time.Duration) {
	if d.updateTimer == nil {
		d.updateTimer = time.AfterFunc(sometime, func() {
			d.updateSignal.L.Lock()
			d.updateTimer = nil
			d.updateSignal.Broadcast()
			d.updateSignal.L.Unlock()
		})
	} else {
		d.updateTimer.Stop()
		d.updateTimer = nil
		d.updateWithin(sometime)
	}
}
