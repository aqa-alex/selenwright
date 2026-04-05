package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/aerokube/selenoid/info"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"golang.org/x/net/websocket"
)

const (
	jsonParam = "json"
)

func logs(w http.ResponseWriter, r *http.Request) {
	requestId := serial()
	fileNameOrSessionID := strings.TrimPrefix(r.URL.Path, paths.Logs)
	if logOutputDir != "" && (fileNameOrSessionID == "" || strings.HasSuffix(fileNameOrSessionID, logFileExtension)) {
		if r.Method == http.MethodDelete {
			deleteFileIfExists(requestId, w, r, logOutputDir, paths.Logs, "DELETED_LOG_FILE")
			return
		}
		user, remote := info.RequestInfo(r)
		if _, ok := r.URL.Query()[jsonParam]; ok {
			listFilesAsJson(requestId, w, logOutputDir, "LOG_ERROR")
			return
		}
		log.Printf("[%d] [LOG_LISTING] [%s] [%s]", requestId, user, remote)
		fileServer := http.StripPrefix(paths.Logs, http.FileServer(http.Dir(logOutputDir)))
		fileServer.ServeHTTP(w, r)
		return
	}
	websocket.Handler(streamLogs).ServeHTTP(w, r)
}

func listFilesAsJson(requestId uint64, w http.ResponseWriter, dir string, errStatus string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[%d] [%s] [%s]", requestId, errStatus, fmt.Sprintf("Failed to list directory %s: %v", logOutputDir, err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var ret []string
	for _, f := range files {
		ret = append(ret, f.Name())
	}
	w.Header().Add("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ret)
}

func streamLogs(wsconn *websocket.Conn) {
	defer wsconn.Close()
	requestId := serial()
	sid, _ := splitRequestPath(wsconn.Request().URL.Path)
	sess, ok := sessions.Get(sid)
	if ok && sess.Container != nil {
		log.Printf("[%d] [CONTAINER_LOGS] [%s]", requestId, sess.Container.ID)
		r, err := cli.ContainerLogs(wsconn.Request().Context(), sess.Container.ID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
		})
		if err != nil {
			log.Printf("[%d] [CONTAINER_LOGS_ERROR] [%v]", requestId, err)
			return
		}
		defer r.Close()
		wsconn.PayloadType = websocket.BinaryFrame
		_, _ = stdcopy.StdCopy(wsconn, wsconn, r)
		log.Printf("[%d] [CONTAINER_LOGS_DISCONNECTED] [%s]", requestId, sid)
	} else {
		log.Printf("[%d] [SESSION_NOT_FOUND] [%s]", requestId, sid)
	}
}
