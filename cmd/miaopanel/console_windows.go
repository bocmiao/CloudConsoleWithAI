package main

import "golang.org/x/sys/windows"

// cpUTF8 is the Windows code page identifier for UTF-8.
const cpUTF8 = 65001

// setupConsole switches the console to UTF-8 so Chinese output is not
// garbled on systems whose default code page is GBK.
func setupConsole() {
	_ = windows.SetConsoleOutputCP(cpUTF8)
}
