package defaults

import (
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod/internal/testutil"
)

func TestLoad(t *testing.T) {
	g := testutil.T(t)

	originalArgs := os.Args
	originalFlags := flag.CommandLine
	disabled, hadDisabled := os.LookupEnv("DISABLE_ROD_FLAG")
	t.Cleanup(func() {
		os.Args = originalArgs
		flag.CommandLine = originalFlags
		if hadDisabled {
			_ = os.Setenv("DISABLE_ROD_FLAG", disabled)
		} else {
			_ = os.Unsetenv("DISABLE_ROD_FLAG")
		}
		loadOnce = sync.Once{}
		Reset()
	})

	if err := os.Unsetenv("DISABLE_ROD_FLAG"); err != nil {
		t.Fatal(err)
	}
	flag.CommandLine = flag.NewFlagSet("defaults-load", flag.ContinueOnError)
	os.Args = []string{"defaults-load", "-rod=show,port=9222"}
	loadOnce = sync.Once{}
	resetValues()

	Load()
	g.True(Show)
	g.Eq("9222", Port)
	g.NotNil(CDP)
	g.NotNil(flag.Lookup("rod"))

	os.Args = []string{"defaults-load", "-rod=devtools"}
	Load()
	g.False(Devtools)
	Show = false
	Load()
	g.False(Show)

	loadOnce = sync.Once{}
	resetValues()
	Reset()
	Load()
	g.False(Devtools)

	if err := os.Setenv("DISABLE_ROD_FLAG", "1"); err != nil {
		t.Fatal(err)
	}
	flag.CommandLine = flag.NewFlagSet("defaults-disabled", flag.ContinueOnError)
	loadOnce = sync.Once{}
	resetValues()
	Load()
	g.False(Devtools)
	g.Nil(flag.Lookup("rod"))
}

func TestBasic(t *testing.T) {
	g := testutil.T(t)

	Show = true
	Devtools = true
	URL = "test"
	Monitor = "test"

	ResetWith("")
	parse("")
	g.False(Show)
	g.False(Devtools)
	g.Eq("", Monitor)
	g.Eq("", URL)

	parse("show,devtools,trace,slow=2s,port=8080,dir=tmp," +
		"url=http://test.com,cdp,monitor,bin=/path/to/chrome," +
		"proxy=localhost:8080,",
	)

	g.True(Show)
	g.True(Devtools)
	g.True(Trace)
	g.Eq(2*time.Second, Slow)
	g.Eq("8080", Port)
	g.Eq("/path/to/chrome", Bin)
	g.Eq("tmp", Dir)
	g.Eq("http://test.com", URL)
	g.NotNil(CDP.Println)
	g.Eq("127.0.0.1:0", Monitor)
	g.Eq("localhost:8080", Proxy)

	parse("monitor=:1234")
	g.Eq(":1234", Monitor)

	g.Panic(func() {
		parse("a")
	})

	g.Eq(try(func() { parse("slow=1") }), "invalid value for \"slow\": time: missing unit in duration \"1\" (learn format from https://golang.org/pkg/time/#ParseDuration)")
}

func try(fn func()) (err any) {
	defer func() {
		err = recover()
	}()

	fn()

	return err
}

// isolateCommandLine restores the process command line, flag set, and defaults
// after a test replaces them.
func isolateCommandLine(t *testing.T) {
	t.Helper()
	originalArgs := os.Args
	originalFlags := flag.CommandLine
	disabled, hadDisabled := os.LookupEnv("DISABLE_ROD_FLAG")
	t.Cleanup(func() {
		os.Args = originalArgs
		flag.CommandLine = originalFlags
		if hadDisabled {
			_ = os.Setenv("DISABLE_ROD_FLAG", disabled)
		} else {
			_ = os.Unsetenv("DISABLE_ROD_FLAG")
		}
		loadOnce = sync.Once{}
		Reset()
	})
	if err := os.Unsetenv("DISABLE_ROD_FLAG"); err != nil {
		t.Fatal(err)
	}
}

// loadCommandLine runs Load for args with a fresh flag set that defines the
// bool flag -verbose and the string flag -name.
func loadCommandLine(args ...string) *flag.FlagSet {
	fs := flag.NewFlagSet("tool", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("verbose", false, "")
	fs.String("name", "", "")
	flag.CommandLine = fs
	os.Args = append([]string{"tool"}, args...)
	loadOnce = sync.Once{}
	resetValues()
	Load()
	return fs
}

func TestLoadCommandLineBoundaries(t *testing.T) {
	isolateCommandLine(t)

	const untrusted = "-rod=bin=/tmp/evil,proxy=127.0.0.1:1,url=ws://attacker.invalid," +
		"monitor=0.0.0.0:9273,trace,cdp"
	for _, test := range []struct {
		name        string
		args        []string
		show, trace bool
	}{
		{"argument terminator", []string{"--", untrusted}, false, false},
		{"positional argument", []string{"input", untrusted}, false, false},
		{"standard input argument", []string{"-", untrusted}, false, false},
		{"after flags and terminator", []string{"-verbose", "--", untrusted}, false, false},
		{"value of registered flag", []string{"-name", untrusted}, false, false},
		{"after unregistered flag", []string{"-unregistered", untrusted}, false, false},
		{"after help flag", []string{"-h=1", untrusted}, false, false},
		{"after bad syntax", []string{"---rod=show", untrusted}, false, false},
		{"missing value", []string{"-rod"}, false, false},
		{"inline value", []string{"-rod=show"}, true, false},
		{"double dash inline value", []string{"--rod=show"}, true, false},
		{"separate value", []string{"-rod", "show"}, true, false},
		{"double dash separate value", []string{"--rod", "show"}, true, false},
		{"after bool flag", []string{"-verbose", "-rod=show"}, true, false},
		{"after bool flag value", []string{"--verbose=false", "-rod=show"}, true, false},
		{"after registered flag value", []string{"-name", "value", "--rod", "show"}, true, false},
		{"after inline values", []string{"-name=value", "-unregistered=value", "-rod=show"}, true, false},
		{"repeated", []string{"-rod=show", "-rod", "trace", "input", "-rod=devtools"}, true, true},
		// The go command's test flags before the testing package defines them.
		{"go test flags", []string{
			"-test.testlogfile=/tmp/testlog.txt", "-test.paniconexit0", "-test.timeout=10m0s",
			"-test.v=true", "-rod=show",
		}, true, false},
		{"after undefined test flag", []string{"-test.v", "--rod", "show"}, true, false},
		{"after undefined test flag value", []string{"-test.run", "TestX", untrusted}, false, false},
		{"test flags and terminator", []string{"-test.paniconexit0", "--", untrusted}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := testutil.T(t)
			loadCommandLine(test.args...)
			g.Eq(test.show, Show)
			g.Eq(test.trace, Trace)
			g.False(Devtools)
			g.Eq("", Bin)
			g.Eq("", Proxy)
			g.Eq("", URL)
			g.Eq("", Monitor)
		})
	}
}

func TestLoadParsedCommandLine(t *testing.T) {
	g := testutil.T(t)
	isolateCommandLine(t)

	// Parsing after Load applies -rod with the definitions registered by then.
	fs := loadCommandLine("-late", "value", "-rod=show", "input", "-rod=trace")
	g.False(Show)
	fs.String("late", "", "")
	g.E(fs.Parse(os.Args[1:]))
	g.True(Show)
	g.False(Trace)
	var usage strings.Builder
	fs.SetOutput(&usage)
	fs.PrintDefaults()
	g.Has(usage.String(), "-rod")

	// ResetWith reuses only the parsed value.
	ResetWith("devtools")
	g.True(Show)
	g.False(Trace)
	g.True(Devtools)

	fs = loadCommandLine()
	err := fs.Parse([]string{"-rod=show,unknown"})
	g.Err(err)
	g.Has(err.Error(), "unknown rod env option: unknown")

	// Once the command line is parsed, arguments are not scanned again.
	fs = flag.NewFlagSet("tool", flag.ContinueOnError)
	g.E(fs.Parse([]string{"--", "-rod=trace"}))
	flag.CommandLine = fs
	os.Args = []string{"tool", "--", "-rod=trace"}
	loadOnce = sync.Once{}
	resetValues()
	Load()
	g.False(Trace)
	g.Nil(flag.Lookup("rod"))

	// An application's own -rod flag supplies its last parsed value.
	fs = flag.NewFlagSet("tool", flag.ContinueOnError)
	fs.String("rod", "", "")
	os.Args = []string{"tool", "-rod=trace", "-rod=show", "--", "-rod=devtools"}
	g.E(fs.Parse(os.Args[1:]))
	flag.CommandLine = fs
	loadOnce = sync.Once{}
	resetValues()
	Load()
	g.True(Show)
	g.False(Trace)
	g.False(Devtools)
}

// loadDuringInitEnv makes a child test binary call Load during package
// initialization.
const loadDuringInitEnv = "ROD_DEFAULTS_TEST_LOAD_DURING_INIT"

// showDuringInit is Show after a Load call during package initialization, as
// in a package-level launcher.New or rod.New in a test file.
var showDuringInit = func() bool {
	if os.Getenv(loadDuringInitEnv) == "" {
		return false
	}
	Load()
	return Show
}()

func TestLoadDuringTestInitialization(t *testing.T) {
	if os.Getenv(loadDuringInitEnv) != "" {
		if !showDuringInit {
			t.Fatalf("-rod was not applied during package initialization: %q", os.Args)
		}
		return
	}

	// The go command passes -test.paniconexit0 first and its other test flags
	// as -test.name=value, followed by the arguments given to go test.
	cmd := exec.Command(os.Args[0],
		"-test.paniconexit0", "-test.timeout=1m0s",
		"-test.run=^TestLoadDuringTestInitialization$", "-rod=show",
	)
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "DISABLE_ROD_FLAG=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, loadDuringInitEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
