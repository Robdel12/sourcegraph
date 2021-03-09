package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/rjeczalik/notify"

	// TODO - deduplicate me
	"github.com/sourcegraph/sourcegraph/dev/sg/root"
)

func run(ctx context.Context, cmds ...Command) error {
	chs := make([]<-chan struct{}, 0, len(cmds))
	monitor := &changeMonitor{}
	for _, cmd := range cmds {
		chs = append(chs, monitor.register(cmd))
	}

	pathChanges, err := watch()
	if err != nil {
		return err
	}
	go monitor.run(pathChanges)

	root, err := root.RepositoryRoot()
	if err != nil {
		return err
	}

	wg := sync.WaitGroup{}
	errs := make(chan error, len(cmds))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i, cmd := range cmds {
		wg.Add(1)

		go func(cmd Command, ch <-chan struct{}) {
			defer wg.Done()

			// TODO - needs to be a proper fanout
			if err := runWatch(ctx, cmd, root, ch); err != nil {
				if err != ctx.Err() {
					errs <- errors.Wrap(err, fmt.Sprintf("%s failed", cmd.Name))
					cancel()
				}
			}
		}(cmd, chs[i])
	}

	wg.Done()
	return <-errs
}

func runWatch(ctx context.Context, cmd Command, root string, reload <-chan struct{}) error {
	for {
		// Build it
		fmt.Printf("Installing %s...\n", cmd.Name)

		c := exec.CommandContext(ctx, "bash", "-c", cmd.Install)
		c.Dir = root
		c.Env = makeEnv(conf.Env, cmd.Env)
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to install %q: %s (output: %s)", cmd.Name, err, out)
		}

		// clear this signal before starting
		select {
		case <-reload:
		default:
		}

		//
		// Run it
		fmt.Printf("Running %s...\n", cmd.Name)

		commandCtx, cancel := context.WithCancel(ctx)
		c = exec.CommandContext(commandCtx, "bash", "-c", cmd.Cmd)
		c.Dir = root
		c.Env = makeEnv(conf.Env, cmd.Env)
		stdout, err := c.StdoutPipe()
		if err != nil {
			return err
		}

		stderr, err := c.StderrPipe()
		if err != nil {
			return err
		}

		wg := &sync.WaitGroup{}

		readIntoBuf := func(prefix string, r io.Reader) {
			defer wg.Done()

			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				fmt.Fprintf(os.Stdout, "%s: %s\n", prefix, scanner.Text())
			}
		}

		wg.Add(2)
		go readIntoBuf("stdout", stdout)
		go readIntoBuf("stderr", stderr)

		if err := c.Start(); err != nil {
			return err
		}

		errs := make(chan error, 1)
		go func() {
			defer close(errs)

			errs <- (func() error {
				wg.Wait()

				if err := c.Wait(); err != nil {
					if exitErr, ok := err.(*exec.ExitError); ok {
						return fmt.Errorf("exited with %d", exitErr.ExitCode())
					}

					return err
				}

				return nil
			})()
		}()

	outer:
		for {
			select {
			case path := <-reload:
				fmt.Printf("Reloading %s...\n", cmd.Name)

				cancel()    // Stop command
				<-errs      // Wait for exit
				break outer // Reinstall

			case err := <-errs:
				// Exited on its own or errored
				return err
			}
		}
	}

	return nil
}

func makeEnv(envs ...map[string]string) []string {
	combined := os.Environ()
	for _, env := range envs {
		for k, v := range env {
			// TODO - should expand env variables that reference others?
			// SRC_REPOS_DIR: $HOME/.sourcegraph/repos is a literal value today
			combined = append(combined, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return combined
}

//
//

type changeMonitor struct {
	subscriptions []subscription
}

type subscription struct {
	cmd Command
	ch  chan struct{}
}

func (m *changeMonitor) run(paths <-chan string) {
	for path := range paths {
		for _, sub := range m.subscriptions {
			m.notify(sub, path)
		}
	}
}

func (m *changeMonitor) notify(sub subscription, path string) {
	found := false
	for _, prefix := range sub.cmd.Watch {
		if strings.HasPrefix(path, prefix) {
			found = true
		}
	}
	if !found {
		return
	}

	select {
	case sub.ch <- struct{}{}:
	default:
	}
}

func (m *changeMonitor) register(cmd Command) <-chan struct{} {
	ch := make(chan struct{}, 1)
	m.subscriptions = append(m.subscriptions, subscription{cmd, ch})
	return ch
}

//
//

var watchIgnorePatterns = []*regexp.Regexp{
	regexp.MustCompile(`_test\.go$`),
	regexp.MustCompile(`^.bin/`),
	regexp.MustCompile(`^.git/`),
	regexp.MustCompile(`^dev/`),
	regexp.MustCompile(`^node_modules/`),
}

func watch() (<-chan string, error) {
	root, err := root.RepositoryRoot()
	if err != nil {
		return nil, err
	}

	paths := make(chan string)
	events := make(chan notify.EventInfo, 1)

	if err := notify.Watch(root+"/...", events, notify.All); err != nil {
		return nil, err
	}

	go func() {
		defer close(events)
		defer notify.Stop(events)

	outer:
		for event := range events {
			path := strings.TrimPrefix(strings.TrimPrefix(event.Path(), root), "/")

			for _, pattern := range watchIgnorePatterns {
				if pattern.MatchString(path) {
					continue outer
				}
			}

			paths <- path
		}
	}()

	return paths, nil
}
