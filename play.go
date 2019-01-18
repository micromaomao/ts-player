package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/mattn/go-isatty"
	"github.com/pkg/term/termios"
	"github.com/valyala/gozstd"
	"io"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type decoderState struct {
	size        sizeStruct
	index       *ITSIndex
	lastFrameId uint64
	compressed  bool
	ddict       *gozstd.DDict
	file        *os.File

	renderingFrameId    uint64
	updateSignal        *sync.Cond
	renderCache         map[uint64]frameToRender
	renderCacheLock     sync.Locker
	paused              bool
	nextTimeForceRedraw bool

	exiting bool

	updateTimer *time.Timer
}

type frameToRender struct {
	frameId      uint64
	frameContent frameContent
	duration     float64
}

func log(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Stderr.WriteString("\n")
}

func doOpPlay(opt options) {
	if !isatty.IsTerminal(0) || !isatty.IsTerminal(1) {
		fmt.Fprintf(os.Stderr, "Stdin and/or stdout are not terminals!")
		os.Exit(1)
	}
	d := initPlayer(opt)
	ttyAttr := syscall.Termios{}
	termios.Tcgetattr(0, &ttyAttr)

	copy := ttyAttr
	copy.Iflag &= ^uint32(syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON)
	copy.Oflag &= ^uint32(syscall.OPOST)
	copy.Lflag &= ^uint32(syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN)
	copy.Cflag |= uint32(syscall.CS8)
	termios.Tcsetattr(0, termios.TCSADRAIN, &copy)
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
		d.updateSignal.Wait()
		if d.exiting {
			d.updateSignal.L.Unlock()
			break
		}
		d.updateSignal.L.Unlock()
	}
	fmt.Fprintf(os.Stdout, "\033[1049l")
	termios.Tcsetattr(0, termios.TCSADRAIN, &ttyAttr)
}

func initPlayer(opt options) *decoderState {
	fIts, err := os.OpenFile(opt.itsFile, os.O_RDONLY, 0)
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
	d.size.rows = int(header.GetRows())
	d.size.cols = int(header.GetCols())
	if d.size.rows*d.size.cols <= 0 {
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
		dictBuf, err := gozstd.Decompress(nil, header.GetCompressionDict())
		if err != nil {
			panic(err)
		}
		d.ddict, err = gozstd.NewDDict(dictBuf)
		if err != nil {
			panic(err)
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

func (d *decoderState) readFrameFromOffset(byteOffset uint64) (frameInfo *frame, content frameContent, err error, nextOffset uint64) {
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
	buf := make([]byte, frameByteLen)
	d.file.Read(buf)
	nextOffset = byteOffset + 4 + uint64(frameByteLen)
	if d.compressed {
		buf, err = gozstd.DecompressDict(nil, buf, d.ddict)
		if err != nil {
			return
		}
	}
	frameStruct := &ITSFrame{}
	err = proto.Unmarshal(buf, frameStruct)
	if err != nil {
		return
	}
	defer func() {
		p := recover()
		if p != nil {
			err = errors.New("panic!")
		}
	}()
	frameInfo, content = d.decodeFrameStruct(frameStruct)
	return
}

func (d *decoderState) decodeFrameStruct(frameStruct *ITSFrame) (frameInfo *frame, content frameContent) {
	if frameStruct.GetType() != ITSFrame_FRAMETYPE_K {
		panic("Unrecognized frame type")
	}
	finfo := &frame{}
	finfo.index = frameStruct.GetFrameId()
	finfo.time = frameStruct.GetTimeOffset()
	finfo.duration = frameStruct.GetDuration()
	fContent := make(frameContent, d.size.rows*d.size.cols)
	body := frameStruct.GetBodyK()
	i := 0
	for row := 0; row < d.size.rows; row++ {
		for col := 0; col < d.size.cols; col++ {
			cell := frameCell{}
			cell.chars = []rune(body.GetContents()[i])
			cell.fromAttrCode(body.GetAttrs()[i])
			fContent.setCellAt(row, col, cell, &d.size)
			i++
		}
	}
	return finfo, fContent
}

func (c *frameCell) equalsTo(c2 *frameCell) bool {
	if string(c.chars) != string(c2.chars) {
		return false
	}
	if c.attrCode() != c2.attrCode() {
		return false
	}
	return true
}

func (d *decoderState) renderFrameTo(perv, next frameContent, out io.Writer, dx, dy, dw, dh int) {
	log("rendering to %v %v %v %v", dx, dy, dw, dh)
	var cursorRow, cursorCol int
	fmt.Fprintf(out, "\033[%v;%vH", dy+1, dx+1)
	var lastAttr uint64
	for row := 0; row < d.size.rows && row < dh; row++ {
		if row+dy < 0 {
			continue
		}
		for col := 0; col < d.size.cols && col < dw; col++ {
			if col+dx < 0 {
				continue
			}
			if perv != nil && perv.getCellAt(row, col, &d.size).equalsTo(next.getCellAt(row, col, &d.size)) {
				continue
			}
			if cursorRow != row || cursorCol != col {
				fmt.Fprintf(out, "\033[%d;%dH", row+dy+1, col+dx+1)
				cursorRow = row
				cursorCol = col
			}
			cell := next.getCellAt(row, col, &d.size)
			if row != 0 && col != 0 && cell.attrCode() == lastAttr {
				// no need to output attr
				out.Write([]byte(string(cell.chars)))
			} else {
				out.Write([]byte(cell.toOutput()))
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
					if lastFrameRendered != nil && lastFrameStaysBefore.Before(time.Now().Add(-10*time.Millisecond)) {
						log("lagging on frame %v (%v)!", lastFrameRendered.frameId, time.Now().Sub(lastFrameStaysBefore))
					}
					fmt.Fprintf(renderTo, "\033[1;1H")
					if pervFrameContent == nil {
						fmt.Fprintf(renderTo, "\033[J")
					}
					d.renderFrameTo(pervFrameContent, frameToDraw.frameContent, renderTo, 0, 0, termSz.cols, termSz.rows)
					lastFrameRendered = &frameToDraw
					nextFrameWithin := time.Duration(frameToDraw.duration*1000) * time.Millisecond
					lastFrameStaysBefore = time.Now().Add(nextFrameWithin)
					d.updateSignal.L.Lock()
					d.updateWithin(nextFrameWithin)
					d.updateSignal.L.Unlock()
				}
			} else {
				log("lag!")
				if lastFrameRendered == nil && needsForcedRedraw {
					fmt.Fprintf(renderTo, "\033[1;1H\033[J\033[%v;%vH\033[0;4;1mRendering...\033[0m\033[%v;%vH", termSz.rows/4, termSz.cols/2-6, termSz.rows/4+2, termSz.cols/2)
					d.updateSignal.L.Lock()
					d.updateWithin(100 * time.Millisecond)
					d.updateSignal.L.Unlock()
				} else if lastFrameRendered != nil && needsForcedRedraw {
					fmt.Fprintf(renderTo, "\033[1;1H\033[J")
					d.renderFrameTo(nil, lastFrameRendered.frameContent, renderTo, 0, 0, termSz.cols, termSz.rows)
				}
			}
		} else if needsForcedRedraw {
			fmt.Fprintf(renderTo, "\033[1;1H\033[J")
			d.renderFrameTo(nil, lastFrameRendered.frameContent, renderTo, 0, 0, termSz.cols, termSz.rows)
		}
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
		if leader == '\x03' {
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
			log("d = %v", finfo.duration)
		}

		if len(framesToLoad) > 0 {
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
