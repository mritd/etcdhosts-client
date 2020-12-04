package etcdhosts_client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// ErrInvalidVersionArg is raised when a function expects IPv 4 or 6 but is
// passed a value not 4 or 6.
var ErrInvalidVersionArg = errors.New("version argument must be 4 or 6")
var ErrHostnameNotFound = errors.New("hostname not found")

// HostList is a sortable set of Hostnames. When in a HostList, Hostnames must
// follow some rules:
//
// 	- HostList may contain IPv4 AND IPv6 ("IP version" or "IPv") Hostnames.
// 	- Names are only allowed to overlap if IP version is different.
// 	- Adding a Hostname for an existing name will replace the old one.
//
// The HostList uses a deterministic Sort order designed to make a HostFile
// output look a particular way. Generally you don't need to worry about this
// as Sort will be called automatically before Format. However, the HostList
// may or may not be sorted at any particular time during runtime.
//
// See the docs and implementation in Sort and Add for more details.
type HostList []*Hostname

// NewHostlist initializes a new HostList
func NewHostList() *HostList {
	return &HostList{}
}

// Len returns the number of Hostnames in the list, part of sort.Interface
func (h HostList) Len() int {
	return len(h)
}

// MakeSurrogateIP takes an IP like 127.0.0.1 and munges it to 0.0.0.1 so we can
// sort it more easily. Note that we don't actually want to change the value,
// so we use value copies here (not pointers).
func MakeSurrogateIP(IP net.IP) net.IP {
	if len(IP.String()) > 3 && IP.String()[0:3] == "127" {
		return net.ParseIP("0" + IP.String()[3:])
	}
	return IP
}

// Less determines the sort order of two Hostnames, part of sort.Interface
func (h HostList) Less(A, B int) bool {
	// Sort IPv4 before IPv6
	// A is IPv4 and B is IPv6. A wins!
	if !h[A].IPv6 && h[B].IPv6 {
		return true
	}
	// A is IPv6 but B is IPv4. A loses!
	if h[A].IPv6 && !h[B].IPv6 {
		return false
	}

	// Sort "localhost" at the top
	if h[A].Domain == "localhost" {
		return true
	}
	if h[B].Domain == "localhost" {
		return false
	}

	// Compare the the IP addresses (byte array)
	// We want to push 127. to the top so we're going to mark it zero.
	surrogateA := MakeSurrogateIP(h[A].IP)
	surrogateB := MakeSurrogateIP(h[B].IP)
	if !surrogateA.Equal(surrogateB) {
		for charIndex := range surrogateA {
			// A and B's IPs differ at this index, and A is less. A wins!
			if surrogateA[charIndex] < surrogateB[charIndex] {
				return true
			}
			// A and B's IPs differ at this index, and B is less. A loses!
			if surrogateA[charIndex] > surrogateB[charIndex] {
				return false
			}
		}
		// If we got here then the IPs are the same and we want to continue on
		// to the domain sorting section.
	}

	// Prep for sorting by domain name
	aLength := len(h[A].Domain)
	bLength := len(h[B].Domain)
	max := aLength
	if bLength > max {
		max = bLength
	}

	// Sort domains alphabetically
	// TODO: This works best if domains are lowercased. However, we do not
	// enforce lowercase because of UTF-8 domain names, which may be broken by
	// case folding. There is a way to do this correctly but it's complicated
	// so I'm not going to do it right now.
	for charIndex := 0; charIndex < max; charIndex++ {
		// This index is longer than A, so A is shorter. A wins!
		if charIndex >= aLength {
			return true
		}
		// This index is longer than B, so B is shorter. A loses!
		if charIndex >= bLength {
			return false
		}
		// A and B differ at this index and A is less. A wins!
		if h[A].Domain[charIndex] < h[B].Domain[charIndex] {
			return true
		}
		// A and B differ at this index and B is less. A loses!
		if h[A].Domain[charIndex] > h[B].Domain[charIndex] {
			return false
		}
	}

	// If we got here then A and B are the same -- by definition A is not Less
	// than B so we return false. Technically we shouldn't get here since Add
	// should not allow duplicates, but we'll guard anyway.
	return false
}

// Swap changes the position of two Hostnames, part of sort.Interface
func (h HostList) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

// Sort this list of Hostnames, according to HostList sorting rules:
//
// 	1. localhost comes before other hostnames
// 	2. IPv4 comes before IPv6
// 	3. IPs are sorted in numerical order
// 	4. The remaining hostnames are sorted in lexicographical order
func (h *HostList) Sort() {
	sort.Sort(*h)
}

// Contains returns true if this HostList has the specified Hostname
func (h *HostList) Contains(b *Hostname) bool {
	for _, a := range *h {
		if a.Equal(b) {
			return true
		}
	}
	return false
}

// ContainsDomain returns true if a Hostname in this HostList matches domain
func (h *HostList) ContainsDomain(domain string) bool {
	for _, hostname := range *h {
		if hostname.Domain == domain {
			return true
		}
	}
	return false
}

// ContainsIP returns true if a Hostname in this HostList matches IP
func (h *HostList) ContainsIP(IP net.IP) bool {
	for _, hostname := range *h {
		if hostname.EqualIP(IP) {
			return true
		}
	}
	return false
}

// Add a new Hostname to this HostList. Add uses some merging logic in the
// event it finds duplicated hostnames. In the case of a conflict (incompatible
// entries) the last write wins. In the case of duplicates, duplicates will be
// removed and the remaining entry will be enabled if any of the duplicates was
// enabled.
//
// Both duplicate and conflicts return errors so you are aware of them, but you
// don't necessarily need to do anything about the error.
func (h *HostList) Add(input *Hostname) error {
	newHostname, err := NewHostname(input.Domain, input.IP.String(), input.Enabled)
	if err != nil {
		return err
	}
	for index, found := range *h {
		if found.Equal(newHostname) {
			// If either hostname is enabled we will set the existing one to
			// enabled state. That way if we add a hostname from the end of a
			// hosts file it will take over, and if we later add a disabled one
			// the original one will stick. We still error in this case so the
			// user can see that there is a duplicate.
			(*h)[index].Enabled = found.Enabled || newHostname.Enabled
			return fmt.Errorf("duplicate hostname entry for %s -> %s",
				newHostname.Domain, newHostname.IP)
		} else if found.Domain == newHostname.Domain && found.IPv6 == newHostname.IPv6 {
			(*h)[index] = newHostname
			return fmt.Errorf("conflicting hostname entries for %s -> %s and -> %s",
				newHostname.Domain, newHostname.IP, found.IP)
		}
	}
	*h = append(*h, newHostname)
	return nil
}

// IndexOf will indicate the index of a Hostname in HostList, or -1 if it is
// not found.
func (h *HostList) IndexOf(host *Hostname) int {
	for index, found := range *h {
		if found.Equal(host) {
			return index
		}
	}
	return -1
}

// IndexOfDomainV will indicate the index of a Hostname in HostList that has
// the same domain and IP version, or -1 if it is not found.
//
// This function will panic if IP version is not 4 or 6.
func (h *HostList) IndexOfDomainV(domain string, version int) int {
	if version != 4 && version != 6 {
		panic(ErrInvalidVersionArg)
	}
	for index, hostname := range *h {
		if hostname.Domain == domain && hostname.IPv6 == (version == 6) {
			return index
		}
	}
	return -1
}

// Remove will delete the Hostname at the specified index. If index is out of
// bounds (i.e. -1), Remove silently no-ops. Remove returns the number of items
// removed (0 or 1).
func (h *HostList) Remove(index int) int {
	if index > -1 && index < len(*h) {
		*h = append((*h)[:index], (*h)[index+1:]...)
		return 1
	}
	return 0
}

// RemoveDomain removes both IPv4 and IPv6 Hostname entries matching domain.
// Returns the number of entries removed.
func (h *HostList) RemoveDomain(domain string) int {
	return h.RemoveDomainV(domain, 4) + h.RemoveDomainV(domain, 6)
}

// RemoveDomainV removes a Hostname entry matching the domain and IP version.
func (h *HostList) RemoveDomainV(domain string, version int) int {
	return h.Remove(h.IndexOfDomainV(domain, version))
}

// Enable will change any Hostnames matching name to be enabled.
func (h *HostList) Enable(name string) error {
	for _, hostname := range *h {
		if hostname.Domain == name {
			hostname.Enabled = true
			return nil
		}
	}
	return ErrHostnameNotFound
}

// EnableV will change a Hostname matching domain and IP version to be enabled.
//
// This function will panic if IP version is not 4 or 6.
func (h *HostList) EnableV(domain string, version int) error {
	if version != 4 && version != 6 {
		return ErrInvalidVersionArg
	}
	for _, hostname := range *h {
		if hostname.Domain == domain && hostname.IPv6 == (version == 6) {
			hostname.Enabled = true
			return nil
		}
	}
	return ErrHostnameNotFound
}

// Disable will change any Hostnames matching name to be disabled.
func (h *HostList) Disable(name string) error {
	for _, hostname := range *h {
		if hostname.Domain == name {
			hostname.Enabled = false
			return nil
		}
	}
	return ErrHostnameNotFound
}

// DisableV will change any Hostnames matching domain and IP version to be disabled.
//
// This function will panic if IP version is not 4 or 6.
func (h *HostList) DisableV(domain string, version int) error {
	if version != 4 && version != 6 {
		return ErrInvalidVersionArg
	}
	for _, hostname := range *h {
		if hostname.Domain == domain && hostname.IPv6 == (version == 6) {
			hostname.Enabled = false
			return nil
		}
	}
	return ErrHostnameNotFound
}

// FilterByIP filters the list of hostnames by IP address.
func (h *HostList) FilterByIP(IP net.IP) (hostnames []*Hostname) {
	for _, hostname := range *h {
		if hostname.IP.Equal(IP) {
			hostnames = append(hostnames, hostname)
		}
	}
	return
}

// FilterByDomain filters the list of hostnames by Domain.
func (h *HostList) FilterByDomain(domain string) (hostnames []*Hostname) {
	for _, hostname := range *h {
		if hostname.Domain == domain {
			hostnames = append(hostnames, hostname)
		}
	}
	return
}

// FilterByDomainV filters the list of hostnames by domain and IPv4 or IPv6.
// This should never contain more than one item, but returns a list for
// consistency with other filter functions.
//
// This function will panic if IP version is not 4 or 6.
func (h *HostList) FilterByDomainV(domain string, version int) (hostnames []*Hostname) {
	if version != 4 && version != 6 {
		panic(ErrInvalidVersionArg)
	}
	for _, hostname := range *h {
		if hostname.Domain == domain && hostname.IPv6 == (version == 6) {
			hostnames = append(hostnames, hostname)
		}
	}
	return
}

// GetUniqueIPs extracts an ordered list of unique IPs from the HostList.
// This calls Sort() internally.
func (h *HostList) GetUniqueIPs() []net.IP {
	h.Sort()
	// A map doesn't preserve order so we're going to use the map to check
	// whether we've seen something and use the list to keep track of the
	// order.
	seen := make(map[string]bool)
	var inOrder []net.IP

	for _, hostname := range *h {
		key := (*hostname).IP.String()
		if !seen[key] {
			seen[key] = true
			inOrder = append(inOrder, (*hostname).IP)
		}
	}
	return inOrder
}

// Format takes the current list of Hostnames in this HostFile and turns it
// into a string suitable for use as an /etc/hosts file.
// Sorting uses the following logic:
//
// 1. List is sorted by IP address
// 2. Commented items are sorted displayed
// 3. 127.* appears at the top of the list (so boot resolvers don't break)
// 4. When present, "localhost" will always appear first in the domain list
func (h *HostList) FormatLinux() []byte {
	h.Sort()
	out := bytes.Buffer{}

	// We want to output one line of hostnames per IP, so first we get that
	// list of IPs and iterate.
	for _, IP := range h.GetUniqueIPs() {
		// Technically if an IP has some disabled hostnames we'll show two
		// lines, one starting with a comment (#).
		var enabledIPs []string
		var disabledIPs []string

		// For this IP, get all hostnames that match and iterate over them.
		for _, hostname := range h.FilterByIP(IP) {
			// If it's enabled, put it in the enabled bucket (likewise for
			// disabled hostnames)
			if hostname.Enabled {
				enabledIPs = append(enabledIPs, hostname.Domain)
			} else {
				disabledIPs = append(disabledIPs, hostname.Domain)
			}
		}

		// Finally, if the bucket contains anything, concatenate it all
		// together and append it to the output. Also add a newline.
		if len(enabledIPs) > 0 {
			out.WriteString(fmt.Sprintf("%s %s\n", IP.String(), strings.Join(enabledIPs, " ")))
		}

		if len(disabledIPs) > 0 {
			out.WriteString(fmt.Sprintf("# %s %s\n", IP.String(), strings.Join(disabledIPs, " ")))
		}
	}

	return out.Bytes()
}

func (h HostList) FormatWindows() []byte {
	h.Sort()
	out := bytes.Buffer{}

	for _, hostname := range h {
		out.WriteString(hostname.Format())
		out.WriteString("\n")
	}

	return out.Bytes()
}

func (h *HostList) Format(goos string) []byte {
	switch goos {
	case "windows":
		return h.FormatWindows()
	case "unix":
		return h.FormatLinux()
	default:
		// Theoretically the Windows format might be more compatible but there
		// are a lot of different operating systems, and they're almost all
		// unix-based OSes, so we'll just assume the linux format is OK. For
		// example, FreeBSD, MacOS, and Linux all use the same format and while
		// I haven't checked OpenBSD or NetBSD, I am going to assume they are
		// OK with this format. If not we can add a case above.
		return h.FormatLinux()
	}
}

// Dump exports all entries in the HostList as JSON
func (h *HostList) Dump() ([]byte, error) {
	return json.MarshalIndent(h, "", "  ")
}

// Apply imports all entries from the JSON input to this HostList
func (h *HostList) Apply(jsonbytes []byte) error {
	var hostnames HostList
	err := json.Unmarshal(jsonbytes, &hostnames)
	if err != nil {
		return err
	}

	for _, hostname := range hostnames {
		_ = h.Add(hostname)
	}

	return nil
}

// ParseLine parses an individual line in a HostFile, which may contain one
// (un)commented ip and one or more hostnames. For example
//
//	127.0.0.1 localhost mysite1 mysite2
func ParseLine(line string) (HostList, error) {
	var hostnames HostList

	if len(line) == 0 {
		return hostnames, fmt.Errorf("line is blank")
	}

	// Parse leading # for disabled lines
	enabled := true
	if line[0:1] == "#" {
		enabled = false
		line = strings.TrimSpace(line[1:])
	}

	// Parse other #s for actual comments
	line = strings.Split(line, "#")[0]

	// Replace tabs and multi spaces with single spaces throughout
	line = strings.Replace(line, "\t", " ", -1)
	for strings.Contains(line, "  ") {
		line = strings.Replace(line, "  ", " ", -1)
	}
	line = strings.TrimSpace(line)

	// Break line into words
	words := strings.Split(line, " ")
	for idx, word := range words {
		words[idx] = strings.TrimSpace(word)
	}

	// Separate the first bit (the ip) from the other bits (the domains)
	ip := words[0]
	domains := words[1:]

	// if LooksLikeIPv4(ip) || LooksLikeIPv6(ip) {
	for _, v := range domains {
		hostname, err := NewHostname(v, ip, enabled)
		if err != nil {
			return nil, err
		}
		hostnames = append(hostnames, hostname)
	}
	// }

	return hostnames, nil
}

// MustParseLine is like ParseLine but panics instead of errors.
func MustParseLine(line string) HostList {
	hostList, err := ParseLine(line)
	if err != nil {
		panic(err)
	}
	return hostList
}
