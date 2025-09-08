// Copyright 2025 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
)

const usage = "%s [ls|cat|edit|merge|add|addAll|trim|inject] [options] archive_file [output_file] [url]"

type HttpArchiveConfig struct {
	method, host, fullPath                                           string
	statusCode                                                       int
	decodeResponseBody, skipExisting, overwriteExisting, invertMatch bool
}

func (cfg *HttpArchiveConfig) RequestFilterFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "command",
			Value:       "",
			Usage:       "Only include URLs matching this HTTP method.",
			Destination: &cfg.method,
		},
		&cli.StringFlag{
			Name:        "host",
			Value:       "",
			Usage:       "Only include URLs matching this host.",
			Destination: &cfg.host,
		},
		&cli.StringFlag{
			Name:        "full_path",
			Value:       "",
			Usage:       "Only include URLs matching this full path.",
			Destination: &cfg.fullPath,
		},
		&cli.IntFlag{
			Name:        "status_code",
			Value:       0,
			Usage:       "Only include URLs matching this response status code.",
			Destination: &cfg.statusCode,
		},
	}
}

func (cfg *HttpArchiveConfig) DefaultFlags() []cli.Flag {
	return append([]cli.Flag{
		&cli.BoolFlag{
			Name:        "decode_response_body",
			Usage:       "Decode/encode response body according to Content-Encoding header.",
			Destination: &cfg.decodeResponseBody,
		},
	}, cfg.RequestFilterFlags()...)
}

func (cfg *HttpArchiveConfig) AddFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:        "skip-existing",
			Usage:       "Skip over existing urls in the archive",
			Destination: &cfg.skipExisting,
		},
		&cli.BoolFlag{
			Name:        "overwrite-existing",
			Usage:       "Overwrite existing urls in the archive",
			Destination: &cfg.overwriteExisting,
		},
	}
}

func (cfg *HttpArchiveConfig) TrimFlags() []cli.Flag {
	return append([]cli.Flag{
		&cli.BoolFlag{
			Name:        "invert-match",
			Usage:       "Trim away any urls that DON'T match in the archive",
			Destination: &cfg.invertMatch,
		},
	}, cfg.DefaultFlags()...)
}

func (cfg *HttpArchiveConfig) requestEnabled(req *http.Request, resp *http.Response) bool {
	if cfg.method != "" && strings.ToUpper(cfg.method) != req.Method {
		return false
	}
	if cfg.host != "" && cfg.host != req.Host {
		return false
	}
	if cfg.fullPath != "" && cfg.fullPath != req.URL.Path {
		return false
	}
	if cfg.statusCode != 0 && cfg.statusCode != resp.StatusCode {
		return false
	}
	return true
}

func list(cfg *HttpArchiveConfig, a *Archive, printFull bool) error {
	return a.ForEach(func(req *http.Request, resp *http.Response) error {
		if !cfg.requestEnabled(req, resp) {
			return nil
		}
		if printFull {
			fmt.Fprint(os.Stdout, "----------------------------------------\n")
			req.Write(os.Stdout)
			fmt.Fprint(os.Stdout, "\n")
			err := DecompressResponse(resp)
			if err != nil {
				return fmt.Errorf("Unable to decompress body:\n%v", err)
			}
			resp.Write(os.Stdout)
			fmt.Fprint(os.Stdout, "\n")
		} else {
			fmt.Fprintf(os.Stdout, "%s %s %s %s\n", req.Method, req.Host, req.URL, resp.Status)
		}
		return nil
	})
}

func trim(cfg *HttpArchiveConfig, a *Archive, outfile string) error {
	newA, err := a.Trim(func(req *http.Request, resp *http.Response) (bool, error) {
		// If req matches and invertMatch -> keep match
		// If req doesn't match and !invertMatch -> keep match
		// Otherwise, trim match
		if cfg.requestEnabled(req, resp) == cfg.invertMatch {
			fmt.Printf("Keeping request: host=%s uri=%s\n", req.Host, req.URL.String())
			return false, nil
		} else {
			fmt.Printf("Trimming request: host=%s uri=%s\n", req.Host, req.URL.String())
			return true, nil
		}
	})
	if err != nil {
		return fmt.Errorf("error editing archive:\n%v", err)
	}
	return writeArchive(newA, outfile)
}

func edit(cfg *HttpArchiveConfig, a *Archive, outfile string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		fmt.Printf("Warning: EDITOR not specified, using default.\n")
		editor = "vi"
	}

	marshalForEdit := func(w io.Writer, req *http.Request, resp *http.Response) error {
		// WriteProxy writes absolute URI in the Start line including the
		// scheme and host. It is necessary for unmarshaling later.
		if err := req.WriteProxy(w); err != nil {
			return err
		}
		if cfg.decodeResponseBody {
			if err := DecompressResponse(resp); err != nil {
				return fmt.Errorf("couldn't decompress body: %v", err)
			}
		}
		return resp.Write(w)
	}

	unmarshalAfterEdit := func(r io.Reader) (*http.Request, *http.Response, error) {
		br := bufio.NewReader(r)
		req, err := http.ReadRequest(br)
		if err != nil {
			return nil, nil, fmt.Errorf("couldn't unmarshal request: %v", err)
		}
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			if req.Body != nil {
				req.Body.Close()
			}
			return nil, nil, fmt.Errorf("couldn't unmarshal response: %v", err)
		}
		if cfg.decodeResponseBody {
			// Compress body back according to Content-Encoding
			if err := compressResponse(resp); err != nil {
				return nil, nil, fmt.Errorf("couldn't compress response: %v", err)
			}
		}
		// Read resp.Body into a buffer since the tmpfile is about to be deleted.
		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("couldn't unmarshal response body: %v", err)
		}
		resp.Body = ioutil.NopCloser(bytes.NewReader(body))
		return req, resp, nil
	}

	newA, err := a.Edit(func(req *http.Request, resp *http.Response) (*http.Request, *http.Response, error) {
		if !cfg.requestEnabled(req, resp) {
			return req, resp, nil
		}
		fmt.Printf("Editing request: host=%s uri=%s\n", req.Host, req.URL.String())
		// Serialize the req/resp to a temporary file, let the user edit that file, then
		// de-serialize and return the result. Repeat until de-serialization succeeds.
		for {
			tmpf, err := ioutil.TempFile("", "httparchive_edit_request")
			if err != nil {
				return nil, nil, err
			}
			tmpname := tmpf.Name()
			defer os.Remove(tmpname)
			if err := marshalForEdit(tmpf, req, resp); err != nil {
				tmpf.Close()
				return nil, nil, err
			}
			if err := tmpf.Close(); err != nil {
				return nil, nil, err
			}
			// Edit this file.
			cmd := exec.Command(editor, tmpname)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, nil, fmt.Errorf("Error running %s %s: %v", editor, tmpname, err)
			}
			// Reload.
			tmpf, err = os.Open(tmpname)
			if err != nil {
				return nil, nil, err
			}
			defer tmpf.Close()
			newReq, newResp, err := unmarshalAfterEdit(tmpf)
			if err != nil {
				fmt.Printf("Error in editing request. Try again: %v\n", err)
				continue
			}
			return newReq, newResp, nil
		}
	})
	if err != nil {
		return fmt.Errorf("error editing archive:\n%v", err)
	}

	return writeArchive(newA, outfile)
}

func writeArchive(archive *Archive, outfile string) error {
	outf, err := os.OpenFile(outfile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0660))
	if err != nil {
		return fmt.Errorf("error opening output file %s:\n%v", outfile, err)
	}
	err0 := archive.Serialize(outf)
	err1 := outf.Close()
	if err0 != nil || err1 != nil {
		if err0 == nil {
			err0 = err1
		}
		return fmt.Errorf("error writing edited archive to %s:\n%v", outfile, err0)
	}
	fmt.Printf("Wrote edited archive to %s\n", outfile)
	return nil
}

func merge(cfg *HttpArchiveConfig, archive *Archive, input *Archive, outfile string) error {
	if err := archive.Merge(input); err != nil {
		return fmt.Errorf("Merge archives failed: %v", err)
	}

	return writeArchive(archive, outfile)
}

func addUrl(cfg *HttpArchiveConfig, archive *Archive, urlString string) error {
	addMode := AddModeAppend
	if cfg.skipExisting {
		addMode = AddModeSkipExisting
	} else if cfg.overwriteExisting {
		addMode = AddModeOverwriteExisting
	}
	if err := archive.Add("GET", urlString, addMode); err != nil {
		return fmt.Errorf("Error adding request: %v", err)
	}
	return nil
}

func add(cfg *HttpArchiveConfig, archive *Archive, outfile string, urls []string) error {
	for _, urlString := range urls {
		if err := addUrl(cfg, archive, urlString); err != nil {
			return err
		}
	}
	return writeArchive(archive, outfile)
}

func addAll(cfg *HttpArchiveConfig, archive *Archive, outfile string, inputFilePath string) error {
	f, err := os.OpenFile(inputFilePath, os.O_RDONLY, os.ModePerm)
	if err != nil {
		return fmt.Errorf("open file error: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		urlString := sc.Text() // GET the line string
		if err := addUrl(cfg, archive, urlString); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan file error: %v", err)
	}

	return writeArchive(archive, outfile)
}

func inject(cfg *HttpArchiveConfig, a *Archive, outfile string, scriptFile string) error {
	timeSeedMs := a.DeterministicTimeSeedMs
	// Replace {{WPR_TIME_SEED_TIMESTAMP}} with the time seed.
	replacements := map[string]string{"{{WPR_TIME_SEED_TIMESTAMP}}": strconv.FormatInt(timeSeedMs, 10)}
	si, err := NewScriptInjectorFromFile(scriptFile, replacements)
	if err != nil {
		return fmt.Errorf("Error opening script %s: %v", scriptFile, err)
	}

	err = a.ForEach(func(req *http.Request, resp *http.Response) error {
			if cfg.requestEnabled(req, resp) {
				si.Transform(req, resp)
			}
			a.AddArchivedRequest(req, resp, AddModeOverwriteExisting)
			return nil
	})
	if err != nil {
		return fmt.Errorf("Error editing archive: %v", err)
	}

	return writeArchive(a, outfile)
}

// compressResponse compresses resp.Body in place according to resp's Content-Encoding header.
func compressResponse(resp *http.Response) error {
	ce := strings.ToLower(resp.Header.Get("Content-Encoding"))
	if ce == "" {
		return nil
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()

	body, newCE, err := CompressBody(ce, body)
	if err != nil {
		return err
	}
	if ce != newCE {
		return fmt.Errorf("can't compress body to '%s' recieved Content-Encoding: '%s'", ce, newCE)
	}
	resp.Body = ioutil.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return nil
}
