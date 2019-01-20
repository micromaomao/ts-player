package main

import (
	"fmt"
	"github.com/mattn/go-isatty"
	"github.com/micromaomao/go-libvterm"
	"github.com/pkg/term/termios"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type recorderState struct {
	exited        bool
	encoder       *encoderState
	master        *os.File
	slave         *os.File
	process       *os.Process
	signalChannel chan os.Signal
	finalWorkLock *sync.Mutex
	termInitAttr  *syscall.Termios
	startTime     time.Time

	lastFrameId     uint64
	lastTime        time.Time
	outputBuffer    []byte
	lastCt          frameContent
	frameBufferLock *sync.Mutex
}

func doOpRecord(opt options) {
	if !isatty.IsTerminal(0) || !isatty.IsTerminal(1) {
		panic(fmt.Errorf("Stdin and/or stdout are not terminals!"))
	}
	exPath, err := os.Executable()
	if err != nil {
		panic(err)
	}
	shell, ok := os.LookupEnv("SHELL")
	if !ok {
		panic(fmt.Errorf("Environmental variable $SHELL must be set."))
	}
	_, err = os.Stat(opt.output)
	if err == nil {
		panic(fmt.Errorf("%v already exists", opt.output))
	} else if !os.IsNotExist(err) {
		panic(fmt.Errorf("%v stating %v", err.Error(), opt.output))
	}
	fOut, err := os.OpenFile(opt.output, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(fmt.Errorf("%v opening %v for writing", err.Error(), opt.output))
	}
	err = fOut.Truncate(0)
	if err != nil {
		panic(err)
	}
	fOut.Seek(0, os.SEEK_SET)
	r := &recorderState{}
	e := &encoderState{}
	r.encoder = e
	vt := vterm.New(opt.stage.rows, opt.stage.cols)
	defer vt.Close()
	e.t = vt
	e.size.rows = opt.stage.rows
	e.size.cols = opt.stage.cols
	e.resetVT(opt)
	e.dict = nil
	e.cdict = nil
	e.initOutputFile(fOut)
	master, slave, err := termios.Pty()
	if err != nil {
		panic(fmt.Errorf("%v opening tty/pty", err.Error()))
	}
	defer master.Close()
	defer slave.Close()
	fmt.Fprintf(os.Stdout, "Recording started. Exit the shell to end.\n")
	r.master = master
	r.slave = slave
	initTermSize := termGetSize()
	termSetSize(slave.Fd(), initTermSize)
	if isatty.IsTerminal(2) {
		os.Stderr.Close()
	}
	initAttr := termSetRaw()
	r.termInitAttr = &initAttr
	err = termios.Tcsetattr(slave.Fd(), termios.TCSANOW, &initAttr)
	if err != nil {
		panic(err)
	}
	r.startTime = time.Now()
	r.lastTime = r.startTime
	r.lastFrameId = 0
	r.outputBuffer = make([]byte, 0, 1000000)
	procAttr := &os.ProcAttr{}
	procAttr.Files = make([]*os.File, 3)
	procAttr.Files[0] = slave
	procAttr.Files[1] = slave
	procAttr.Files[2] = slave
	proc, err := os.StartProcess(exPath, []string{os.Args[0], "__rec_exec", shell}, procAttr)
	if err != nil {
		panic(err)
	}
	r.process = proc
	r.finalWorkLock = &sync.Mutex{}
	r.frameBufferLock = &sync.Mutex{}
	r.signalChannel = make(chan os.Signal)
	signal.Notify(r.signalChannel, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	go r.signalHandlerThread()
	go r.stdinReader()
	go r.masterReader()
	go r.frameWriterThread()
	_, err = r.process.Wait()
	r.doFinalWorkAndExit()
}

func (r *recorderState) signalHandlerThread() {
	for {
		signal := <-r.signalChannel
		switch signal {
		case syscall.SIGWINCH:
			nTermSize := termGetSize()
			termSetSize(r.slave.Fd(), nTermSize)
		case syscall.SIGTERM:
			fallthrough
		case syscall.SIGINT:
			fallthrough
		case syscall.SIGQUIT:
			r.process.Signal(signal)
			r.doFinalWorkAndExit()
		}
	}
}

func (r *recorderState) doFinalWorkAndExit() {
	r.finalWorkLock.Lock()
	if r.exited {
		// should not happen
		r.finalWorkLock.Unlock()
		panic("!")
	}
	r.exited = true
	termRestore(*r.termInitAttr)
	r.frameBufferLock.Lock()
	r.encoder.finalize()
	os.Exit(0)
}

func (r *recorderState) stdinReader() {
	for {
		buf := make([]byte, 1000000)
		n, _ := os.Stdin.Read(buf)
		if n == 0 {
			continue
		}
		buf = buf[0:n]
		written := 0
		for written < n {
			nowWritten, err := r.master.Write(buf[written:])
			fmt.Fprintf(os.Stderr, "forwarded %v\n", strconv.Quote(string(buf[written:written+nowWritten])))
			written += nowWritten
			if nowWritten == 0 && err != nil {
				break
			}
		}
	}
}

func (r *recorderState) masterReader() {
	buf := make([]byte, 1000000)
	for {
		n, _ := r.master.Read(buf)
		if n == 0 {
			continue
		}
		readBuf := buf[0:n]
		os.Stdout.Write(readBuf)
		perf := time.Now()
		r.frameBufferLock.Lock()
		r.outputBuffer = append(r.outputBuffer, readBuf...)
		r.frameBufferLock.Unlock()
		timeUsed := time.Now().Sub(perf)
		fmt.Fprintf(os.Stderr, "received %v within %v\n", strconv.Quote(string(readBuf)), timeUsed)
	}
}

func (r *recorderState) frameWriterThread() {
	for {
		r.frameBufferLock.Lock()
		if len(r.outputBuffer) == 0 {
			r.frameBufferLock.Unlock()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		now := time.Now()

		if r.lastCt != nil {
			finfo := frame{}
			finfo.time = float64(r.lastTime.Sub(r.startTime)) / float64(time.Second)
			finfo.duration = float64(now.Sub(r.lastTime)) / float64(time.Second)
			finfo.index = r.lastFrameId
			ct := r.lastCt
			r.lastFrameId++
			r.lastCt = nil
			r.frameBufferLock.Unlock()
			r.finalWorkLock.Lock()
			r.encoder.writeFrame(&finfo, ct)
			r.finalWorkLock.Unlock()
			r.frameBufferLock.Lock()
		}

		r.lastTime = now
		nData := r.outputBuffer
		r.outputBuffer = make([]byte, 0, 1000000)
		r.frameBufferLock.Unlock()
		r.finalWorkLock.Lock()
		termSize := termGetSize()
		r.lastCt = r.encoder.inputToFrameContentSize(nData, termSize)
		r.finalWorkLock.Unlock()
	}
}
