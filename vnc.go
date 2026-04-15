// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"io"
	"log"
	"net"

	"github.com/aqa-alex/selenwright/session"
	"golang.org/x/net/websocket"
)

func vnc(wsconn *websocket.Conn) {
	defer wsconn.Close()
	requestId := serial()
	sid, _ := splitRequestPath(wsconn.Request().URL.Path)
	sess, ok := sessions.Get(sid)
	if !ok {
		log.Printf("[%d] [SESSION_NOT_FOUND] [%s]", requestId, sid)
		return
	}
	vncHostPort := sess.HostPort.VNC
	if vncHostPort == "" {
		log.Printf("[%d] [VNC_NOT_ENABLED] [%s]", requestId, sid)
		return
	}
	log.Printf("[%d] [VNC_ENABLED] [%s]", requestId, sid)
	var d net.Dialer
	conn, err := d.DialContext(wsconn.Request().Context(), "tcp", vncHostPort)
	if err != nil {
		log.Printf("[%d] [VNC_ERROR] [%v]", requestId, err)
		return
	}
	defer conn.Close()
	wsconn.PayloadType = websocket.BinaryFrame
	go func() {
		_, _ = copyWithWatchdog(wsconn, conn, sess)
		_ = wsconn.Close()
		log.Printf("[%d] [VNC_SESSION_CLOSED] [%s]", requestId, sid)
	}()
	_, _ = copyWithWatchdog(conn, wsconn, sess)
	log.Printf("[%d] [VNC_CLIENT_DISCONNECTED] [%s]", requestId, sid)
}

// copyWithWatchdog is io.Copy plus a Touch of the session watchdog after
// every non-empty chunk. Without it a long-running VNC session would
// still count as idle — the watchdog fires, container is killed, user
// sees their screen go black mid-work. Buffer size matches io.Copy's
// internal default (32 KiB) so we don't penalize throughput.
func copyWithWatchdog(dst io.Writer, src io.Reader, sess *session.Session) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			total += int64(nw)
			if sess != nil && sess.Watchdog != nil {
				_ = sess.Watchdog.Touch()
			}
			if werr != nil {
				return total, werr
			}
			if nw < nr {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}
