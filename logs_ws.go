package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/aqa-alex/selenwright/info"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gorilla/websocket"
)

const (
	jsonParam = "json"
)

var logsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return app.originChecker.Check(r)
	},
}

func logs(w http.ResponseWriter, r *http.Request) {
	requestId := serial()
	fileNameOrSessionID := strings.TrimPrefix(r.URL.Path, paths.Logs)
	if app.logOutputDir != "" && (fileNameOrSessionID == "" || strings.HasSuffix(fileNameOrSessionID, logFileExtension)) {
		if r.Method == http.MethodDelete {
			deleteFileIfExists(requestId, w, r, app.logOutputDir, paths.Logs, "DELETED_LOG_FILE")
			return
		}
		user, remote := info.RequestInfo(r)
		if _, ok := r.URL.Query()[jsonParam]; ok {
			items, err := ensureArtifactHistoryManager().ListLogs()
			if err != nil {
				log.Printf("[%d] [LOG_ERROR] [Failed to list directory %s: %v]", requestId, app.logOutputDir, err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeJSONResponse(w, http.StatusOK, items)
			return
		}
		log.Printf("[%d] [LOG_LISTING] [%s] [%s]", requestId, user, remote)
		fileServer := http.StripPrefix(paths.Logs, http.FileServer(http.Dir(app.logOutputDir)))
		fileServer.ServeHTTP(w, r)
		return
	}
	streamLogs(w, r)
}

func streamLogs(w http.ResponseWriter, r *http.Request) {
	requestId := serial()
	wsconn, err := logsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[%d] [LOGS_UPGRADE_FAILED] [%v]", requestId, err)
		return
	}
	applyWSReadLimit(wsconn)
	defer wsconn.Close()

	sid, _ := splitRequestPath(r.URL.Path)
	sess, ok := app.sessions.Get(sid)
	if !ok || sess.Container == nil {
		log.Printf("[%d] [SESSION_NOT_FOUND] [%s]", requestId, sid)
		return
	}

	if sess.Protocol == session.ProtocolPlaywright && sess.LogSink != nil {
		log.Printf("[%d] [PLAYWRIGHT_LOGS] [%s]", requestId, sid)
		streamPlaywrightLogs(wsconn, sess.LogSink, r)
		log.Printf("[%d] [PLAYWRIGHT_LOGS_DISCONNECTED] [%s]", requestId, sid)
		return
	}

	log.Printf("[%d] [CONTAINER_LOGS] [%s]", requestId, sess.Container.ID)
	rc, err := app.cli.ContainerLogs(r.Context(), sess.Container.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		log.Printf("[%d] [CONTAINER_LOGS_ERROR] [%v]", requestId, err)
		return
	}
	defer rc.Close()

	ww := &wsBinaryWriter{conn: wsconn}
	_, _ = stdcopy.StdCopy(ww, ww, rc)
	log.Printf("[%d] [CONTAINER_LOGS_DISCONNECTED] [%s]", requestId, sid)
}

func streamPlaywrightLogs(wsconn *websocket.Conn, sink *session.LogSink, r *http.Request) {
	index := 0
	for {
		lines, nextIndex, closed, notify := sink.ReadFrom(index)
		for _, line := range lines {
			if err := wsconn.WriteMessage(websocket.BinaryMessage, []byte(line)); err != nil {
				return
			}
		}
		index = nextIndex
		if closed {
			return
		}
		select {
		case <-notify:
		case <-r.Context().Done():
			return
		}
	}
}
