package main

import (
	"regexp"

	"github.com/rjeczalik/notify"

	// TODO - deduplicate me
	"github.com/sourcegraph/sourcegraph/dev/sg/root"
)

var watchIgnorePatterns = []*regexp.Regexp{
	regexp.MustCompile(`_test\.go$`),
	regexp.MustCompile(`^dev/`),
	regexp.MustCompile(`^.git/`),
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
			path := event.Path()
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
