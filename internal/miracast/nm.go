package miracast

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	nmService           = "org.freedesktop.NetworkManager"
	nmPath              = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmDeviceIface       = nmService + ".Device"
	nmWifiP2PIface      = nmService + ".Device.WifiP2P"
	nmPeerIface         = nmService + ".WifiP2PPeer"
	nmActiveIface       = nmService + ".Connection.Active"
	nmIP4ConfigIface    = nmService + ".IP4Config"
	nmDeviceTypeWifiP2P = 30

	nmActiveActivated   = 2
	nmActiveDeactivated = 4

	// p2pConnectTimeout covers GO negotiation, any on-sink confirmation
	// prompt and DHCP on the new group.
	p2pConnectTimeout = 60 * time.Second

	findProgressInterval = 2 * time.Second
)

// ErrNoP2PDevice means NetworkManager exposes no Wi-Fi P2P device: the Wi-Fi
// card or driver lacks P2P, or NetworkManager runs the iwd backend.
var ErrNoP2PDevice = errors.New("no Wi-Fi P2P device: Miracast needs a P2P-capable Wi-Fi card and NetworkManager with the wpa_supplicant backend")

// Peer is a Wi-Fi Display sink found by a P2P search.
type Peer struct {
	Name      string
	HwAddress string // P2P device address, the connect target
	Strength  uint8  // signal quality in percent
	path      dbus.ObjectPath
}

// P2P drives NetworkManager's Wi-Fi P2P device over the system bus.
type P2P struct {
	conn   *dbus.Conn
	device dbus.ObjectPath
}

// OpenP2P connects to NetworkManager and finds its Wi-Fi P2P device. The
// connection is private: closing it makes NetworkManager tear down any group
// activated through it, even if this process crashes.
func OpenP2P() (*P2P, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect to system bus: %w", err)
	}
	var devices []dbus.ObjectPath
	if err := conn.Object(nmService, nmPath).Call(nmService+".GetDevices", 0).Store(&devices); err != nil {
		conn.Close()
		return nil, fmt.Errorf("NetworkManager GetDevices: %w", err)
	}
	for _, device := range devices {
		kind, err := conn.Object(nmService, device).GetProperty(nmDeviceIface + ".DeviceType")
		if err == nil && kind.Value() == uint32(nmDeviceTypeWifiP2P) {
			return &P2P{conn: conn, device: device}, nil
		}
	}
	conn.Close()
	return nil, ErrNoP2PDevice
}

// Close releases the bus connection and with it any group it activated.
func (p *P2P) Close() error { return p.conn.Close() }

// Find searches for Wi-Fi Display sinks for the given duration. The search
// competes with normal Wi-Fi traffic, so callers keep it short. A non-nil
// progress receives the sinks found so far every findProgressInterval.
func (p *P2P) Find(ctx context.Context, duration time.Duration, progress func([]Peer)) ([]Peer, error) {
	device := p.conn.Object(nmService, p.device)
	timeout := int32(duration / time.Second)
	if timeout < 1 {
		timeout = 1
	}
	options := map[string]dbus.Variant{"timeout": dbus.MakeVariant(timeout)}
	if err := device.CallWithContext(ctx, nmWifiP2PIface+".StartFind", 0, options).Err; err != nil {
		return nil, fmt.Errorf("start P2P find: %w", err)
	}
	deadline := time.After(duration)
	ticker := time.NewTicker(findProgressInterval)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-deadline:
			break wait
		case <-ticker.C:
			if progress == nil {
				continue
			}
			if peers, err := p.Peers(); err == nil {
				progress(peers)
			}
		}
	}
	_ = device.Call(nmWifiP2PIface+".StopFind", 0).Err
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.Peers()
}

// WaitForPeer searches until the sink with device address hwAddress is seen,
// so a following Connect finds its peer object. It leaves the search running;
// connecting ends it.
func (p *P2P) WaitForPeer(ctx context.Context, hwAddress string, timeout time.Duration) error {
	device := p.conn.Object(nmService, p.device)
	options := map[string]dbus.Variant{"timeout": dbus.MakeVariant(int32(timeout / time.Second))}
	if err := device.CallWithContext(ctx, nmWifiP2PIface+".StartFind", 0, options).Err; err != nil {
		return fmt.Errorf("start P2P find: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		peers, err := p.Peers()
		if err != nil {
			return err
		}
		for _, peer := range peers {
			if equalMAC(peer.HwAddress, hwAddress) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			_ = device.Call(nmWifiP2PIface+".StopFind", 0).Err
			return fmt.Errorf("Miracast display %s not found within %s; put it in screen-mirroring mode", hwAddress, timeout)
		case <-ticker.C:
		}
	}
}

// Peers lists the Wi-Fi Display sinks NetworkManager currently knows about.
func (p *P2P) Peers() ([]Peer, error) {
	value, err := p.conn.Object(nmService, p.device).GetProperty(nmWifiP2PIface + ".Peers")
	if err != nil {
		return nil, fmt.Errorf("read P2P peers: %w", err)
	}
	paths, _ := value.Value().([]dbus.ObjectPath)
	var peers []Peer
	for _, path := range paths {
		peer, ok := p.readPeer(path)
		if ok {
			peers = append(peers, peer)
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Strength > peers[j].Strength })
	return peers, nil
}

func (p *P2P) readPeer(path dbus.ObjectPath) (Peer, bool) {
	var props map[string]dbus.Variant
	if err := p.conn.Object(nmService, path).Call("org.freedesktop.DBus.Properties.GetAll", 0, nmPeerIface).Store(&props); err != nil {
		return Peer{}, false
	}
	ies, _ := props["WfdIEs"].Value().([]byte)
	info, err := ParseDeviceInfo(ies)
	if err != nil || !info.IsSink() {
		return Peer{}, false // not a Wi-Fi Display sink
	}
	peer := Peer{path: path}
	peer.Name, _ = props["Name"].Value().(string)
	peer.HwAddress, _ = props["HwAddress"].Value().(string)
	peer.Strength, _ = props["Strength"].Value().(uint8)
	if peer.Name == "" {
		peer.Name = peer.HwAddress
	}
	return peer, true
}

// Group is an activated P2P group with one sink.
type Group struct {
	p2p    *P2P
	active dbus.ObjectPath
	// LocalIP is this host's address on the group; the sink connects to it.
	LocalIP net.IP
}

// Connect forms a P2P group with the sink whose device address is hwAddress.
// The volatile, client-bound profile leaves no saved connection behind.
func (p *P2P) Connect(ctx context.Context, hwAddress string) (*Group, error) {
	peers, err := p.Peers()
	if err != nil {
		return nil, err
	}
	var peerPath dbus.ObjectPath
	for _, peer := range peers {
		if equalMAC(peer.HwAddress, hwAddress) {
			peerPath = peer.path
		}
	}
	if peerPath == "" {
		return nil, fmt.Errorf("Miracast display %s not found; search again with the display in screen-mirroring mode", hwAddress)
	}
	settings := map[string]map[string]dbus.Variant{
		"connection": {
			"id":          dbus.MakeVariant("doubletake Miracast " + hwAddress),
			"type":        dbus.MakeVariant("wifi-p2p"),
			"autoconnect": dbus.MakeVariant(false),
		},
		"wifi-p2p": {
			"peer":    dbus.MakeVariant(hwAddress),
			"wfd-ies": dbus.MakeVariant(SourceIEs(DefaultControlPort)),
		},
		"ipv6": {"method": dbus.MakeVariant("disabled")},
	}
	options := map[string]dbus.Variant{
		"persist":         dbus.MakeVariant("volatile"),
		"bind-activation": dbus.MakeVariant("dbus-client"),
	}
	var connection, active dbus.ObjectPath
	var result map[string]dbus.Variant
	err = p.conn.Object(nmService, nmPath).CallWithContext(ctx, nmService+".AddAndActivateConnection2", 0,
		settings, p.device, peerPath, options).Store(&connection, &active, &result)
	if err != nil {
		return nil, fmt.Errorf("activate Wi-Fi P2P connection: %w", err)
	}
	group := &Group{p2p: p, active: active}
	if err := group.waitActivated(ctx); err != nil {
		group.Close()
		return nil, err
	}
	return group, nil
}

func (g *Group) waitActivated(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p2pConnectTimeout)
	defer cancel()
	obj := g.p2p.conn.Object(nmService, g.active)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := obj.GetProperty(nmActiveIface + ".State")
		if err != nil {
			return fmt.Errorf("Wi-Fi P2P connection failed (rejected on the display, or the group dropped)")
		}
		switch state.Value() {
		case uint32(nmActiveActivated):
			ip, err := g.localIP()
			if err != nil {
				return err
			}
			g.LocalIP = ip
			return nil
		case uint32(nmActiveDeactivated):
			return fmt.Errorf("Wi-Fi P2P connection was deactivated before it came up")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Wi-Fi P2P connection did not come up within %s: %w", p2pConnectTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (g *Group) localIP() (net.IP, error) {
	value, err := g.p2p.conn.Object(nmService, g.active).GetProperty(nmActiveIface + ".Ip4Config")
	if err != nil {
		return nil, fmt.Errorf("read P2P IPv4 config: %w", err)
	}
	path, _ := value.Value().(dbus.ObjectPath)
	data, err := g.p2p.conn.Object(nmService, path).GetProperty(nmIP4ConfigIface + ".AddressData")
	if err != nil {
		return nil, fmt.Errorf("read P2P IPv4 address: %w", err)
	}
	addresses, _ := data.Value().([]map[string]dbus.Variant)
	for _, address := range addresses {
		text, _ := address["address"].Value().(string)
		if ip := net.ParseIP(text).To4(); ip != nil {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("the Wi-Fi P2P group has no IPv4 address")
}

// Down reports whether NetworkManager no longer has the group active.
func (g *Group) Down() bool {
	state, err := g.p2p.conn.Object(nmService, g.active).GetProperty(nmActiveIface + ".State")
	return err != nil || state.Value() != uint32(nmActiveActivated)
}

// Close deactivates the group. NetworkManager removes the volatile profile.
func (g *Group) Close() error {
	err := g.p2p.conn.Object(nmService, nmPath).Call(nmService+".DeactivateConnection", 0, g.active).Err
	if err != nil && !g.Down() {
		return fmt.Errorf("deactivate Wi-Fi P2P connection: %w", err)
	}
	return nil
}

func equalMAC(a, b string) bool {
	x, errA := net.ParseMAC(a)
	y, errB := net.ParseMAC(b)
	return errA == nil && errB == nil && x.String() == y.String()
}
