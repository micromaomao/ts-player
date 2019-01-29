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
}

const (
	opEncode            = "encode"
	opPlay              = "play"
	opRecord            = "record"
	opOptimize          = "optimize"
	opGetColorProfile   = "get-color-profile"
	opCheckColorProfile = "check-color-profile"
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
	default:
		// default case handled by parseArgs
		panic("!")
	}
}

type Palettle [18]uint32

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
			continue
		}

		if currentArg == "--debug" {
			outputDebugLog = true
			continue
		}

		if currentArg == "-f" && (opt.operation == opRecord || opt.operation == opEncode) {
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

		if currentArg == "-c" && (opt.operation == opRecord || opt.operation == opEncode || opt.operation == opPlay) {
			if !hasNextArg {
				err = fmt.Errorf("-c <color profile file>")
				return
			}
			opt.colorProfileInput = nextArg
			i++
			continue
		}

		const ddBufSizeEqual = "--buffer-size="
		if strings.HasPrefix(currentArg, ddBufSizeEqual) && (opt.operation == opRecord || opt.operation == opEncode || opt.operation == opOptimize) {
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
	default:
		err = fmt.Errorf("Unknown operation %v", opt.operation)
		return
	}
	return
}
