// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package sysfs

import (
	"syscall"
	"unsafe"
)

var _ = syscall.Getpid
var _ unsafe.Pointer
