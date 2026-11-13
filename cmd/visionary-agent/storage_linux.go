package main

import "syscall"

// lowPriority puts the calling thread at nice 19 and in the idle I/O class,
// so it reads the disk only when nothing else wants it. Both are per thread
// on Linux; failures leave the thread as it was.
func lowPriority() {
	tid := syscall.Gettid()
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, tid, 19)
	const whoProcess, classIdle, classShift = 1, 3, 13
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, whoProcess, uintptr(tid), classIdle<<classShift)
}
