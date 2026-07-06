# sdvc client

Cross-platform save-data version control client with a local web UI.
Runs on **Windows, macOS, Linux and SteamOS** (Steam Deck).

It watches your save folders and, **only while the configured game/process is not running**:

- detects local changes, zips them, and uploads a new version to the server (hash-checked);
- periodically checks the server and downloads the latest version when it's newer;
- lets you browse history and restore any older version (downloads are hash-verified).

Only repositories you define locally are ever checked.

The app lives in the **system tray** (Windows / Linux) and the **macOS menu bar**. Use its
menu to open the control panel or quit. Set `SDVC_NO_TRAY=1` to run headless (e.g. on a server
or in an environment without a tray host).

## Run

```powershell
cd client
go run .
```

The client opens a local control panel at `http://127.0.0.1:8477` (configurable). It binds to
localhost only. On Steam Deck, run it in Desktop Mode and open the URL in a browser.

Configuration is stored at `<user-config-dir>/sdvc/config.json`
(e.g. `%AppData%\sdvc\config.json` on Windows, `~/.config/sdvc/config.json` on Linux/SteamOS).

## Build for all platforms

The tray uses native APIs. **Windows builds are pure Go**; **macOS and Linux builds require
CGO** and must be built on (or cross-toolchained for) that platform:

```powershell
# Windows (pure Go)
$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o sdvc-client.exe .
```

```bash
# Linux / SteamOS (needs a C toolchain + libayatana-appindicator dev headers)
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o sdvc-client-linux .

# macOS (build on a Mac)
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o sdvc-client-mac .
```

To build fully headless (no tray, pure Go on any platform), run with `SDVC_NO_TRAY=1` at runtime.

## How it works

| Setting        | Meaning                                                                       |
| -------------- | ----------------------------------------------------------------------------- |
| Save folder    | Directory containing the game's save data.                                    |
| User / Repo    | Namespace on the server (`/v1/repos/{user}/{repo}`).                           |
| Processes      | Executable/process names. Sync pauses while any of them is running.           |
| Poll interval  | How often the client checks for local changes and server updates.             |

Sync decision each cycle (when no configured process is running):

- **Local content changed** → zip + upload a new version. Server never deletes old ones.
- Otherwise, **server has a newer version** → download latest and replace the folder.

Uploads send the zip's SHA-256; downloads are rejected if the received bytes don't match the
server-reported SHA-256. Restores are extracted into a staging directory and swapped in
atomically, so an interrupted download never corrupts your saves.
