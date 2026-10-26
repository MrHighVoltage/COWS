//go:build linux

package fileagent

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type idMapEntry struct {
	containerID int64
	hostID      int64
	size        int64
}

func (entry idMapEntry) contains(id int64) bool {
	return id >= entry.containerID && id-entry.containerID < entry.size
}

// verifyNamespaceIdentity compares the adapter's expected host identity with
// the user namespace podman unshare actually entered. It catches a namespace
// whose mapping differs from the Podman service mapping, which would otherwise
// run the helper as a different host account without any visible error.
func verifyNamespaceIdentity(value options) error {
	if value.ExpectedHostUID < 0 && value.ExpectedHostGID < 0 {
		return nil
	}
	if value.ExpectedHostUID >= 0 {
		hostID, err := namespaceHostID("/proc/self/uid_map", value.UID)
		if err != nil {
			return err
		}
		if hostID != value.ExpectedHostUID {
			return fmt.Errorf("namespace UID %d maps to host UID %d, want %d", value.UID, hostID, value.ExpectedHostUID)
		}
	}
	if value.ExpectedHostGID >= 0 {
		hostID, err := namespaceHostID("/proc/self/gid_map", value.GID)
		if err != nil {
			return err
		}
		if hostID != value.ExpectedHostGID {
			return fmt.Errorf("namespace GID %d maps to host GID %d, want %d", value.GID, hostID, value.ExpectedHostGID)
		}
	}
	return nil
}

func namespaceHostID(procPath string, id int64) (int64, error) {
	entries, err := readIDMap(procPath)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if entry.contains(id) {
			return entry.hostID + id - entry.containerID, nil
		}
	}
	return 0, fmt.Errorf("namespace identity %d is not mapped", id)
}

func readIDMap(procPath string) ([]idMapEntry, error) {
	file, err := os.Open(procPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := make([]idMapEntry, 0, 2)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid identity map line %q", scanner.Text())
		}
		values := make([]int64, 3)
		for index, field := range fields {
			value, err := strconv.ParseInt(field, 10, 64)
			if err != nil || value < 0 {
				return nil, fmt.Errorf("invalid identity map line %q", scanner.Text())
			}
			values[index] = value
		}
		if values[2] <= 0 {
			return nil, fmt.Errorf("invalid identity map line %q", scanner.Text())
		}
		entries = append(entries, idMapEntry{containerID: values[0], hostID: values[1], size: values[2]})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("identity map is empty")
	}
	return entries, nil
}

func dropToIdentity(uid, gid int64, allowNamespaceRoot bool) error {
	if !allowNamespaceRoot && (uid == 0 || gid == 0) {
		return errors.New("namespace-root file identity is not allowed")
	}
	currentUID := int64(syscall.Getuid())
	currentGID := int64(syscall.Getgid())
	if currentUID == uid && currentGID == gid {
		// A namespace-root process can still carry inherited supplementary
		// groups. Clear them even when the IDs already match; an ordinary
		// unprivileged test process cannot, so leave its groups untouched.
		if currentUID == 0 {
			if err := syscall.Setgroups([]int{int(gid)}); err != nil {
				return err
			}
			return verifyProcessIdentity(uid, gid, true)
		}
		return verifyProcessIdentity(uid, gid, false)
	}
	if err := syscall.Setgroups([]int{int(gid)}); err != nil {
		return err
	}
	if err := syscall.Setgid(int(gid)); err != nil {
		return err
	}
	if err := syscall.Setuid(int(uid)); err != nil {
		return err
	}
	return verifyProcessIdentity(uid, gid, true)
}

func verifyProcessIdentity(uid, gid int64, checkGroups bool) error {
	if int64(syscall.Getuid()) != uid || int64(syscall.Getgid()) != gid {
		return fmt.Errorf("file identity is %d:%d, want %d:%d", syscall.Getuid(), syscall.Getgid(), uid, gid)
	}
	if !checkGroups {
		return nil
	}
	groups, err := syscall.Getgroups()
	if err != nil {
		return err
	}
	if len(groups) != 1 || int64(groups[0]) != gid {
		return fmt.Errorf("supplementary groups are %v, want [%d]", groups, gid)
	}
	return nil
}
