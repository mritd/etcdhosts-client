package etcdhosts_client

import (
	"fmt"
	"strings"
)

const DefaultOSX = `
##
# Host Database
#
# localhost is used to configure the loopback interface
# when the system is booting.  Do not change this entry.
##

127.0.0.1       localhost
255.255.255.255 broadcasthost
::1             localhost
fe80::1%lo0     localhost
`

const DefaultLinux = `
127.0.0.1   localhost
127.0.1.1   HOSTNAME

# The following lines are desirable for IPv6 capable hosts
::1     localhost ip6-localhost ip6-loopback
fe00::0 ip6-localnet
ff00::0 ip6-mcastprefix
ff02::1 ip6-allnodes
ff02::2 ip6-allrouters
ff02::3 ip6-allhosts
`

// HostFile represents /etc/hosts (or a similar file, depending on OS), and
// includes a list of Hostnames. HostFile includes
type HostFile struct {
	Hosts HostList
	data  []byte
}

// NewHostFile creates a new HostFile object from the specified file.
func NewHostFile(data []byte) (*HostFile, error) {
	hostFile := &HostFile{HostList{}, data}
	errs := hostFile.Parse()
	if errs != nil {
		return nil, fmt.Errorf("failed to create hostfile: %s", errs)
	}
	hostFile.Hosts.Sort()

	return hostFile, nil
}

// Parse reads
func (h *HostFile) Parse() []error {
	var errs []error
	var line = 1
	for _, v := range strings.Split(string(h.data), "\n") {
		hostnames, _ := ParseLine(v)
		for _, hostname := range hostnames {
			err := h.Hosts.Add(hostname)
			if err != nil {
				errs = append(errs, err)
			}
		}
		line++
	}
	return errs
}

// GetData returns the internal snapshot of the HostFile we read when we loaded
// this HostFile from disk (if we ever did that). This is implemented for
// testing and you probably won't need to use it.
func (h *HostFile) GetData() []byte {
	return h.data
}

// Format takes the current list of Hostnames in this HostFile and turns it
// into a string suitable for use as an /etc/hosts file.
// Sorting uses the following logic:
// 1. List is sorted by IP address
// 2. Commented items are left in place
// 3. 127.* appears at the top of the list (so boot resolvers don't break)
// 4. When present, localhost will always appear first in the domain list
func (h *HostFile) Format(goos string) []byte {
	return h.Hosts.Format(goos)
}
