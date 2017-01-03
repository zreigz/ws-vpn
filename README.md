# ws-vpn
A VPN implementation over websockets. This is the client/server implementation 
of a layer-2 software switch able to route packets over websockets connections.
The ws-vpn is built on top of Linux's tun/tap device.

## Configuration

There are two config files to distinguish between client and server.

To start server execute the following command:

```
ws-vpn --config server.ini
```

client:

```
ws-vpn --config client.ini
```

### Download

You can get updated release from: https://github.com/zreigz/ws-vpn/releases

### Build and Install

Building ws-vpn needs Go 1.1 or higher.

ws-vpn is a go-gettable package:

```
go get github.com/zreigz/ws-vpn
```