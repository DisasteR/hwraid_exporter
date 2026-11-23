package sysfs

import "os"

func f(fh *os.File) { _, _ = fh.SyscallConn() }
