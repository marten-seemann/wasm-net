package wasip1

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"
)

func dialResolverNotSupported(ctx context.Context, network, address string) (net.Conn, error) {
	// The net.Resolver type makes a call to net.DialUDP to determine which
	// resolved addresses are reachable, which does not go through its Dial
	// hook. As a result, it is unusable on GOOS=wasip1 because it fails
	// even when the Dial function is set because WASI preview 1 does not
	// have a mechanism for opening UDP sockets.
	//
	// Instead of having (often indirect) use of the net.Resolver crash, we
	// override the Dial function to error earlier in the resolver lifecycle
	// with an error which is more explicit to the end user.
	return nil, errors.New("net.Resolver not supported on GOOS=wasip1")
}

func init() {
	net.DefaultResolver.Dial = dialResolverNotSupported

	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.DialContext = DialContext
	}
}

func newOpError(op string, addr net.Addr, err error) error {
	return &net.OpError{
		Op:   op,
		Net:  addr.Network(),
		Addr: addr,
		Err:  err,
	}
}

type netAddr struct{ network, address string }

func (na *netAddr) Network() string { return na.address }
func (na *netAddr) String() string  { return na.address }

func family(addr net.Addr) int {
	var ip net.IP
	switch a := addr.(type) {
	case *net.UnixAddr:
		return AF_UNIX
	case *net.TCPAddr:
		ip = a.IP
	case *net.UDPAddr:
		ip = a.IP
	case *net.IPAddr:
		ip = a.IP
	}
	if ip.To4() != nil {
		return AF_INET
	} else if len(ip) == net.IPv6len {
		return AF_INET6
	}
	return AF_INET
}

func socketType(addr net.Addr) (int, error) {
	switch addr.Network() {
	case "tcp", "unix":
		return SOCK_STREAM, nil
	case "udp", "unixgram":
		return SOCK_DGRAM, nil
	default:
		return -1, syscall.EPROTOTYPE
	}
}

func socketAddress(addr net.Addr) (sockaddr, error) {
	var ip net.IP
	var port int
	switch a := addr.(type) {
	case *net.UnixAddr:
		return &sockaddrUnix{name: a.Name}, nil
	case *net.TCPAddr:
		ip, port = a.IP, a.Port
	case *net.UDPAddr:
		ip, port = a.IP, a.Port
	case *net.IPAddr:
		ip = a.IP
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return &sockaddrInet4{addr: ([4]byte)(ipv4), port: port}, nil
	} else if len(ip) == net.IPv6len {
		return &sockaddrInet6{addr: ([16]byte)(ip), port: port}, nil
	} else {
		return nil, &net.AddrError{
			Err:  "unsupported address type",
			Addr: addr.String(),
		}
	}
}

type conn struct {
	net.Conn
	laddr net.Addr
	raddr net.Addr
}

func (c *conn) LocalAddr() net.Addr  { return c.laddr }
func (c *conn) RemoteAddr() net.Addr { return c.raddr }

// In Go 1.21, the net package cannot initialize the local and remote addresses
// of network connections. For this reason, we use this function to retreive the
// addresses and return a wrapped net.Conn with LocalAddr/RemoteAddr implemented.
func makeConn(c net.Conn) (net.Conn, error) {
	syscallConn, ok := c.(syscall.Conn)
	if !ok {
		return c, nil
	}
	rawConn, err := syscallConn.SyscallConn()
	if err != nil {
		c.Close()
		return nil, err
	}
	var laddr net.Addr
	var raddr net.Addr
	rawConnErr := rawConn.Control(func(fd uintptr) {
		var addr sockaddr
		var peer sockaddr
		if addr, err = getsockname(int(fd)); err != nil {
			return
		}
		if peer, err = getpeername(int(fd)); err != nil {
			return
		}
		switch c.(type) {
		case *net.UnixConn:
			laddr = sockaddrToUnixAddr(addr)
			raddr = sockaddrToUnixAddr(peer)
		case *net.UDPConn:
			laddr = sockaddrToUDPAddr(addr)
			raddr = sockaddrToUDPAddr(peer)
		case *net.TCPConn:
			laddr = sockaddrToTCPAddr(addr)
			raddr = sockaddrToTCPAddr(peer)
		}
	})
	if err == nil {
		err = rawConnErr
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return &conn{c, laddr, raddr}, nil
}

func sockaddrToUnixAddr(addr sockaddr) net.Addr {
	switch a := addr.(type) {
	case *sockaddrUnix:
		return &net.UnixAddr{
			Net:  "unix",
			Name: a.name,
		}
	default:
		return nil
	}
}

func sockaddrToTCPAddr(addr sockaddr) net.Addr {
	ip, port := sockaddrIPAndPort(addr)
	return &net.TCPAddr{
		IP:   ip,
		Port: port,
	}
}

func sockaddrToUDPAddr(addr sockaddr) net.Addr {
	ip, port := sockaddrIPAndPort(addr)
	return &net.UDPAddr{
		IP:   ip,
		Port: port,
	}
}

func sockaddrIPAndPort(addr sockaddr) (net.IP, int) {
	switch a := addr.(type) {
	case *sockaddrInet4:
		return net.IP(a.addr[:]), a.port
	case *sockaddrInet6:
		return net.IP(a.addr[:]), a.port
	default:
		return nil, 0
	}
}
