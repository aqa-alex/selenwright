# Selenwright
Selenoid fork with native Playwright protocol support.
Based on [aerokube/selenoid](https://github.com/aerokube/selenoid) — Apache 2.0 license. This project adds Playwright protocol support to Selenoid.

## Features

### One-command Installation
Start browser automation in minutes by downloading [Configuration Manager](TBA) binary and running just **one command**:
```
$ ./cm selenwright start --vnc --tmpfs 128
```
**That's it!** You can now use Selenwright instead of Selenium server. Specify the following Selenium URL in tests:
```
http://localhost:4444/wd/hub
```

### Ready to use Browser Images
No need to manually install browsers or dive into WebDriver documentation. Available images:
![Browsers List](docs/img/browsers-list.gif)

New images are added right after official releases. You can create your custom images with browsers. 

### Live Browser Screen and Logs
New showing browser screen and Selenium session logs

### Native Playwright WebSocket Support
Selenoid can proxy native Playwright connections through a dedicated WebSocket endpoint:
```
ws://<host>:4444/playwright/<browser>/<playwright-version>
```
This is native Playwright support, not Selenium Grid compatibility mode. Configure a browser version with `protocol: "playwright"` and point it to a companion image running Playwright server instead of Selenium. Companion Playwright images are external to this repository; see the [Playwright guide](docs/playwright.adoc) for the runtime contract, version matching rules, and example configuration.

### Video Recording
* Any browser session can be saved to [H.264](https://en.wikipedia.org/wiki/H.264/MPEG-4_AVC) video ([example](https://www.youtube.com/watch?v=maB298oO5cI))
* An API to list, download, and delete recorded video files

### Convenient Logging

* Any browser session logs are automatically saved to files - one per session
* An API to list, download, and delete saved log files

### Lightweight and Lightning Fast
Suitable for personal usage and in big clusters:
* Consumes **10 times** less memory than the Java-based Selenium server under the same load
* **Small 6 Mb binary** with no external dependencies (no need to install Java)
* **Browser consumption API** working out of the box
* Ability to send browser logs to **centralized log storage** (e.g. to the [ELK-stack](https://logz.io/learn/complete-guide-elk-stack/))
* Fully **isolated** and **reproducible** environment

## Complete Guide & Build Instructions

Build instructions and additional documentation are available in the [docs](docs) directory.
