// SPDX-License-Identifier: GPL-3.0-or-later

package netutil

import (
	"fmt"
	"net"
)

// InterfaceInfo holds the resolved network interface details needed for
// Pro DJ Link communication.
type InterfaceInfo struct {
	Interface *net.Interface
	IP        net.IP
	Broadcast net.IP
	MAC       net.HardwareAddr
}

// ResolveInterface looks up the named network interface and extracts its
// IPv4 address, MAC address, and broadcast address.
func ResolveInterface(name string) (*InterfaceInfo, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("interface %q: %w", name, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("interface %q addrs: %w", name, err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}

		broadcast := make(net.IP, 4)
		mask := ipNet.Mask
		for i := 0; i < 4; i++ {
			broadcast[i] = ip4[i] | ^mask[i]
		}

		return &InterfaceInfo{
			Interface: iface,
			IP:        ip4,
			Broadcast: broadcast,
			MAC:       iface.HardwareAddr,
		}, nil
	}

	return nil, fmt.Errorf("interface %q has no IPv4 address", name)
}
