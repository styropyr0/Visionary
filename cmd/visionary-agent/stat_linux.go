package main

import (
	"os"
	"strconv"
	"syscall"
)

// statOwner is a file's owner, group and change time (ns), from its stat.
func statOwner(fi os.FileInfo) (uid, gid uint32, ctime int64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid, st.Gid, st.Ctim.Nano()
	}
	return 0, 0, 0
}

// fileID names a file by device and inode, so a rotated log is noticed even
// when the new file has the same name.
func fileID(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10)
	}
	return ""
}
