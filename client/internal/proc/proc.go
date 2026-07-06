// Package proc detects whether given processes are currently running.
//
// It shells out to platform-native tools so it works on Windows, macOS, Linux
// and SteamOS (Arch-based) without external dependencies:
//   - Windows: "tasklist"
//   - Unix (linux/darwin/steamos): "ps"
package proc

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"sdvc/client/internal/hidden"
)

// AnyRunning reports whether any of the named processes is currently running.
//
// Matching is case-insensitive and compares against the process image/base name
// both with and without a trailing ".exe". If names is empty it returns false
// (nothing to gate on).
func AnyRunning(names []string) (bool, error) {
	wanted := normalizeSet(names)
	if len(wanted) == 0 {
		return false, nil
	}

	running, err := listProcessNames()
	if err != nil {
		return false, err
	}
	for _, r := range running {
		if _, ok := wanted[r]; ok {
			return true, nil
		}
	}
	return false, nil
}

// listProcessNames returns the set of normalized running process names.
func listProcessNames() ([]string, error) {
	if runtime.GOOS == "windows" {
		return listWindows()
	}
	return listUnix()
}

func listWindows() ([]string, error) {
	// CSV, no header. First column is the image name, e.g. "game.exe".
	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	hidden.Apply(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		first := line
		if strings.HasPrefix(line, "\"") {
			if end := strings.Index(line[1:], "\""); end >= 0 {
				first = line[1 : 1+end]
			}
		}
		names = append(names, normalize(first))
	}
	return names, sc.Err()
}

func listUnix() ([]string, error) {
	// "comm=" prints just the command name (basename) with no header.
	out, err := exec.Command("ps", "-A", "-o", "comm=").Output()
	if err != nil {
		return nil, err
	}
	var names []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		names = append(names, normalize(filepath.Base(line)))
	}
	return names, sc.Err()
}

func normalizeSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names)*2)
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		set[normalize(n)] = struct{}{}
	}
	return set
}

// normalize lowercases and strips a trailing ".exe" so matches are lenient
// across platforms and user input styles.
func normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".exe")
	return name
}
