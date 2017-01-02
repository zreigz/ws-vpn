/*
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 * Author: Lukasz Zajaczkowski <zreigz@gmail.com>
 *
 */
package vpn

import (
	"time"
	"github.com/gorilla/websocket"
	"encoding/json"
	"io"
	"net"
)

const (
	writeWait = 10 * time.Second
	pongWait = 30 * time.Second
	pingPeriod = (pongWait) / 10
	maxMessageSize = 1024 * 1024
)

type connection struct {
	id        int
	ws        *websocket.Conn
	server    *VpnServer
	data      chan *Data
	state     int
	ipAddress *net.IPNet
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  maxMessageSize,
	WriteBufferSize: maxMessageSize,
}

var maxId int = 0

func NewConnection(ws *websocket.Conn, server *VpnServer) *connection {

	logger.Debug("New connection created")

	if ws == nil {
		panic("ws cannot be nil")
	}

	if server == nil {
		panic("server cannot be nil")
	}

	maxId++
	data := make(chan *Data)

	c := &connection{maxId, ws, server, data, STATE_INIT, nil}
	go c.writePump()
	go c.readPump()

	return c
}

func (c *connection) readPump() {
	defer func() {
		c.server.unregister <- c
		c.ws.Close()
	}()

	c.ws.SetReadLimit(maxMessageSize)
	c.ws.SetPingHandler(func(string) error {
		logger.Debug("Ping received")
		if err := c.ws.WriteControl(websocket.PongMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
			logger.Error("Send ping error", err)
		}
		return nil
	})

	for {
		messageType, r, err := c.ws.ReadMessage()
		if err == io.EOF {
			c.cleanUp()
			break
		} else if err != nil {
			logger.Info(err)
			c.cleanUp()
			break
		} else {

			if messageType == websocket.TextMessage {
				c.dispatcher(r)
			}
		}
	}
}

func (c *connection) writePump() {

	defer func() {

		c.ws.Close()
	}()

	for {
		if c != nil {
			select {
			case message, ok := <-c.data:
			        // Thread can be still active after close connection
				if message != nil {
					logger.Debug("writePump data len: ", len(message.Payload))
					if !ok {
						c.write(websocket.CloseMessage, &Data{})
						return
					}
					if err := c.write(websocket.TextMessage, message); err != nil {
						logger.Error("writePump error", err)
					}
				} else {
					break
				}

			}
		} else {
			break
		}
	}
}

func (c *connection) write(mt int, message *Data) error {

	c.ws.SetWriteDeadline(time.Now().Add(writeWait))

	if message.ConnectionState == STATE_CONNECTED {
		err := c.ws.WriteMessage(mt, message.Payload)
		if err != nil {
			return err
		}
	} else {
		s, err := json.Marshal(message)
		if err != nil {
			logger.Panic(err)
			return err
		}


		err = c.ws.WriteMessage(mt, s)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *connection) dispatcher(p []byte) {
	logger.Debug("Dispatcher: ", c.state)
	switch c.state {
	case STATE_INIT:
		logger.Debug("STATE_INIT")
		var message Data
		if err := json.Unmarshal(p, &message); err != nil {
			logger.Panic(err)
		}
		if message.ConnectionState == STATE_CONNECT {
			d := new(Data)
			d.ConnectionState = STATE_CONNECT
			cltIP, err := c.server.ippool.next()
			if err != nil {
				c.cleanUp()
				logger.Error(err)
			}
			logger.Debug("Next IP from ippool", cltIP)
			c.ipAddress = cltIP
			d.Payload = []byte(cltIP.String())
			c.state = STATE_CONNECTED
			c.server.register <- c
			c.data <- d

		}
	case STATE_CONNECTED:
		logger.Debug("STATE_CONNECTED")
		c.server.toIface <- p
	}
}

func (c *connection) cleanUp() {
	c.server.unregister <- c
	c.ws.Close()
}