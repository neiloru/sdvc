// Package notify sends cross-platform desktop notifications.
package notify

import "github.com/gen2brain/beeep"

const appName = "sdvc"

// Send shows a desktop notification. Failures are ignored (best-effort).
func Send(title, message string) {
	_ = beeep.Notify(appName+" - "+title, message, "")
}
