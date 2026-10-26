package main

import (
	"os"
	"syscall"
)

// statOwner is a file's owner, group and change time (ns), from its stat.
func statOwner(fi os.FileInfo) (uid, gid uint32, ctime int64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid, st.Gid, st.Ctim.Nano()
	}
	return 0, 0, 0
}
