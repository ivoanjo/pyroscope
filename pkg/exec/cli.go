package exec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/mitchellh/go-ps"
	"github.com/pyroscope-io/pyroscope/pkg/agent"
	"github.com/pyroscope-io/pyroscope/pkg/agent/spy"
	"github.com/pyroscope-io/pyroscope/pkg/agent/upstream/remote"
	"github.com/pyroscope-io/pyroscope/pkg/config"
	"github.com/pyroscope-io/pyroscope/pkg/util/atexit"
	"github.com/sirupsen/logrus"
)

func Cli(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("no arguments passed")
	}

	spyName := cfg.Exec.SpyName
	if spyName == "auto" {
		baseName := path.Base(args[0])
		spyName = spy.ResolveAutoName(baseName)
		if spyName == "" {
			supportedSpies := spy.SupportedExecSpies()
			suggestedCommand := fmt.Sprintf("pyroscope exec -spy-name %s %s", supportedSpies[0], strings.Join(args, " "))
			return fmt.Errorf(
				"could not automatically find a spy for program \"%s\". Pass spy name via %s argument, for example: \n  %s\n\nAvailable spies are: %s\n%s\nIf you believe this is a mistake, please submit an issue at %s",
				baseName,
				color.YellowString("-spy-name"),
				color.YellowString(suggestedCommand),
				strings.Join(supportedSpies, ","),
				armMessage(),
				color.BlueString("https://github.com/pyroscope-io/pyroscope/issues"),
			)
		}
	}

	logrus.Info("to disable logging from pyroscope, pass " + color.YellowString("-no-logging") + " argument to pyroscope exec")

	if err := performChecks(spyName); err != nil {
		return err
	}

	signal.Ignore(syscall.SIGCHLD)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	cmd.SysProcAttr.Setpgid = true
	err := cmd.Start()
	if err != nil {
		return err
	}
	u := remote.New(remote.RemoteConfig{
		UpstreamAddress:        cfg.Exec.ServerAddress,
		UpstreamThreads:        cfg.Exec.UpstreamThreads,
		UpstreamRequestTimeout: cfg.Exec.UpstreamRequestTimeout,
	})
	defer u.Stop()

	// TODO: improve this logic, basically we need a smart way of detecting that an app successfully loaded.
	//   Maybe do this on some ticker (every 100 ms) with a timeout (20 s). Make this configurable too
	time.Sleep(5 * time.Second)
	// TODO: add sample rate, make it configurable
	sess := agent.NewSession(u, cfg.Exec.ApplicationName, spyName, 100, cmd.Process.Pid, cfg.Exec.DetectSubprocesses)
	sess.Start()
	defer sess.Stop()

	waitForProcessToExit(cmd)
	return nil
}

// TODO: very hacky, at some point we'll need to make `cmd.Wait()` work
//   Currently the issue is that on Linux it often thinks the process exited when it did not.
func waitForProcessToExit(cmd *exec.Cmd) {
	sigc := make(chan struct{})

	atexit.Register(func() {
		sigc <- struct{}{}
	})

	t := time.NewTicker(time.Second)
	for {
		select {
		case <-sigc:
			cmd.Process.Kill()
			return
		case <-t.C:
			p, err := ps.FindProcess(cmd.Process.Pid)
			if p == nil || err != nil {
				return
			}
		}
	}
}

func performChecks(spyName string) error {
	if spyName == "gospy" {
		return fmt.Errorf("gospy can not profile other processes. See our documentation on using gospy: %s", color.BlueString("https://pyroscope.io/docs/"))
	}

	if runtime.GOOS == "darwin" {
		if !isRoot() {
			logrus.Error("on macOS you're required to run the agent with sudo")
		}
	}

	if !stringsContains(spy.SupportedSpies, spyName) {
		supportedSpies := spy.SupportedExecSpies()
		return fmt.Errorf(
			"Spy \"%s\" is not supported. Available spies are: %s\n%s",
			color.BlueString(spyName),
			strings.Join(supportedSpies, ","),
			armMessage(),
		)
	}

	return nil
}

func stringsContains(arr []string, element string) bool {
	for _, v := range arr {
		if v == element {
			return true
		}
	}
	return false
}

func isRoot() bool {
	u, err := user.Current()
	return err == nil && u.Username == "root"
}

func armMessage() string {
	if runtime.GOARCH == "arm64" {
		return "Note that rbspy is not available on arm64 platform"
	}
	return ""
}
