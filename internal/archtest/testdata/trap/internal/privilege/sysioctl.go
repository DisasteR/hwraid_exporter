package privilege

import "syscall"

func f() { _, _, _ = syscall.Syscall(syscall.SYS_IOCTL, 0, 0, 0) }
