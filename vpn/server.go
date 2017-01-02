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
	"net"

	. "github.com/zreigz/ws-vpn/utils"
	"github.com/songgao/water"

	"net/http"
	"os"
	"os/signal"
	"syscall"
	"fmt"

	"golang.org/x/net/ipv4"
)

type VpnServer struct {
	// config
	cfg        ServerConfig
	// interface
	iface      *water.Interface
	// subnet
	ipnet      *net.IPNet
	// IP Pool
	ippool     *VpnIpPool
	// client peers, key is the mac address, value is a HopPeer record

	// Registered clients
	clients    map[string]*connection

	// Register requests
	register   chan *connection

	// Unregister requests
	unregister chan *connection

	outData  *Data

	inData chan *Data

	toIface chan []byte

}

func NewServer(cfg ServerConfig) error {
	var err error

	if cfg.MTU != 0 {
		MTU = cfg.MTU
	}

	vpnServer := new(VpnServer)

	vpnServer.cfg = cfg

	vpnServer.ippool = new(VpnIpPool)

	iface, err := newTun("")
	if err != nil {
		return err
	}
	vpnServer.iface = iface
	ip, subnet, err := net.ParseCIDR(cfg.VpnAddr)
	err = setTunIP(iface, ip, subnet)
	if err != nil {
		return err
	}
	vpnServer.ipnet = &net.IPNet{ip, subnet.Mask}
	vpnServer.ippool.subnet = subnet

	go vpnServer.cleanUp()

	go vpnServer.run()


	vpnServer.register = make(chan *connection)
	vpnServer.unregister = make(chan *connection)
	vpnServer.clients = make(map[string]*connection)
	vpnServer.inData = make(chan *Data, 100)
	vpnServer.toIface = make(chan []byte, 100)


	vpnServer.handleInterface()

	http.HandleFunc("/ws", vpnServer.serveWs)

	adr := fmt.Sprintf(":%d", vpnServer.cfg.Port)
	err = http.ListenAndServe(adr, nil)
	if err != nil {
		logger.Panic("ListenAndServe: " + err.Error())
	}

	return nil

}

func (srv *VpnServer)serveWs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error(err)
		return
	}

	NewConnection(ws, srv)

}

func (srv *VpnServer) run() {
	for {
		select {
		case c := <-srv.register:
			logger.Info("Connection registered:", c.ipAddress.IP.String())
			srv.clients[c.ipAddress.IP.String()] = c
			break

		case c := <-srv.unregister:
			clientIP := c.ipAddress.IP.String()
			_, ok := srv.clients[clientIP]
			if ok {
				delete(srv.clients, clientIP)
				close(c.data)
				if c.ipAddress != nil {
					srv.ippool.relase(c.ipAddress.IP)
				}
				logger.Info("Connection removed:",c.ipAddress.IP)
				logger.Info("Number active clients:", len(srv.clients))
			}
			break

		}
	}
}

func (srv *VpnServer) handleInterface() {
	// network packet to interface
	go func() {
		for {
			hp := <-srv.toIface
			logger.Debug("Write to interface")
			_, err := srv.iface.Write(hp)
			if err != nil {
				logger.Error(err.Error())
				return
			}

		}
	}()

	go func() {
		packet := make([]byte, IFACE_BUFSIZE)
		for {
			plen, err := srv.iface.Read(packet)
			if err != nil {
				logger.Error(err)
				break
			}
			header, _ := ipv4.ParseHeader(packet[:plen])
			logger.Debug("Sending to remote: ", header)

			clientIP := header.Dst.String()
			client, ok := srv.clients[clientIP]
			if ok {
				logger.Debug("Sending to client: ", client.ipAddress)
				client.data <- &Data{
					ConnectionState: STATE_CONNECTED,
					Payload: packet[:plen],
				}
			} else {
				logger.Error("Client not found ", clientIP)
			}



		}
	}()
}

func (srv *VpnServer) cleanUp() {

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c
	//clearMSS(srv.iface.Name(), true)
	os.Exit(0)
}




