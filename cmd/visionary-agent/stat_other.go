//go:build !linux

package main

import "os"

// statOwner is a file's owner, group and change time; the collectors that
// use it only run on Linux.
func statOwner(os.FileInfo) (uid, gid uint32, ctime int64) { return 0, 0, 0 }
