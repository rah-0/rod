// Package defaults provides commonly used Rod options.
// Check Load and ResetWith for command-line configuration.
package defaults

import (
	"errors"
	"flag"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/utils"
)

// Trace is the default of rod.Browser.Trace .
// Option name is "trace".
var Trace bool

// Slow is the default of rod.Browser.SlowMotion .
// The format is same as https://golang.org/pkg/time/#ParseDuration
// Option name is "slow".
var Slow time.Duration

// Monitor is the default of rod.Browser.ServeMonitor .
// Option name is "monitor".
var Monitor string

// Show is the default of launcher.Launcher.Headless .
// Option name is "show".
var Show bool

// Devtools is the default of launcher.Launcher.Devtools .
// Option name is "devtools".
var Devtools bool

// Dir is the default of launcher.Launcher.UserDataDir .
// Option name is "dir".
var Dir string

// Port is the default of launcher.Launcher.RemoteDebuggingPort .
// Option name is "port".
var Port = "0"

// Bin is the default of launcher.Launcher.Bin .
// Option name is "bin".
var Bin string

// Proxy is the default of launcher.Launcher.Proxy
// Option name is "proxy".
var Proxy string

// URL is the default websocket url for remote control a browser.
// Option name is "url".
var URL string

// CDP is the default of cdp.Client.Logger
// Option name is "cdp".
var CDP = utils.LoggerQuiet

var loadOnce sync.Once

// Load applies the optional -rod command-line configuration once.
//
// If [flag.CommandLine] has already been parsed, Load uses only the parsed
// value of a defined -rod flag. Otherwise it defines -rod on [flag.CommandLine]
// unless the application already has, then reads [os.Args] with the flag
// package's rules: -rod and --rod take "=value" or the next argument, and
// reading stops at "--" or at the first non-flag argument. Flags already defined
// on [flag.CommandLine] follow their definitions; bool flags never consume the
// next argument. Reading also stops at an undefined flag without "=value",
// because its value cannot be distinguished from the next argument. Put -rod
// before such flags, use the -name=value form, or define them before calling Load.
// Undefined test.* flags never consume the next argument, so -rod given to
// "go test" also applies to Rod objects created while a test binary
// initializes its packages, before the testing package defines those flags.
// Pass -rod to "go test" before any "--".
//
// When Load defines -rod, a later [flag.Parse] applies each -rod value it
// parses, in order, so values after flags defined later also apply. Call Load
// before flag.Parse, and assign exported defaults after flag.Parse. A test
// binary accepts -rod only when it is defined before the testing package
// parses flags: call Load in TestMain before calling m.Run, or create a Rod
// object during package initialization. Otherwise "go test" reports "flag
// provided but not defined: -rod".
func Load() {
	loadOnce.Do(loadCLI)
}

// Reset restores all options to their base values.
func Reset() {
	// An explicit reset must not be undone by a later constructor calling Load.
	loadOnce.Do(func() {})
	resetValues()
}

func resetValues() {
	Trace = false
	Slow = 0
	Monitor = ""
	Show = false
	Devtools = false
	Dir = ""
	Port = "0"
	Bin = ""
	Proxy = ""
	URL = ""
	CDP = utils.LoggerQuiet
}

var envParsers = map[string]func(string) error{
	"trace": func(string) error {
		Trace = true
		return nil
	},
	"slow": func(v string) error {
		slow, err := time.ParseDuration(v)
		if err != nil {
			return errors.New("invalid value for \"slow\": " + err.Error() +
				" (learn format from https://golang.org/pkg/time/#ParseDuration)")
		}
		Slow = slow
		return nil
	},
	"monitor": func(v string) error {
		Monitor = "127.0.0.1:0"
		if v != "" {
			Monitor = v
		}
		return nil
	},
	"show": func(string) error {
		Show = true
		return nil
	},
	"devtools": func(string) error {
		Devtools = true
		return nil
	},
	"dir": func(v string) error {
		Dir = v
		return nil
	},
	"port": func(v string) error {
		Port = v
		return nil
	},
	"bin": func(v string) error {
		Bin = v
		return nil
	},
	"proxy": func(v string) error {
		Proxy = v
		return nil
	},
	"url": func(v string) error {
		URL = v
		return nil
	},
	"cdp": func(string) error {
		CDP = log.New(log.Writer(), "[cdp] ", log.LstdFlags)
		return nil
	},
}

// ResetWith options and "-rod" command line flag.
// It resets the defaults, loads the command-line flag as described by [Load],
// and then applies options. Explicit options override values from the command line.
// If you want to disable the global cli argument flag, set env DISABLE_ROD_FLAG.
// Values are separated by commas, key and value are separated by "=". For example:
//
//	go run main.go -rod=show
//	go run main.go -rod show,trace,slow=1s,monitor
//	go run main.go --rod="slow=1s,dir=path/has /space,monitor=127.0.0.1:9223"
func ResetWith(options string) {
	Reset()
	loadCLI()
	parse(options)
}

func loadCLI() {
	if _, has := os.LookupEnv("DISABLE_ROD_FLAG"); has {
		return
	}

	if flag.Parsed() {
		// The flag package has already separated flags from other arguments,
		// including those after "--".
		if f := flag.Lookup("rod"); f != nil {
			parse(f.Value.String())
		}
		return
	}

	if flag.Lookup("rod") == nil {
		flag.Var(&commandLineOption{}, "rod", `Set the default value of options used by rod.`)
	}
	for _, options := range commandLineOptions(flag.CommandLine, os.Args[1:]) {
		parse(options)
	}
}

// commandLineOption is the -rod flag registered by Load. Parsing applies each
// occurrence; String returns all occurrences, in order, for a later ResetWith.
type commandLineOption struct {
	values []string
}

func (o *commandLineOption) String() string {
	if o == nil {
		return ""
	}
	return strings.Join(o.values, ",")
}

func (o *commandLineOption) Set(options string) error {
	if err := parseOptions(options); err != nil {
		return err
	}
	o.values = append(o.values, options)
	return nil
}

// commandLineOptions returns each -rod value in args, following the argument
// rules of fs.Parse without parsing or modifying fs.
func commandLineOptions(fs *flag.FlagSet, args []string) []string {
	var values []string
	for len(args) > 0 {
		arg := args[0]
		if len(arg) < 2 || arg[0] != '-' {
			break
		}
		name := arg[1:]
		if name[0] == '-' {
			if len(name) == 1 {
				break // "--" terminates the flags.
			}
			name = name[1:]
		}
		if name[0] == '-' || name[0] == '=' {
			break // fs.Parse rejects this syntax.
		}
		args = args[1:]
		name, value, hasValue := strings.Cut(name, "=")

		if name == "rod" {
			if !hasValue {
				if len(args) == 0 {
					break
				}
				value, args = args[0], args[1:]
			}
			values = append(values, value)
			continue
		}

		f := fs.Lookup(name)
		if f == nil {
			if hasValue && name != "help" && name != "h" {
				continue
			}
			if strings.HasPrefix(name, "test.") {
				// A test binary defines its test.* flags only after package
				// initialization. The go command passes them as
				// -test.name=value, except the boolean -test.paniconexit0.
				continue
			}
			// Without a definition, the next argument may be this flag's value.
			break
		}
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if !hasValue {
			if len(args) == 0 {
				break
			}
			args = args[1:]
		}
	}
	return values
}

// parse options and set them globally.
func parse(options string) {
	if err := parseOptions(options); err != nil {
		panic(err.Error())
	}
}

func parseOptions(options string) error {
	if options == "" {
		return nil
	}

	reg := regexp.MustCompile(`[,\r\n]`)

	for _, str := range reg.Split(options, -1) {
		n, v, _ := strings.Cut(str, "=")
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}

		f := envParsers[n]
		if f == nil {
			return errors.New("unknown rod env option: " + n)
		}
		if err := f(v); err != nil {
			return err
		}
	}
	return nil
}
