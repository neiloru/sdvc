# SDVC Server

A save-data version control server. It stores uploaded zip archives per **user** and **repo**,
verifies each upload against a client-supplied SHA-256 hash, and serves any historical version or
the latest. Archives are **never overwritten or deleted** - every upload creates a new version.

## Run

```powershell
cd server
go run .
```

Configuration (environment variables):

| Variable                | Default | Description                          |
| ----------------------- | ------- | ------------------------------------ |
| `SDVC_ADDR`             | `:8080` | Listen address                       |
| `SDVC_DATA`             | `data`  | Root directory for stored archives   |
| `SDVC_MAX_UPLOAD_BYTES` | `1 GiB` | Max upload size (`0` = unlimited)    |

## Storage layout

```
data/<user>/<repo>/index.json     metadata for all versions
data/<user>/<repo>/00000001.zip   version 1
data/<user>/<repo>/00000002.zip   version 2
```

## API

### Upload a new version

```
POST /v1/repos/{user}/{repo}
X-Content-Sha256: <sha256-hex>   (or ?hash=<sha256-hex>)
Body: raw zip bytes
```

The server computes the SHA-256 of the body and rejects the upload with `400` if it does not
match. On success it returns `201` with the new version metadata.

### List versions

```
GET /v1/repos/{user}/{repo}/versions
```

### Download a specific version

```
GET /v1/repos/{user}/{repo}/versions/{version}
```

### Download the latest version

```
GET /v1/repos/{user}/{repo}/latest
```

Download responses include `X-Content-Sha256` and `X-Version` headers and support HTTP range requests.

### List a user's repositories

```
GET /v1/repos/{user}
```

### Health check

```
GET /healthz
```

## Example (PowerShell)

```powershell
$file = "save.zip"
$hash = (Get-FileHash $file -Algorithm SHA256).Hash.ToLower()

# Upload
curl.exe -X POST "http://localhost:8080/v1/repos/alice/mygame" `
  -H "X-Content-Sha256: $hash" --data-binary "@$file"

# Download latest
curl.exe -o latest.zip "http://localhost:8080/v1/repos/alice/mygame/latest"
```
