package defaults

import (
	"flag"
	"os"
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

func try(fn func()) (err interface{}) {
	defer func() {
		err = recover()
	}()

	fn()

	return err
}

func TestParseFlag(t *testing.T) {
	g := testutil.T(t)

	Reset()

	parseFlag([]string{"-rod"})
	g.False(Show)

	parseFlag([]string{"-rod=show"})
	g.True(Show)

	Reset()

	parseFlag([]string{"-rod", "show"})
	g.True(Show)
}
