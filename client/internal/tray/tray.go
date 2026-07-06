// Package tray provides a cross-platform system tray / menu-bar presence.
//
// It shows in the Windows notification area, the Linux system tray
// (StatusNotifier/AppIndicator, e.g. KDE on SteamOS desktop) and the macOS
// menu bar. The icon is generated at runtime so no binary assets are required.
package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"runtime"

	"fyne.io/systray"
)

// Options configures the tray.
type Options struct {
	Title   string
	Tooltip string
	OnOpen  func()
	OnQuit  func()
}

// Run starts the tray. It blocks until the tray exits and MUST be called on the
// main goroutine.
func Run(opts Options) {
	systray.Run(func() { onReady(opts) }, func() {
		if opts.OnQuit != nil {
			opts.OnQuit()
		}
	})
}

// Quit asks the tray to exit. Safe to call once the tray is running.
func Quit() { systray.Quit() }

func onReady(opts Options) {
	systray.SetIcon(iconBytes())
	systray.SetTooltip(opts.Tooltip)
	// On macOS a title renders as text in the menu bar, aiding discovery.
	if runtime.GOOS == "darwin" {
		systray.SetTitle(opts.Title)
	}

	mOpen := systray.AddMenuItem("Open sdvc", "Open the control panel")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop sdvc and exit")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if opts.OnOpen != nil {
					opts.OnOpen()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// iconBytes returns platform-appropriate icon bytes (ICO on Windows, PNG else).
func iconBytes() []byte {
	pngBytes := makePNG()
	if runtime.GOOS == "windows" {
		return pngToICO(pngBytes)
	}
	return pngBytes
}

func makePNG() []byte {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	accent := color.NRGBA{R: 0x3b, G: 0x82, B: 0xf6, A: 0xff}
	inner := color.NRGBA{R: 0xe6, G: 0xed, B: 0xf3, A: 0xff}
	cx, cy := 15.5, 15.5
	rOuter, rInner := 15.0, 6.5
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d2 := dx*dx + dy*dy
			switch {
			case d2 <= rInner*rInner:
				img.SetNRGBA(x, y, inner)
			case d2 <= rOuter*rOuter:
				img.SetNRGBA(x, y, accent)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// pngToICO wraps a PNG in a single-image ICO container (Vista+ supports PNG).
func pngToICO(pngBytes []byte) []byte {
	var buf bytes.Buffer
	// ICONDIR header.
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // image count
	// ICONDIRENTRY.
	buf.WriteByte(32)                                              // width
	buf.WriteByte(32)                                              // height
	buf.WriteByte(0)                                               // palette colors
	buf.WriteByte(0)                                               // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))             // color planes
	binary.Write(&buf, binary.LittleEndian, uint16(32))            // bits per pixel
	binary.Write(&buf, binary.LittleEndian, uint32(len(pngBytes))) // image size
	binary.Write(&buf, binary.LittleEndian, uint32(22))            // offset (6 + 16)
	buf.Write(pngBytes)
	return buf.Bytes()
}
