package main

import (
	"fmt"
	"github.com/mattn/go-isatty"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

var outputDebugLog = false

type options struct {
	operation         string
	fps               int
	colorProfileInput string
	itsOutput         string
	bufferSize        sizeStruct
	evenIfNotTty      bool

	shell string
	quiet bool

	script string
	timing string

	itsInput string

	fontFamily  string
	ffplay      bool
	videoOutput string
	dpi         float64
	ss          uint64
	t           uint64
}

const (
	opEncode            = "encode"
	opPlay              = "play"
	opRecord            = "record"
	opOptimize          = "optimize"
	opGetColorProfile   = "get-color-profile"
	opCheckColorProfile = "check-color-profile"
	opToVideo           = "to-video"
)

func log(format string, args ...interface{}) {
	if !outputDebugLog {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
	os.Stderr.WriteString("\n")
}

func main() {
	opt, err := parseArgs(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v. For usage, see man ts-player.\n", err.Error())
		os.Exit(1)
	}
	// defer (func() {
	// 	p := recover()
	// 	if p != nil {
	// 		pErr, ok := p.(error)
	// 		if !ok {
	// 			panic(p)
	// 		}
	// 		fmt.Fprintf(os.Stderr, "%v\n", pErr.Error())
	// 		os.Exit(1)
	// 	}
	// })()
	switch opt.operation {
	case opEncode:
		doOpEncode(opt)
	case opPlay:
		doOpPlay(opt)
	case opRecord:
		doOpRecord(opt)
	case opOptimize:
		doOpOptimize(opt)
	case opGetColorProfile:
		doOpGetColorProfile(opt)
	case opCheckColorProfile:
		doOpCheckColorProfile(opt)
	case opToVideo:
		doOpToVideo(opt)
	default:
		// default case handled by parseArgs
		panic("!")
	}
}

type Palette [18]uint32

var (
	regXxY = regexp.MustCompile(`^(\d+)x(\d+)$`)
)

func parseArgs(args []string) (opt options, err error) {
	err = nil
	if len(args) == 3 && args[1] == "__rec_exec" {
		if !isatty.IsTerminal(0) || !isatty.IsTerminal(1) || !isatty.IsTerminal(2) {
			os.Exit(1)
		}
		syscall.Setsid()
		tiocsctty(0)
		syscall.Exec(args[2], []string{args[2]}, os.Environ())
		return
	}
	opt.fps = 60
	opt.bufferSize = sizeStruct{300, 300}
	nbNonOptionArgs := 0
	if len(args) <= 1 {
		err = fmt.Errorf("Not enough arguments")
		return
	}
	for i := 1; i < len(args); i++ {
		currentArg := args[i]
		hasNextArg := false
		nextArg := ""
		if currentArg == "" {
			continue
		}
		if i+1 < len(args) {
			hasNextArg = true
			nextArg = args[i+1]
		}
		if i == 1 {
			if currentArg[0] == '-' {
				err = fmt.Errorf("The first argument must be an operation")
				return
			}
			opt.operation = currentArg
			if opt.operation == opToVideo {
				opt.fps = 25
				opt.bufferSize = sizeStruct{160, 60}
				opt.dpi = 150
			}
			continue
		}

		if currentArg == "--debug" {
			outputDebugLog = true
			continue
		}

		if currentArg == "-f" && (opt.operation == opRecord || opt.operation == opEncode || opt.operation == opToVideo) {
			if !hasNextArg {
				err = fmt.Errorf("-f <fps>")
				return
			}
			opt.fps, err = strconv.Atoi(nextArg)
			if err != nil {
				return
			}
			i++
			continue
		}

		if currentArg == "-c" && (opt.operation == opRecord || opt.operation == opEncode || opt.operation == opPlay || opt.operation == opToVideo) {
			if !hasNextArg {
				err = fmt.Errorf("-c <color profile file>")
				return
			}
			opt.colorProfileInput = nextArg
			i++
			continue
		}

		const ddBufSizeEqual = "--buffer-size="
		if strings.HasPrefix(currentArg, ddBufSizeEqual) && (opt.operation == opRecord || opt.operation == opEncode || opt.operation == opOptimize || opt.operation == opToVideo) {
			equals := currentArg[len(ddBufSizeEqual):]
			sm := regXxY.FindStringSubmatch(equals)
			if sm == nil {
				err = fmt.Errorf("--buffer-size=<rows>x<cols>")
				return
			}
			var rows, cols int
			rows, err = strconv.Atoi(sm[1])
			if err != nil {
				return
			}
			cols, err = strconv.Atoi(sm[2])
			if err != nil {
				return
			}
			opt.bufferSize = sizeStruct{rows: rows, cols: cols}
			continue
		}

		const ddEvenIfNotTty = "--even-if-not-tty"
		if currentArg == ddEvenIfNotTty && (opt.operation == opRecord || opt.operation == opPlay || opt.operation == opGetColorProfile) {
			opt.evenIfNotTty = true
			continue
		}

		if opt.operation == opRecord {
			if currentArg == "-s" {
				if !hasNextArg {
					err = fmt.Errorf("-s <shell>")
					return
				}
				opt.shell = nextArg
				i++
				continue
			}

			if currentArg == "-q" {
				opt.quiet = true
				continue
			}

			if currentArg[0] != '-' {
				if nbNonOptionArgs == 0 {
					nbNonOptionArgs++
					opt.itsOutput = currentArg
					continue
				}
			}
		}

		if opt.operation == opEncode {
			if currentArg[0] != '-' {
				if nbNonOptionArgs == 0 {
					nbNonOptionArgs++
					opt.script = currentArg
					continue
				}
				if nbNonOptionArgs == 1 {
					nbNonOptionArgs++
					opt.timing = currentArg
					continue
				}
				if nbNonOptionArgs == 2 {
					nbNonOptionArgs++
					opt.itsOutput = currentArg
					continue
				}
			}
		}

		if opt.operation == opPlay {
			if currentArg[0] != '-' {
				if nbNonOptionArgs == 0 {
					nbNonOptionArgs++
					opt.itsInput = currentArg
					continue
				}
			}
		}

		if opt.operation == opOptimize {
			if currentArg[0] != '-' {
				if nbNonOptionArgs == 0 {
					nbNonOptionArgs++
					opt.itsInput = currentArg
					continue
				}
				if nbNonOptionArgs == 1 {
					nbNonOptionArgs++
					opt.itsOutput = currentArg
					continue
				}
			}
		}

		if opt.operation == opCheckColorProfile {
			if currentArg[0] != '-' {
				if nbNonOptionArgs == 0 {
					nbNonOptionArgs++
					opt.colorProfileInput = currentArg
					continue
				}
			}
		}

		if opt.operation == opToVideo {
			const ddFontEqual = "--font="
			if strings.HasPrefix(currentArg, ddFontEqual) && (opt.operation == opToVideo) {
				equals := currentArg[len(ddFontEqual):]
				if strings.ContainsAny(equals, "-,:=_") {
					err = fmt.Errorf("Font-config pattern detected. Pass family name directly instead")
					return
				}
				opt.fontFamily = equals
				continue
			}

			const ddDpiEqual = "--dpi="
			if strings.HasPrefix(currentArg, ddDpiEqual) && (opt.operation == opToVideo) {
				equals := currentArg[len(ddDpiEqual):]
				var f float64
				f, err = strconv.ParseFloat(equals, 64)
				if err != nil {
					err = fmt.Errorf("--dpi=number")
					return
				}
				if f <= 0 {
					err = fmt.Errorf("dpi must be positive")
					return
				}
				opt.dpi = f
				continue
			}

			const ddFfplay = "--ffplay"
			if currentArg == ddFfplay {
				opt.ffplay = true
				continue
			}

			if currentArg == "-ss" {
				if !hasNextArg {
					err = fmt.Errorf("-ss <skip frames>")
					return
				}
				var ss uint64
				ss, err = strconv.ParseUint(nextArg, 10, 64)
				if err != nil {
					return
				}
				opt.ss = ss
				i++
				continue
			}

			if currentArg == "-t" {
				if !hasNextArg {
					err = fmt.Errorf("-t <frames>")
					return
				}
				var t uint64
				t, err = strconv.ParseUint(nextArg, 10, 64)
				if err != nil {
					return
				}
				opt.t = t
				i++
				continue
			}

			if currentArg[0] != '-' {
				if nbNonOptionArgs == 0 {
					nbNonOptionArgs++
					opt.itsInput = currentArg
					continue
				}
				if nbNonOptionArgs == 1 {
					nbNonOptionArgs++
					opt.videoOutput = currentArg
					continue
				}
			}
		}

		err = fmt.Errorf("Unused argument %v", strconv.Quote(currentArg))
		return
	}
	switch opt.operation {
	case opRecord:
		if nbNonOptionArgs != 1 {
			err = fmt.Errorf("Expected output file as argument")
			return
		}
	case opEncode:
		if nbNonOptionArgs != 3 {
			err = fmt.Errorf("Expected 3 files as argument: script, timing and output")
			return
		}
	case opPlay:
		if nbNonOptionArgs != 1 {
			err = fmt.Errorf("Expected input file as argument")
			return
		}
	case opOptimize:
		if nbNonOptionArgs != 2 {
			err = fmt.Errorf("Expected 2 files as argument: input and output")
			return
		}
	case opGetColorProfile:
		if nbNonOptionArgs != 0 {
			err = fmt.Errorf("Expected no additional arguments")
			return
		}
	case opCheckColorProfile:
		if nbNonOptionArgs != 1 {
			err = fmt.Errorf("Expected a color profile image as argument")
			return
		}
	case opToVideo:
		if nbNonOptionArgs == 1 && !opt.ffplay {
			err = fmt.Errorf("Expected 1 more additional arguments: <output>")
			return
		}
		if nbNonOptionArgs == 2 && opt.ffplay {
			err = fmt.Errorf("No output can be produced if --ffplay used. Remove the output argument")
			return
		}
		if nbNonOptionArgs != 1 && nbNonOptionArgs != 2 {
			if opt.ffplay {
				err = fmt.Errorf("Expected 1 additional arguments: <input>")
			} else {
				err = fmt.Errorf("Expected 2 additional arguments: <input> and <output>")
			}
			return
		}
		if opt.colorProfileInput == "" {
			err = fmt.Errorf("Requires color profile. Pass with -c.")
			return
		}
	default:
		err = fmt.Errorf("Unknown operation %v", opt.operation)
		return
	}
	return
}
