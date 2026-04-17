# Selenwright

Selenoid fork with native Playwright WebSocket support.
Based on [aerokube/selenoid](https://github.com/aerokube/selenoid) — Apache 2.0 license.

- **Dual protocol** — Selenium WebDriver (HTTP) and Playwright (WebSocket) in a single server
- **Lightweight** — ~6 MB static binary, 10x less memory than Java-based Selenium Grid
- **Docker-managed browsers** — isolated containers per session, no manual driver setup
- **Video, VNC, logs** — record sessions, watch live, capture logs automatically
- **Authentication** — htpasswd, trusted-proxy, or no-auth modes with role-based access

## Quick Start

1. Install [Docker](https://www.docker.com/).
2. Download the Selenwright binary from [releases](https://github.com/aqa-alex/selenwright/releases/latest).
3. Make it executable and run:

```bash
chmod +x selenwright
./selenwright
```

4. Point your tests at the Selenium endpoint:

```
http://localhost:4444/wd/hub
```

5. Check server status:

```
http://localhost:4444/status
```

6. Optionally start [Selenwright UI](https://github.com/aqa-alex/selenwright-ui) at `http://localhost:8080` to watch live sessions.

### Docker

```bash
docker run -d --name selenwright                  \
    -p 4444:4444                                  \
    -v /var/run/docker.sock:/var/run/docker.sock  \
    selenwright/hub:latest-release
```

## Playwright Support

Selenwright proxies native Playwright WebSocket connections through a dedicated endpoint:

```
ws://<host>:4444/playwright/<browser>/<playwright-version>
```

Connect from a Playwright client:

```javascript
const browser = await browserType.connect(
  "ws://selenwright.example.com:4444/playwright/chromium/1.44.1"
);
```

Or via environment variable for your test launcher:

```bash
PW_TEST_CONNECT_WS_ENDPOINT=ws://selenwright.example.com:4444/playwright/chromium/1.44.1
```

Query parameters for Playwright sessions: `enableVNC`, `name`, `screenResolution`.

```
ws://host:4444/playwright/chromium/1.44.1?enableVNC=true&name=myTest
```

Playwright client and server versions must match on **major.minor** (e.g. client `1.44.x` connects to image built for `1.44.x`).

File uploads (`page.setInputFiles()`) and downloads (`page.download()`) work natively through the Playwright protocol — no additional configuration needed.

See [docs/playwright.adoc](docs/playwright.adoc) for the full companion image contract and configuration details.

## Selenium WebDriver

Standard Selenium WebDriver endpoint:

```
http://localhost:4444/wd/hub
```

Custom capabilities are passed via the `selenoid:options` extension key:

```json
{
  "browserName": "chrome",
  "selenoid:options": {
    "enableVNC": true,
    "enableVideo": true,
    "screenResolution": "1280x1024x24"
  }
}
```

## Browser Configuration

### Browser Discovery (recommended)

Selenwright auto-discovers browser images on the Docker host by scanning image labels. Manage the catalog from the UI or API — no manual file editing required.

1. Pull browser images with the appropriate labels.
2. Open Selenwright UI — discovered images appear in the admin panel.
3. **Adopt** an image to add it to the live catalog, or **Dismiss** to hide it.

Adoption state is persisted in `-state-dir` (default `state/`). Send `SIGHUP` or call `POST /browsers/rescan` to trigger a manual rescan.

See [docs/browser-discovery.adoc](docs/browser-discovery.adoc) for the API reference.

### Manual `browsers.json` (legacy)

For environments where discovery is not practical, create a `browsers.json` and pass it with `-conf`:

```json
{
  "chrome": {
    "default": "126.0",
    "versions": {
      "126.0": {
        "image": "selenoid/chrome:126.0",
        "port": "4444",
        "path": "/"
      }
    }
  },
  "chromium": {
    "default": "1.44.1",
    "versions": {
      "1.44.1": {
        "image": "example/playwright-chromium:1.44.1",
        "port": "3000",
        "path": "/",
        "protocol": "playwright"
      }
    }
  }
}
```

Set `"protocol": "playwright"` for Playwright images. Version matching is prefix-based (e.g. `"126"` matches `"126.0"`).

See [docs/browsers-configuration-file.adoc](docs/browsers-configuration-file.adoc) for all per-version fields (`tmpfs`, `volumes`, `env`, `shmSize`, `mem`, `cpu`, etc.).

## Features

### Video Recording

Enable per-session recording via capabilities:

```
enableVideo: true
videoName: "my-test.mp4"
```

Requires the video recorder image (`selenwright-video-recorder`) and `-video-output-dir` flag. Access recordings via the API:

```
GET    http://host:4444/video/<filename>.mp4
DELETE http://host:4444/video/<filename>.mp4
```

See [docs/video.adoc](docs/video.adoc).

### Session Logs

Save per-session browser logs to files:

```
enableLog: true
```

Requires `-log-output-dir` flag. Use `-save-all-logs` to capture every session without setting the capability.

```
GET http://host:4444/logs/<filename>.log
```

See [docs/logs.adoc](docs/logs.adoc).

### VNC Live View

Watch browser sessions in real time through the Selenwright UI:

```
enableVNC: true
```

Set `-default-enable-vnc` to enable VNC for all sessions by default. VNC is proxied as a WebSocket at `http://host:4444/vnc/<session-id>`.

### Chrome DevTools

Proxy for Chrome DevTools Protocol (Chrome 63+):

```
GET http://host:4444/devtools/<session-id>/browser
GET http://host:4444/devtools/<session-id>/page
```

See [docs/devtools.adoc](docs/devtools.adoc).

### File Upload / Download

**Upload** works out of the box with Selenium clients that support `LocalFileDetector`. See [docs/file-upload.adoc](docs/file-upload.adoc).

**Download** files from sessions at:

```
GET http://host:4444/download/<session-id>/<filename>
```

See [docs/file-download.adoc](docs/file-download.adoc).

### Clipboard

Read and update the browser clipboard during active sessions:

```
GET  http://host:4444/clipboard/<session-id>
POST http://host:4444/clipboard/<session-id>
```

## Special Capabilities

<details>
<summary>Full capabilities reference</summary>

| Capability | Type | Example | Description |
|---|---|---|---|
| `enableVNC` | bool | `true` | Show live browser screen |
| `screenResolution` | string | `"1280x1024x24"` | Custom screen resolution |
| `enableVideo` | bool | `true` | Record session video |
| `videoName` | string | `"test.mp4"` | Custom video file name |
| `videoScreenSize` | string | `"1024x768"` | Override video resolution |
| `videoFrameRate` | int | `24` | Frames per second |
| `videoCodec` | string | `"mpeg4"` | FFmpeg video codec |
| `enableLog` | bool | `true` | Save session logs |
| `logName` | string | `"test.log"` | Custom log file name |
| `name` | string | `"myTest"` | Test name (shown in UI) |
| `sessionTimeout` | string | `"30m"` | Per-session idle timeout |
| `timeZone` | string | `"Europe/Berlin"` | Container timezone |
| `containerHostname` | string | `"my-host"` | Override container hostname |
| `env` | array | `["LANG=en_US.UTF-8"]` | Environment variables (admin-only under strict policy) |
| `hostsEntries` | array | `["example.com:1.2.3.4"]` | Custom /etc/hosts entries (admin-only) |
| `dnsServers` | array | `["8.8.8.8"]` | Custom DNS servers (admin-only) |
| `additionalNetworks` | array | `["my-net"]` | Extra Docker networks (admin-only) |
| `applicationContainers` | array | `["app:alias"]` | Link to other containers (admin-only) |
| `labels` | map | `{"env": "staging"}` | Container metadata labels |
| `s3KeyPattern` | string | `"$quota/$fileName"` | Override S3 key pattern |

Pass via `selenoid:options` for W3C protocol:

```json
{"selenoid:options": {"enableVNC": true, "sessionTimeout": "5m"}}
```

</details>

## Authentication

Controlled by `-auth-mode` (default: `embedded`).

### `embedded` — Built-in BasicAuth (default)

Create an htpasswd file with bcrypt passwords:

```bash
docker run --rm httpd:alpine htpasswd -nbB alice MyPassword123 >> users.htpasswd
docker run --rm httpd:alpine htpasswd -nbB bob AnotherPass456 >> users.htpasswd
```

Start Selenwright with the password file:

```bash
./selenwright -htpasswd users.htpasswd -admin-users=alice
```

Or as a Docker container:

```bash
docker run -d --name selenwright                  \
    -p 4444:4444                                  \
    -v /var/run/docker.sock:/var/run/docker.sock  \
    -v $(pwd)/users.htpasswd:/etc/selenwright/users.htpasswd:ro \
    selenwright/hub:latest-release            \
    -htpasswd /etc/selenwright/users.htpasswd -admin-users=alice
```

Test with:

```bash
curl -u alice:MyPassword123 http://localhost:4444/status
```

Edit the htpasswd file and send `SIGHUP` to reload without restart:

```bash
docker kill -s HUP selenwright
```

### `trusted-proxy` — Behind a Reverse Proxy

When nginx, Envoy, or OAuth2 Proxy handles authentication and passes identity via headers:

```bash
./selenwright \
    -auth-mode=trusted-proxy \
    -user-header=X-Forwarded-User \
    -admin-header=X-Admin
```

**Important:** without source trust validation, any client can forge these headers. Add at least one check:

```bash
./selenwright \
    -auth-mode=trusted-proxy \
    -user-header=X-Forwarded-User \
    -admin-header=X-Admin \
    -trusted-proxy-secret=s3cret \
    -trusted-proxy-cidr=10.0.0.0/8
```

| Flag | What it checks |
|---|---|
| `-trusted-proxy-secret=mysecret` | Request must have `X-Router-Secret: mysecret` header |
| `-trusted-proxy-cidr=10.0.0.0/8` | Source IP must be in the CIDR range |
| `-trusted-proxy-mtls-ca=/path/to/ca.pem` | Client cert must be signed by this CA |

When multiple checks are configured, **all** must pass.

### `none` — No Authentication

```bash
./selenwright -auth-mode=none -listen=127.0.0.1:4444
```

Also accepted on any network interface:

```bash
./selenwright -auth-mode=none -listen=:4444
```

**Warning:** without authentication, any client that can reach the listen address can create sessions and read any session's data. You own network-level protection (firewall, overlay network, bastion, reverse proxy).

### Capability Policy

`-caps-policy` (default: `strict`) restricts dangerous capabilities (`env`, `dnsServers`, `hostsEntries`, `additionalNetworks`, `applicationContainers`) to admin users only. Set `-caps-policy=permissive` for legacy behavior.

See [docs/authentication.adoc](docs/authentication.adoc).

## Docker Compose

Create directories for artifacts:

```bash
mkdir -p /data/selenwright/video /data/selenwright/logs
```

```yaml
version: '3'
services:
  selenwright:
    network_mode: bridge
    image: selenwright/hub:latest-release
    volumes:
      - "/var/run/docker.sock:/var/run/docker.sock"
      - "/data/selenwright/video:/opt/selenwright/video"
      - "/data/selenwright/logs:/opt/selenwright/logs"
    environment:
      - OVERRIDE_VIDEO_OUTPUT_DIR=/data/selenwright/video
    command: ["-video-output-dir", "/opt/selenwright/video", "-log-output-dir", "/opt/selenwright/logs"]
    ports:
      - "4444:4444"
```

For custom Docker network setups, see [docs/docker-compose.adoc](docs/docker-compose.adoc).

## Observability

- **Prometheus metrics** — enable with `-enable-metrics`, served at `/metrics` (queue depth, session counts, duration histogram, auth/caps rejection counters)
- **JSON logging** — enable with `-log-json` for structured one-line JSON output
- **Status API** — `GET /status` returns live usage statistics (total/used/queued slots, per-browser breakdown)

See [docs/metrics.adoc](docs/metrics.adoc) and [docs/log-files.adoc](docs/log-files.adoc).

## Advanced Features

- **S3 Upload** — upload videos, logs, and artifacts to S3-compatible storage. See [docs/s3.adoc](docs/s3.adoc).
- **Artifact History** — track and retain session artifacts with automatic cleanup. See [docs/artifact-history.adoc](docs/artifact-history.adoc).
- **Stack Management** — pull and recreate Docker Compose stacks without SSH. See [docs/stack-management.adoc](docs/stack-management.adoc).
- **Metadata** — save session metadata as JSON. See [docs/metadata.adoc](docs/metadata.adoc).

## CLI Flags

<details>
<summary>Key flags reference</summary>

**Server & Network**

| Flag | Default | Description |
|---|---|---|
| `-listen` | `:4444` | Network address to accept connections |
| `-allowed-origins` | (empty) | Allowed Origin values for WebSocket upgrades |

**Session Management**

| Flag | Default | Description |
|---|---|---|
| `-limit` | `5` | Max simultaneous browser sessions |
| `-timeout` | `1m` | Session idle timeout |
| `-max-timeout` | `1h` | Maximum valid session timeout |
| `-session-attempt-timeout` | `30s` | New session attempt timeout |
| `-retry-count` | `1` | New session retry count |
| `-graceful-period` | `5m` | Graceful shutdown period |
| `-disable-queue` | `false` | Disable wait queue |

**Browser Configuration**

| Flag | Default | Description |
|---|---|---|
| `-conf` | `config/browsers.json` | Browser catalog file (legacy) |
| `-state-dir` | `state` | Directory for persistent state (adopted browsers) |
| `-default-enable-vnc` | `false` | Default VNC on for all sessions |

**Video & Logs**

| Flag | Default | Description |
|---|---|---|
| `-video-output-dir` | `video` | Directory to save recorded video |
| `-video-recorder-image` | `selenwright-video-recorder:latest` | Video recorder Docker image |
| `-log-output-dir` | (empty) | Directory to save session logs |
| `-save-all-logs` | `false` | Save all logs regardless of capabilities |
| `-log-json` | `false` | Emit structured JSON logs |

**Container Runtime**

| Flag | Default | Description |
|---|---|---|
| `-disable-docker` | `false` | Driver-only mode (no Docker) |
| `-container-network` | `default` | Docker network for containers |
| `-browser-network` | `selenwright-browsers` | Isolated internal network for browsers |
| `-mem` | (none) | Container memory limit (e.g. `1g`) |
| `-cpu` | (none) | Container CPU limit (e.g. `1.0`) |

**Metrics**

| Flag | Default | Description |
|---|---|---|
| `-enable-metrics` | `false` | Enable Prometheus `/metrics` endpoint |
| `-event-workers` | `16` | Worker goroutines for lifecycle events |

</details>

Full reference: [docs/cli-flags.adoc](docs/cli-flags.adoc).

## Selenwright UI

[Selenwright UI](https://github.com/aqa-alex/selenwright-ui) is a companion web interface for live session monitoring, VNC viewing, artifact browsing, and admin controls (browser discovery, stack management).

## Documentation

Full reference documentation is available in the [docs/](docs/) directory (AsciiDoc format).

## License

Apache 2.0 — see [LICENSE](LICENSE).

This project is a fork of [aerokube/selenoid](https://github.com/aerokube/selenoid).
