// Package miracast implements a Wi-Fi Display (Miracast) source: Wi-Fi Direct
// setup through NetworkManager, the WFD RTSP control session and MPEG-TS/RTP
// media delivery.
package miracast

import (
	"encoding/binary"
	"fmt"
)

// DefaultControlPort is the WFD RTSP port a source advertises and listens on.
const DefaultControlPort = 7236

// WFD subelement IDs (Wi-Fi Display Technical Specification, table 27).
const wfdSubelementDeviceInfo = 0

// WFD device types, bits 0-1 of the device information field.
const (
	wfdDeviceSource        = 0
	wfdDevicePrimarySink   = 1
	wfdDeviceSecondarySink = 2
	wfdDeviceDualRole      = 3
)

// wfdSessionAvailable is bits 4-5 = 01: available for a WFD session.
const wfdSessionAvailable = 1 << 4

// sourceThroughputMbps is the advertised maximum throughput. It is a hint for
// the sink's channel planning, not a rate limit.
const sourceThroughputMbps = 200

// SourceIEs returns the WFD subelements NetworkManager publishes for this
// source (setting wifi-p2p.wfd-ies): a device information subelement marking a
// session-available source whose RTSP server listens on controlPort.
func SourceIEs(controlPort uint16) []byte {
	ie := make([]byte, 9)
	ie[0] = wfdSubelementDeviceInfo
	binary.BigEndian.PutUint16(ie[1:3], 6)
	binary.BigEndian.PutUint16(ie[3:5], wfdDeviceSource|wfdSessionAvailable)
	binary.BigEndian.PutUint16(ie[5:7], controlPort)
	binary.BigEndian.PutUint16(ie[7:9], sourceThroughputMbps)
	return ie
}

// DeviceInfo is the decoded WFD device information subelement of a peer.
type DeviceInfo struct {
	DeviceType       int
	SessionAvailable bool
	ControlPort      uint16
}

// IsSink reports whether the peer can render a source's stream.
func (d DeviceInfo) IsSink() bool {
	return d.DeviceType == wfdDevicePrimarySink || d.DeviceType == wfdDeviceDualRole
}

// ParseDeviceInfo extracts the device information subelement from a peer's WFD
// subelements, as NetworkManager exposes them in WifiP2PPeer.WfdIEs.
func ParseDeviceInfo(ies []byte) (DeviceInfo, error) {
	for len(ies) >= 3 {
		id := ies[0]
		length := int(binary.BigEndian.Uint16(ies[1:3]))
		if 3+length > len(ies) {
			return DeviceInfo{}, fmt.Errorf("WFD subelement %d: length %d exceeds %d remaining bytes", id, length, len(ies)-3)
		}
		body := ies[3 : 3+length]
		if id == wfdSubelementDeviceInfo {
			if length < 6 {
				return DeviceInfo{}, fmt.Errorf("WFD device information is %d bytes, want 6", length)
			}
			info := binary.BigEndian.Uint16(body[0:2])
			return DeviceInfo{
				DeviceType:       int(info & 0x3),
				SessionAvailable: info&0x30 == wfdSessionAvailable,
				ControlPort:      binary.BigEndian.Uint16(body[2:4]),
			}, nil
		}
		ies = ies[3+length:]
	}
	return DeviceInfo{}, fmt.Errorf("WFD device information subelement is absent")
}
