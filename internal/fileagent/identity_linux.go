//go:build linux

package fileagent

import "syscall"

func dropToIdentity(uid, gid int64) error {
	if int64(syscall.Getuid()) == uid && int64(syscall.Getgid()) == gid {
		return nil
	}
	if err := syscall.Setgroups([]int{int(gid)}); err != nil {
		return err
	}
	if err := syscall.Setgid(int(gid)); err != nil {
		return err
	}
	return syscall.Setuid(int(uid))
}
