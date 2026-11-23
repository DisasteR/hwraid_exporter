// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package nodes

import u "golang.org/x/sys/unix"

type req struct{ Ioctl uint32 } // champ homonyme : ne doit pas être signalé

func f(r req) { _, _ = u.IoctlGetInt(0, uint(r.Ioctl)) }
