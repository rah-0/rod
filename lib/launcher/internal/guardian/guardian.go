//go:build !windows

package guardian

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"path"
	"runtime"
	"sync"
	"syscall"
)

const (
	// Argument is the first argument of the private invocation.
	Argument = "--rod-launcher-guardian"
	// Env is the environment variable that marks the private invocation.
	Env = "_ROD_LAUNCHER_GUARDIAN"
)

// Command describes the browser process that the supervisor starts. The
// launcher sends it once the supervisor has reported that it is ready.
type Command struct {
	Path        string
	Args        []string
	Env         []string
	Dir         string
	Preferences string
	Profile     string
	// Scratch lists unpredictable candidate names for the browser's private
	// temporary directory. The launcher generates them, because this package
	// cannot import crypto/rand.
	Scratch []string
}

// Status is a report from the supervisor to the launcher.
type Status struct {
	PID   int
	Error string
	Errno syscall.Errno
	Op    string
	Path  string
	Ready bool
}

// SetError records err so that [Status.Err] can rebuild it in the launcher.
func (s *Status) SetError(err error) {
	s.Error = err.Error()
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		s.Op = pathError.Op
		s.Path = pathError.Path
	}
	_ = errors.As(err, &s.Errno)
}

// Err returns the reported error.
func (s Status) Err() error {
	var err error = errors.New(s.Error)
	if s.Errno != 0 {
		err = s.Errno
		if s.Op != "" {
			err = &os.PathError{Op: s.Op, Path: s.Path, Err: err}
		}
	}
	return err
}

// A separate process must own cleanup: a goroutine cannot run after its owner
// panics, calls os.Exit, or is killed by a test timeout or SIGKILL. The private
// invocation also requires two inherited pipes, so an environment variable
// alone never changes the normal executable's behavior.
func init() {
	if os.Getenv(Env) != "1" || len(os.Args) != 4 || os.Args[1] != Argument {
		return
	}
	status := os.NewFile(3, "rod-launcher-status")
	if !pipe(os.Stdin) || !pipe(status) {
		return
	}
	syscall.CloseOnExec(int(status.Fd()))
	os.Exit(run(status, os.Args[2], os.Args[3]))
}

func pipe(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeNamedPipe != 0
}

// discardExitSignals makes the supervisor outlive SIGHUP, SIGINT, SIGQUIT and
// SIGTERM, whichever package initializers ran in it. The supervisor ends when
// its owner's pipe closes or the browser exits. If one of these signals ended
// it, for example one sent to every process that carries the application's
// name or to all processes of a service, the browser would keep running with
// nobody left to stop it or to remove its directories.
//
// The signals are delivered to a channel that nobody reads. signal.Ignore is
// not used: the browser would inherit an ignored signal, while it starts with
// the default action for a signal that is handled here. A signal that is
// ignored already, such as SIGHUP under nohup, is left alone so that the
// browser inherits that as well.
//
// The first signal.Notify call starts OS threads, and a Go program that cannot
// create a thread aborts. The supervisor therefore calls this before it reports
// that it is ready: failing then fails the launch, while failing later would
// leave a browser behind.
func discardExitSignals() {
	discarded := make(chan os.Signal, 1)
	for _, sig := range []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM} {
		if !signal.Ignored(sig) {
			signal.Notify(discarded, sig)
		}
	}
}

func run(status *os.File, profile, managedProfile string) int {
	discardExitSignals()
	defer status.Close()
	if profile != "" {
		defer os.RemoveAll(profile)
	}
	report := func(pid int, err error) bool {
		result := Status{PID: pid}
		if err != nil {
			result.SetError(err)
		}
		return WriteStatus(status, result) == nil
	}
	cleanupManaged, err := managedCleanup(managedProfile)
	if err != nil {
		report(0, err)
		return 1
	}
	defer cleanupManaged()
	if err := Subreaper(); err != nil {
		report(0, err)
		return 1
	}
	if err := WriteStatus(status, Status{Ready: true}); err != nil {
		return 1
	}
	configuration, err := ReadCommand(os.Stdin)
	if err != nil {
		report(0, err)
		return 1
	}
	cleanupTemporary, err := TemporaryDir(&configuration)
	if err != nil {
		report(0, err)
		return 1
	}
	defer cleanupTemporary()
	if profile != "" && configuration.Preferences != "" {
		if err := preferences(profile, configuration); err != nil {
			report(0, err)
			return 1
		}
	}
	process, err := start(configuration)
	if err != nil {
		report(0, err)
		return 1
	}
	// The liveness descriptor belongs only to the parent and this guardian. The
	// browser inherits neither end, so owner death reliably produces EOF.
	var lifetime sync.Mutex
	finished := false
	stop := func() {
		lifetime.Lock()
		defer lifetime.Unlock()
		if !finished {
			kill(process)
		}
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		stop()
	}()
	if !report(process.Pid, nil) {
		stop()
	}
	_ = status.Close()
	_, _ = process.Wait()
	lifetime.Lock()
	// Chrome helpers can outlive the browser's main process. The guardian owns
	// their cleanup even after graceful Browser.close or a startup failure.
	ReapChildren(process)
	finished = true
	lifetime.Unlock()
	return 0
}

// start runs the browser the way exec.Cmd.Start does for these fields, without
// importing os/exec: no standard input, and this process's standard output and
// error.
func start(configuration Command) (*os.Process, error) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	args := configuration.Args
	if len(args) == 0 {
		args = []string{configuration.Path}
	}
	return os.StartProcess(configuration.Path, args, &os.ProcAttr{
		Dir:   configuration.Dir,
		Env:   configuration.Env,
		Files: []*os.File{input, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	})
}

// TemporaryDir gives the browser a private scratch directory and returns the
// function that removes it.
//
// Chrome creates singleton sockets and other scratch files outside its user
// profile. Give it a private child of its requested temporary directory so an
// abrupt exit cannot leave these files behind in a shared directory.
func TemporaryDir(configuration *Command) (func(), error) {
	base := "/tmp"
	env := make([]string, 0, len(configuration.Env)+1)
	const variable = "TMPDIR"
	for _, entry := range configuration.Env {
		if entry == variable {
			base = ""
			continue
		}
		if len(entry) > len(variable) && entry[:len(variable)+1] == variable+"=" {
			base = entry[len(variable)+1:]
			continue
		}
		env = append(env, entry)
	}
	if base == "" {
		base = "/tmp"
	}
	if !path.IsAbs(base) {
		// Preserve filesystem traversal: cleaning "symlink/../scratch" would
		// select a different directory before the kernel follows the symlink.
		if configuration.Dir != "" {
			base = configuration.Dir + "/" + base
		}
		if !path.IsAbs(base) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			base = cwd + "/" + base
		}
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	err = errors.New("launcher guardian: no name for the temporary directory")
	for _, name := range configuration.Scratch {
		// A name that exists is skipped: the directory must be created here.
		err = parent.Mkdir(name, 0o700)
		if err == nil {
			configuration.Env = append(env, "TMPDIR="+base+"/"+name)
			return func() {
				// Retain the directory handle so a renamed or repointed parent
				// path cannot redirect cleanup into another user's files.
				_ = parent.RemoveAll(name)
				_ = parent.Close()
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	_ = parent.Close()
	return nil, err
}

func preferences(profile string, configuration Command) error {
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(profile)
	if err != nil {
		return err
	}
	defer root.Close()
	name := configuration.Profile
	if name == "" {
		name = "Default"
	}
	if err := root.MkdirAll(name, 0o700); err != nil {
		return err
	}
	return root.WriteFile(path.Join(name, "Preferences"), []byte(configuration.Preferences), 0o600)
}

func managedCleanup(name string) (func(), error) {
	if name == "" {
		return func() {}, nil
	}
	if name == "." || name == ".." || path.Base(name) != name {
		return nil, errors.New("invalid managed profile name")
	}
	parent := os.NewFile(4, "rod-manager-profile-parent")
	defer parent.Close()
	syscall.CloseOnExec(4)
	info, err := parent.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("managed profile parent is not a directory")
	}
	descriptor := "/dev/fd/4"
	if runtime.GOOS == "linux" {
		descriptor = "/proc/self/fd/4"
	}
	root, err := os.OpenRoot(descriptor)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, errors.New("managed profile parent descriptor could not be reopened")
	}
	return func() {
		defer root.Close()
		// Keep deletion anchored to the inherited directory even when its
		// pathname has been renamed or a parent symlink has been replaced.
		_ = root.RemoveAll(name)
	}, nil
}
