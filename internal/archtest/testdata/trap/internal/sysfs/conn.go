// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package sysfs

import "os"

func f(fh *os.File) { _, _ = fh.SyscallConn() }
