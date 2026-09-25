package main

import (
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// The window's message loop must run on the thread that created it.
func init() { runtime.LockOSThread() }

const (
	windowTitle = "Miao Panel"
	windowClass = "webview" // the class go-webview2 registers
	iconID      = 1         // RT_GROUP_ICON #1 in rsrc_windows_*.syso
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const (
	swHide      = 0
	swRestore   = 9
	smCxScreen  = 0
	smCyScreen  = 1
	mbIconError = 0x10
	mbIconInfo  = 0x40
)

func hasWindow() bool { return true }

var procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")

// hasConsole is false for the released GUI build (-H=windowsgui), which
// starts without a console window.
func hasConsole() bool {
	h, _, _ := procGetConsoleWindow.Call()
	return h != 0
}

// alert shows a message box: there is no console to print to.
func alert(text string, isError bool) {
	flags := uint32(mbIconInfo)
	if isError {
		flags = mbIconError
	}
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(windowTitle)
	_, _ = windows.MessageBox(0, t, c, flags)
}

func findWindow() uintptr {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	title, _ := windows.UTF16PtrFromString(windowTitle)
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
	return h
}

// singleInstance returns false when Miao Panel already runs, after
// bringing its window to the front.
func singleInstance() (bool, func()) {
	name, _ := windows.UTF16PtrFromString(`Local\MiaoPanel-single-instance`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if w := findWindow(); w != 0 {
			_, _, _ = procShowWindow.Call(w, swRestore)
			_, _, _ = procSetForegroundWindow.Call(w)
		} else {
			alert("Miao Panel 已经在运行了。", false)
		}
		_ = windows.CloseHandle(h)
		return false, func() {}
	}
	return true, func() { _ = windows.CloseHandle(h) }
}

// scaled converts a size in 96-DPI pixels to the screen's pixels, capped
// to a share of the screen. GetDpiForSystem reports 96 unless the process
// is DPI aware (our manifest makes it so).
func scaled(px uint, metric uintptr, share float64) uint {
	dpi := uint(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
			dpi = uint(d)
		}
	}
	v := px * dpi / 96
	if screen, _, _ := procGetSystemMetrics.Call(metric); screen > 0 && float64(v) > float64(screen)*share {
		v = uint(float64(screen) * share)
	}
	return v
}

// openExternal opens a web address in the system browser.
func openExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("只能打开网页地址")
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String()).Start()
}

// errNoWebView means the WebView2 runtime is missing.
var errNoWebView = errors.New("webview2 runtime not available")

// runWindow shows the UI in a native window and returns when it is closed.
func runWindow(address, dataDir string) error {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(dataDir, "webview"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: windowTitle, IconId: iconID, Center: true,
			Width: scaled(1320, smCxScreen, 0.9), Height: scaled(860, smCyScreen, 0.88),
		},
	})
	if w == nil {
		// go-webview2 shows its window before embedding the browser; hide
		// the empty frame it leaves behind.
		if h := findWindow(); h != 0 {
			_, _, _ = procShowWindow.Call(h, swHide)
		}
		return errNoWebView
	}
	defer w.Destroy()
	w.SetSize(int(scaled(960, smCxScreen, 0.9)), int(scaled(640, smCyScreen, 0.9)), webview2.HintMin)
	_ = w.Bind("miaoOpenExternal", openExternal)
	w.Navigate(address)
	w.Run()
	return nil
}
