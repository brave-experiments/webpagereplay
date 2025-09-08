// Copyright 2017 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Program httparchive prints information about archives saved by record.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/catapult-project/catapult/web_page_replay_go/src/webpagereplay"
	"github.com/urfave/cli/v2"
)


func main() {
	progName := filepath.Base(os.Args[0])
	cfg := &HttpArchiveConfig{}

	fail := func(c *cli.Context, err error) {
		fmt.Fprintf(os.Stderr, "Error:\n%v.\n\n", err)
		cli.ShowSubcommandHelp(c)
		os.Exit(1)
	}

	checkArgs := func(cmdName string, wantArgs int) func(*cli.Context) error {
		return func(c *cli.Context) error {
			if c.Args().Len() != wantArgs {
				return fmt.Errorf("Expected %d arguments but got %d", wantArgs, c.Args().Len())
			}
			return nil
		}
	}
	loadArchiveOrDie := func(c *cli.Context, arg int) *webpagereplay.Archive {
		archive, err := webpagereplay.OpenArchive(c.Args().Get(arg))
		if err != nil {
			fail(c, err)
		}
		return archive
	}

	app := cli.NewApp()
	app.Commands = []*cli.Command{
		&cli.Command{
			Name:      "ls",
			Usage:     "List the requests in an archive",
			ArgsUsage: "archive",
			Flags:     cfg.DefaultFlags(),
			Before:    checkArgs("ls", 1),
			Action: func(c *cli.Context) error {
				return list(cfg, loadArchiveOrDie(c, 0), false)
			},
		},
		&cli.Command{
			Name:      "cat",
			Usage:     "Dump the requests/responses in an archive",
			ArgsUsage: "archive",
			Flags:     cfg.DefaultFlags(),
			Before:    checkArgs("cat", 1),
			Action: func(c *cli.Context) error {
				return list(cfg, loadArchiveOrDie(c, 0), true)
			},
		},
		&cli.Command{
			Name:      "edit",
			Usage:     "Edit the requests/responses in an archive",
			ArgsUsage: "input_archive output_archive",
			Flags:     cfg.DefaultFlags(),
			Before:    checkArgs("edit", 2),
			Action: func(c *cli.Context) error {
				return edit(cfg, loadArchiveOrDie(c, 0), c.Args().Get(1))
			},
		},
		&cli.Command{
			Name:      "merge",
			Usage:     "Merge the requests/responses of two archives",
			ArgsUsage: "base_archive input_archive output_archive",
			Before:    checkArgs("merge", 3),
			Action: func(c *cli.Context) error {
				return merge(cfg, loadArchiveOrDie(c, 0), loadArchiveOrDie(c, 1), c.Args().Get(2))
			},
		},
		&cli.Command{
			Name:      "add",
			Usage:     "Add a simple GET request from the network to the archive",
			ArgsUsage: "input_archive output_archive [urls...]",
			Flags:     cfg.AddFlags(),
			Before: func(c *cli.Context) error {
				if c.Args().Len() < 3 {
					return fmt.Errorf("Expected at least 3 arguments but got %d", c.Args().Len())
				}
				return nil
			},
			Action: func(c *cli.Context) error {
				return add(cfg, loadArchiveOrDie(c, 0), c.Args().Get(1), c.Args().Tail())
			},
		},
		&cli.Command{
			Name:      "addAll",
			Usage:     "Add a simple GET request from the network to the archive",
			ArgsUsage: "input_archive output_archive urls_file",
			Flags:     cfg.AddFlags(),
			Before:    checkArgs("add", 3),
			Action: func(c *cli.Context) error {
				return addAll(cfg, loadArchiveOrDie(c, 0), c.Args().Get(1), c.Args().Get(2))
			},
		},
		&cli.Command{
			Name:      "trim",
			Usage:     "Trim the requests/responses in an archive",
			ArgsUsage: "input_archive output_archive",
			Flags:     cfg.TrimFlags(),
			Before:    checkArgs("trim", 2),
			Action: func(c *cli.Context) error {
				return trim(cfg, loadArchiveOrDie(c, 0), c.Args().Get(1))
			},
		},
		&cli.Command{
			Name:      "inject",
			Usage:     "Inject a script into the selected responses of an archive",
			ArgsUsage: "input_archive output_archive script",
			Flags:     cfg.RequestFilterFlags(),
			Before:    checkArgs("inject", 3),
			Action: func(c *cli.Context) error {
				return inject(cfg, loadArchiveOrDie(c, 0), c.Args().Get(1), c.Args().Get(2))
			},
		},
	}
	app.Usage = "HTTP Archive Utils"
	app.UsageText = fmt.Sprintf(usage, progName)
	app.HideVersion = true
	app.Version = ""
	app.Writer = os.Stderr
	err := app.Run(os.Args)
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
}
