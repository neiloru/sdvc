# sdvc - Save Data Version Control

Cross-platform version control for game save data. A lightweight **server** stores versioned,
hash-verified archives of your saves, and a **client** with a local web UI automatically syncs
your save folders - uploading local changes and downloading newer versions, but only while the
game isn't running.

Runs on **Windows, macOS, Linux and SteamOS** (Steam Deck).

## Repository layout

| Path        | Description                                                                 |
| ----------- | --------------------------------------------------------------------------- |
| [`client/`](client/) | Sync client with local web control panel and system-tray / menu-bar app.    |
| [`server/`](server/) | HTTP server that stores per-user/per-repo versioned zip archives.           |

See [client/README.md](client/README.md) and [server/README.md](server/README.md) for details.

## How it works

- The **server** stores uploaded zip archives per **user** and **repo**. Each upload is verified
  against a client-supplied SHA-256 and creates a new version - archives are never overwritten or
  deleted.
- The **client** watches your configured save folders and, **only while the configured
  game/process is not running**:
  - detects local changes, zips them, and uploads a new version (hash-checked);
  - checks the server and downloads the latest version when it's newer;
  - lets you browse history and restore any older version (downloads are hash-verified).

Restores are extracted into a staging directory and swapped in atomically, so an interrupted
download never corrupts your saves.

## Quick start

Start the server:

```powershell
cd server
go run .
```

The server listens on `:8080` by default (configurable via `SDVC_ADDR`).

In another terminal, start the client:

```powershell
cd client
go run .
```

The client opens a local control panel at `http://127.0.0.1:8477` (localhost only). Add your save
folders, point the client at your server, and configure the process names that should pause syncing.

Set `SDVC_NO_TRAY=1` to run the client headless (no tray).

## Building

Both components are built for **Windows (amd64)**, **Linux (amd64)** and **macOS (arm64)** by the
GitHub Actions workflow in [.github/workflows/build.yml](.github/workflows/build.yml).

The server is pure Go and cross-compiles freely. The client's tray uses native APIs, so **Windows
builds are pure Go** while **macOS and Linux builds require CGO** and are built natively on each
platform. See [client/README.md](client/README.md) for manual build commands.

## Requirements

- Go 1.26+
- For local client builds on Linux: a C toolchain plus `libgtk-3-dev` and
  `libayatana-appindicator3-dev`.
