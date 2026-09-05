package utils_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/utils"
)

var setup = testutil.Setup(nil)

func TestNoop(_ *testing.T) {
	utils.Noop()
}

func TestTestLog(t *testing.T) {
	g := setup(t)

	var res []any
	lg := utils.Log(func(msg ...any) { res = append(res, msg[0]) })
	lg.Println("ok")
	g.Eq(res[0], "ok")

	utils.LoggerQuiet.Println()

	utils.MultiLogger(lg, lg).Println("ok")
	g.Eq(res, []any{"ok", "ok", "ok"})
}

func TestTestE(t *testing.T) {
	g := setup(t)

	utils.E(nil)

	g.Panic(func() {
		utils.E(errors.New("err"))
	})
}

func TestSTemplate(t *testing.T) {
	g := setup(t)

	out := utils.S(
		"{{.a}} {{.b}} {{.c.A}} {{d}}",
		"a", "<value>",
		"b", 10,
		"c", struct{ A string }{"ok"},
		"d", func() string {
			return "ok"
		},
	)
	g.Eq("<value> 10 ok ok", out)
}

func TestGenerateRandomString(t *testing.T) {
	g := setup(t)

	v := utils.RandString(10)
	raw, _ := hex.DecodeString(v)
	g.Len(raw, 10)
}

func TestMkdir(t *testing.T) {
	g := setup(t)

	p := filepath.Join(g.Testable.(*testing.T).TempDir(), "t")
	g.E(utils.Mkdir(p))
}

func TestAbsolutePaths(t *testing.T) {
	g := setup(t)

	p := utils.AbsolutePaths([]string{"utils.go"})
	g.Has(p[0], filepath.FromSlash("/utils.go"))
}

func TestOutputString(t *testing.T) {
	g := setup(t)

	p := filepath.Join(t.TempDir(), "output.txt")

	g.E(utils.OutputFile(p, p))

	s, err := utils.ReadString(p)
	if err != nil {
		panic(err)
	}

	g.Eq(s, p)
}

func TestOutputBytes(t *testing.T) {
	g := setup(t)

	p := filepath.Join(t.TempDir(), "output.txt")

	g.E(utils.OutputFile(p, []byte("test")))

	s, err := utils.ReadString(p)
	if err != nil {
		panic(err)
	}

	g.Eq(s, "test")
}

func TestOutputStream(t *testing.T) {
	g := setup(t)

	p := filepath.Join(t.TempDir(), "output.txt")
	b := bytes.NewBufferString("test")

	g.E(utils.OutputFile(p, b))

	s, err := utils.ReadString(p)
	if err != nil {
		panic(err)
	}

	g.Eq("test", s)
}

func TestOutputJSONErr(t *testing.T) {
	g := setup(t)

	p := filepath.Join(t.TempDir(), "output.json")

	g.Panic(func() {
		_ = utils.OutputFile(p, make(chan struct{}))
	})
}

func TestOutputFileInvalidTarget(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{parent + "/child", t.TempDir()} {
		t.Run(target, func(t *testing.T) {
			for _, data := range []any{"text", []byte("bytes"), bytes.NewBufferString("reader")} {
				if err := utils.OutputFile(target, data); err == nil {
					t.Fatalf("OutputFile(%q, %T) succeeded", target, data)
				}
			}
		})
	}
}

func TestOutputFileReaderError(t *testing.T) {
	wantErr := errors.New("reader failed")
	reader := io.MultiReader(bytes.NewBufferString("partial"), iotest.ErrReader(wantErr))
	path := filepath.Join(t.TempDir(), "nested", "output.txt")
	if err := utils.OutputFile(path, reader); !errors.Is(err, wantErr) {
		t.Fatalf("OutputFile error = %v, want %v", err, wantErr)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "partial" {
		t.Fatalf("partial output = %q, %v", got, err)
	}
}

func TestSleep(_ *testing.T) {
	utils.Sleep(0.01)
}

func TestAll(t *testing.T) {
	g := setup(t)

	c := g.Count(3)
	utils.All(c, c, c)()
}

func TestPause(_ *testing.T) {
	go utils.Pause()
}

func TestMustToJSON(t *testing.T) {
	g := setup(t)

	g.Eq(utils.Dump("a", 10), `"a" 10`)
	g.Eq(`{"a":1}`, utils.MustToJSON(map[string]int{"a": 1}))
}

func TestFileExists(t *testing.T) {
	g := setup(t)

	g.Eq(false, utils.FileExists("."))
	g.Eq(true, utils.FileExists("utils.go"))
	g.Eq(false, utils.FileExists(g.RandStr(16)))
}

func TestExec(t *testing.T) {
	g := setup(t)

	g.Has(utils.Exec("go version"), "go version")
}

func TestExecErr(t *testing.T) {
	g := setup(t)

	g.Panic(func() {
		utils.Exec("")
	})
	g.Panic(func() {
		utils.Exec(g.RandStr(16))
	})
	g.Panic(func() {
		utils.ExecLine(false, "", "")
	})
}

func TestFormatCLIArgs(t *testing.T) {
	g := setup(t)

	g.Eq(utils.FormatCLIArgs([]string{"ab c", "abc"}), `"ab c" abc`)
}

func TestEscapeGoString(t *testing.T) {
	g := setup(t)

	g.Eq("`` + \"`\" + `test` + \"`\" + ``", utils.EscapeGoString("`test`"))
}

func TestIdleCounter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := setup(t)
		ct := utils.NewIdleCounter(100 * time.Millisecond)
		ct.Add()
		ct.Add()
		go func() {
			time.Sleep(300 * time.Millisecond)
			ct.Done()
			ct.Done()
		}()

		start := time.Now()
		ct.Wait(t.Context())
		g.Eq(time.Since(start), 400*time.Millisecond)
		g.Panic(ct.Done)
	})
}

func TestIdleCounterWithoutJobs(t *testing.T) {
	for _, duration := range []time.Duration{0, 100 * time.Millisecond} {
		t.Run(duration.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ct := utils.NewIdleCounter(duration)
				start := time.Now()
				ct.Wait(t.Context())
				if got := time.Since(start); got != duration {
					t.Fatalf("idle duration = %s, want %s", got, duration)
				}
			})
		})
	}
}

func TestIdleCounterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ct := utils.NewIdleCounter(time.Second)
		done := make(chan struct{})
		go func() {
			ct.Wait(t.Context())
			close(done)
		}()
		synctest.Wait()
		synctest.Sleep(500 * time.Millisecond)
		ct.Add()
		synctest.Sleep(time.Second)
		select {
		case <-done:
			t.Fatal("idle wait completed while a job was active")
		default:
		}
		ct.Done()
		synctest.Sleep(time.Second - time.Nanosecond)
		select {
		case <-done:
			t.Fatal("idle wait completed before its restarted deadline")
		default:
		}
		synctest.Sleep(time.Nanosecond)
		select {
		case <-done:
		default:
			t.Fatal("idle wait did not complete after its restarted deadline")
		}
	})
}

func TestIdleCounterCancelAndReuse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ct := utils.NewIdleCounter(time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		done := false
		go func() {
			ct.Wait(ctx)
			done = true
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if !done {
			t.Fatal("idle wait did not observe cancellation")
		}
		// An abandoned timer must not leave a stale value for the next wait.
		synctest.Sleep(2 * time.Second)
		start := time.Now()
		ct.Wait(t.Context())
		if got := time.Since(start); got != time.Second {
			t.Fatalf("reused idle duration = %s, want 1s", got)
		}
	})
}

func TestCropImage(t *testing.T) {
	g := setup(t)

	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))

	g.Err(utils.CropImage(nil, 0, 0, 0, 0, 0))

	bin := bytes.NewBuffer(nil)
	g.E(png.Encode(bin, img))
	g.E(utils.CropImage(bin.Bytes(), 0, 10, 10, 30, 30))

	bin = bytes.NewBuffer(nil)
	g.E(jpeg.Encode(bin, img, &jpeg.Options{Quality: 80}))
	g.E(utils.CropImage(bin.Bytes(), 0, 10, 10, 30, 30))
}

func TestUseNode(t *testing.T) {
	g := setup(t)

	utils.UseNode(false)

	p, err := exec.LookPath("node")
	g.E(err)
	g.Eq(p != "", true)
}
