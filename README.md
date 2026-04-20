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

Selenwright prints a single admin bearer token to stdout on first boot — copy it. Pass it as `Authorization: Bearer <token>` from your tests, or run with `--no-auth` for an open local instance. For multi-user setups create a bcrypt htpasswd file and pass `-htpasswd`; see [Authentication](https://aqa-alex.github.io/selenwright/latest/#_authentication_and_authorization) for minting tokens to team members.

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
const browser = await browserType.connect({
  wsEndpoint: "wss://selenwright.example.com/playwright/chromium/1.44.1",
  headers: { Authorization: `Bearer ${process.env.SELENWRIGHT_TOKEN}` },
});
```

Tokens are minted by an admin (UI → Settings → API Tokens, or `POST /api/admin/tokens`). See [Authentication](https://aqa-alex.github.io/selenwright/latest/#_authentication_and_authorization).

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

Ready-to-use companion Playwright images are published on Docker Hub:

- [`selenwright/playwright-chromium`](https://hub.docker.com/r/selenwright/playwright-chromium)
- [`selenwright/playwright-firefox`](https://hub.docker.com/r/selenwright/playwright-firefox)
- [`selenwright/playwright-webkit`](https://hub.docker.com/r/selenwright/playwright-webkit)

See [Native Playwright Support](https://aqa-alex.github.io/selenwright/latest/#_native_playwright_support) for the full companion image contract and configuration details.

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

See [Browser Discovery](https://aqa-alex.github.io/selenwright/latest/#_browser_discovery) for the API reference.

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

See [Browsers Configuration File](https://aqa-alex.github.io/selenwright/latest/#_browsers_configuration_file) for all per-version fields (`tmpfs`, `volumes`, `env`, `shmSize`, `mem`, `cpu`, etc.).

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

See [Video Recording](https://aqa-alex.github.io/selenwright/latest/#_video_recording).

### Session Logs

Save per-session browser logs to files:

```
enableLog: true
```

Requires `-log-output-dir` flag. Use `-save-all-logs` to capture every session without setting the capability.

```
GET http://host:4444/logs/<filename>.log
```

See [Saving Session Logs](https://aqa-alex.github.io/selenwright/latest/#_saving_session_logs).

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

See [Accessing Browser Developer Tools](https://aqa-alex.github.io/selenwright/latest/#_accessing_browser_developer_tools).

### File Upload / Download

**Upload** works out of the box with Selenium clients that support `LocalFileDetector`. See [Uploading Files To Browser](https://aqa-alex.github.io/selenwright/latest/#_uploading_files_to_browser).

**Download** files from sessions at:

```
GET http://host:4444/download/<session-id>/<filename>
```

See [Downloading Files From Browser](https://aqa-alex.github.io/selenwright/latest/#_downloading_files_from_browser).

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

#### Team/group sharing

By default a non-admin user can only manage their own sessions. To let teammates manage sessions of a shared service account (e.g. `jenkins-bot` running tests from CI) supply a JSON group file and reference it with `-groups-file`:

```json
{
  "qa-payments": ["alice", "bob", "jenkins-bot"],
  "qa-growth":   ["carol"]
}
```

```bash
./selenwright -htpasswd users.htpasswd \
    -admin-users=root \
    -groups-file=groups.json
```

Any member of `qa-payments` can terminate, stream logs, view VNC, etc. of any session created by another member of `qa-payments`. Admin still bypasses all ACL. The file is hot-reloaded on `SIGHUP` alongside the htpasswd file. Group membership is snapshotted onto each session at creation time, so revoking membership does not retroactively change ACL for sessions already running.

### `trusted-proxy` — Behind a Reverse Proxy

When nginx, Envoy, or OAuth2 Proxy handles authentication and passes identity via headers:

```bash
./selenwright \
    -auth-mode=trusted-proxy \
    -user-header=X-Forwarded-User \
    -admin-header=X-Admin \
    -groups-header=X-Groups
```

Groups are read as a comma-separated list from `-groups-header` (default `X-Groups`). Members of the same group share session ACL as described in [Team/group sharing](#teamgroup-sharing). Set `-groups-header=""` to disable group reading entirely.

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

See [Authentication and Authorization](https://aqa-alex.github.io/selenwright/latest/#_authentication_and_authorization).

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

For custom Docker network setups, see [Selenwright with Docker Compose](https://aqa-alex.github.io/selenwright/latest/#_selenwright_with_docker_compose).

## Observability

- **Prometheus metrics** — enable with `-enable-metrics`, served at `/metrics` (queue depth, session counts, duration histogram, auth/caps rejection counters)
- **JSON logging** — enable with `-log-json` for structured one-line JSON output
- **Status API** — `GET /status` returns live usage statistics (total/used/queued slots, per-browser breakdown)

See [Metrics and Observability](https://aqa-alex.github.io/selenwright/latest/#_metrics_and_observability) and [Log Files](https://aqa-alex.github.io/selenwright/latest/#_log_files).

## Advanced Features

- **S3 Upload** — upload videos, logs, and artifacts to S3-compatible storage. See [Uploading Files To S3](https://aqa-alex.github.io/selenwright/latest/#_uploading_files_to_s3).
- **Artifact History** — track and retain session artifacts with automatic cleanup. See [Artifact History](https://aqa-alex.github.io/selenwright/latest/#_artifact_history).
- **Stack Management** — pull and recreate Docker Compose stacks without SSH. See [Docker Compose Stack Management](https://aqa-alex.github.io/selenwright/latest/#_docker_compose_stack_management).
- **Metadata** — save session metadata as JSON. See [Saving Session Metadata](https://aqa-alex.github.io/selenwright/latest/#_saving_session_metadata).

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

Full reference: [Selenwright CLI Flags](https://aqa-alex.github.io/selenwright/latest/#_selenwright_cli_flags).

## Selenwright UI

[Selenwright UI](https://github.com/aqa-alex/selenwright-ui) is a companion web interface for live session monitoring, VNC viewing, artifact browsing, and admin controls (browser discovery, stack management).

## Documentation

Published HTML reference: **https://aqa-alex.github.io/selenwright/** (generated per release from [docs/](docs/), AsciiDoc sources).

## License

Apache 2.0 — see [LICENSE](LICENSE).

This project is a fork of [aerokube/selenoid](https://github.com/aerokube/selenoid).
