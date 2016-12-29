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
	"errors"
	"github.com/songgao/water"
	"net"

	. "github.com/zreigz/ws-vpn/utils"
	"os"
	"os/signal"
	"syscall"
	"net/url"

	"github.com/gorilla/websocket"
	"encoding/json"

	"time"

	"fmt"
	"io"
)

type Client struct {
	// config
	cfg     ClientConfig
	// interface
	iface   *water.Interface
	// ip addr
	ip      net.IP

	toIface chan []byte

	ws      *websocket.Conn

	data    chan *Data

	state   int

	routes  []string
}

var net_gateway, net_nic string

func NewClient(cfg ClientConfig) error {
	var err error

	if cfg.MTU != 0 {
		MTU = cfg.MTU
	}

	client := new(Client)
	client.cfg = cfg

	client.toIface = make(chan []byte, 100)
	client.data = make(chan *Data, 100)
	client.routes = make([]string, 0, 1024)

	go client.cleanUp()

	iface, err := newTun("")
	if err != nil {
		return err
	}
	client.iface = iface

	net_gateway, net_nic, err = getNetGateway()
	logger.Debug("Net Gateway: ", net_gateway, net_nic)
	if err != nil {
		logger.Error("Net gateway error")
		return err
	}
	srvDest := cfg.Server + "/32"
	addRoute(srvDest, net_gateway, net_nic)
	client.routes = append(client.routes, srvDest)

	srvAdr := fmt.Sprintf("%s:%d", cfg.Server, cfg.Port)
	u := url.URL{Scheme: "ws", Host: srvAdr, Path: "/ws"}
	logger.Debug("Connecting to ", u.String())

	ticker := time.NewTicker(time.Second * 4)
	defer ticker.Stop()

	var connection *websocket.Conn

	for ok := true; ok; ok = (connection == nil) {
		select {
		case <-ticker.C:
			connection, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
			if err != nil {
				logger.Info("Dial: ", err)
			} else {
				ticker.Stop()
			}
			break
		}
	}

	client.ws = connection

	defer connection.Close()

	client.state = STATE_INIT

	client.ws.SetReadLimit(maxMessageSize)
	client.ws.SetReadDeadline(time.Now().Add(pongWait));
	client.ws.SetPongHandler(func(string) error {
		client.ws.SetReadDeadline(time.Now().Add(pongWait));
		logger.Debug("Pong received");
		return nil
	})

	go client.writePump()

	// Initialize connection with master
	client.data <- &Data{
		ConnectionState: STATE_CONNECT,
	}

	for {
		messageType, r, err := connection.ReadMessage()
		if err == io.EOF {
			logger.Error("Read error:", err)
			break
		} else if err != nil {
			logger.Error("Read error:", err)
			break
		} else {
			logger.Debug("Read: ", string(r))

			if messageType == websocket.TextMessage {
				var message Data
				if err := json.Unmarshal(r, &message); err != nil {
					client.ws.Close()
					close(client.data)
					logger.Panic(err)
				}
				client.dispatcher(&message)
			}

		}

	}

	return errors.New("Not expected to exit")
}

func (clt *Client) dispatcher(message *Data) {
	logger.Debug("Dispatcher: ", clt.state)
	switch clt.state {
	case STATE_INIT:
		logger.Debug("STATE_INIT")
		if message.ConnectionState == STATE_CONNECT {

			ipStr := string(message.Payload)
			ip, subnet, _ := net.ParseCIDR(ipStr)
			setTunIP(clt.iface, ip, subnet)
			err := redirectGateway(clt.iface.Name(), tun_peer.String())
			if err != nil {
				logger.Error("Redirect gateway error", err.Error())
			}

			clt.state = STATE_CONNECTED
			clt.handleInterface()
		}
	case STATE_CONNECTED:
	if message.ConnectionState == STATE_CONNECTED {
		clt.toIface <- message.Payload
	}

	case STATE_DISCONNECT:


	}
}

func (clt *Client) handleInterface() {
	// network packet to interface
	go func() {
		for {
			hp := <-clt.toIface
			_, err := clt.iface.Write(hp)
			if err != nil {
				logger.Error(err.Error())
				return
			}
			logger.Debug("Write to interface")
		}
	}()

	go func() {
		packet := make([]byte, IFACE_BUFSIZE)
		for {
			plen, err := clt.iface.Read(packet)
			if err != nil {
				logger.Error(err)
				break
			}
			clt.data <- &Data{
				ConnectionState: STATE_CONNECTED,
				Payload: packet[:plen],
			}

		}
	}()
}

func (clt *Client) writePump() {

	ticker := time.NewTicker(pingPeriod)

	defer func() {
		ticker.Stop()
		clt.ws.Close()
	}()

	for {
		select {
		case message, ok := <-clt.data:
			if !ok {
				clt.write(websocket.CloseMessage, &Data{})
				return
			}
			if err := clt.write(websocket.TextMessage, message); err != nil {
				logger.Error("writePump error", err)
			}
		case <-ticker.C:
			if err := clt.ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				logger.Error("Send ping error", err)
			}
		}
	}
}

func (clt *Client) write(mt int, message *Data) error {

	s, err := json.Marshal(message)
	if err != nil {
		logger.Panic(err)
	}
	logger.Debug("Sending data: ", string(s))
	return clt.ws.WriteMessage(mt, s)
}

func (clt *Client) cleanUp() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c
	logger.Info("Cleaning Up")

	if err := clt.ws.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
		logger.Error("Send Close Message error", err)
	}

	delRoute("0.0.0.0/1")
	delRoute("128.0.0.0/1")
	for _, dest := range clt.routes {
		delRoute(dest)
	}

	os.Exit(0)
}