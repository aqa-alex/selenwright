// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"io"
	"log"
	"net"
	"net/http"

	"github.com/aqa-alex/selenwright/session"
	"github.com/gorilla/websocket"
)

var vncUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return app.originChecker.Check(r)
	},
}

func vnc(w http.ResponseWriter, r *http.Request) {
	requestId := serial()
	wsconn, err := vncUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[%d] [VNC_UPGRADE_FAILED] [%v]", requestId, err)
		return
	}
	applyWSReadLimit(wsconn)
	defer wsconn.Close()

	sid, _ := splitRequestPath(r.URL.Path)
	sess, ok := app.sessions.Get(sid)
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
	conn, err := d.DialContext(r.Context(), "tcp", vncHostPort)
	if err != nil {
		log.Printf("[%d] [VNC_ERROR] [%v]", requestId, err)
		return
	}
	defer conn.Close()

	wsWriter := &wsBinaryWriter{conn: wsconn}
	wsReader := &wsMessageReader{conn: wsconn}
	go func() {
		_, _ = copyWithWatchdog(conn, wsReader, sess)
		_ = conn.Close()
		_ = wsconn.Close()
		log.Printf("[%d] [VNC_SESSION_CLOSED] [%s]", requestId, sid)
	}()
	_, _ = copyWithWatchdog(wsWriter, conn, sess)
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

// wsMessageReader adapts a gorilla *websocket.Conn to io.Reader by
// concatenating successive messages into one byte stream. Message
// boundaries are irrelevant for the VNC byte-for-byte tunnel — the
// remote TCP endpoint (x11vnc) just needs bytes in order. Not
// goroutine-safe; callers own the conn for reading.
type wsMessageReader struct {
	conn *websocket.Conn
	cur  io.Reader
}

func (r *wsMessageReader) Read(p []byte) (int, error) {
	for {
		if r.cur != nil {
			n, err := r.cur.Read(p)
			if err == io.EOF {
				r.cur = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		_, next, err := r.conn.NextReader()
		if err != nil {
			return 0, err
		}
		r.cur = next
	}
}

// wsBinaryWriter adapts a gorilla *websocket.Conn to io.Writer by
// emitting each Write as a single binary message. Both VNC (one frame
// per 32 KiB chunk) and container-log streaming (one frame per stdcopy
// multiplex slice) tolerate per-Write framing because the other side
// reassembles the byte stream irrespective of message boundaries.
type wsBinaryWriter struct {
	conn *websocket.Conn
}

func (w *wsBinaryWriter) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
