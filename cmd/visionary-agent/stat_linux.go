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

// Device and inode catch rotation even when the new file has the same name.
func fileID(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10)
	}
	return ""
}

// diskUse is the space a file takes (allocated blocks, not its length), and
// what identifies it when it has more than one hard link.
func diskUse(fi os.FileInfo) (bytes int64, dev, ino uint64, links uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512, uint64(st.Dev), st.Ino, uint64(st.Nlink)
	}
	return fi.Size(), 0, 0, 1
}
