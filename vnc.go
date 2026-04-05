package main

import (
	"io"
	"log"
	"net"

	"golang.org/x/net/websocket"
)

func vnc(wsconn *websocket.Conn) {
	defer wsconn.Close()
	requestId := serial()
	sid, _ := splitRequestPath(wsconn.Request().URL.Path)
	sess, ok := sessions.Get(sid)
	if ok {
		vncHostPort := sess.HostPort.VNC
		if vncHostPort != "" {
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
				_, _ = io.Copy(wsconn, conn)
				_ = wsconn.Close()
				log.Printf("[%d] [VNC_SESSION_CLOSED] [%s]", requestId, sid)
			}()
			_, _ = io.Copy(conn, wsconn)
			log.Printf("[%d] [VNC_CLIENT_DISCONNECTED] [%s]", requestId, sid)
		} else {
			log.Printf("[%d] [VNC_NOT_ENABLED] [%s]", requestId, sid)
		}
	} else {
		log.Printf("[%d] [SESSION_NOT_FOUND] [%s]", requestId, sid)
	}
}
