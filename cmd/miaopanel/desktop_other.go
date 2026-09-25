//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

func hasWindow() bool { return false }

func hasConsole() bool { return true }

func alert(text string, _ bool) { fmt.Fprintln(os.Stderr, text) }

func singleInstance() (bool, func()) { return true, func() {} }

var errNoWebView = errors.New("no native window on this system")

func runWindow(string, string) error { return errNoWebView }
