// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"time"
)

// RunLoop lance un cycle de collecte tout de suite, puis à chaque interval,
// jusqu'à la fin de ctx. Aucun cycle ne démarre une fois ctx terminé ;
// collect reçoit ctx pour arrêter le cycle en cours.
func RunLoop(ctx context.Context, interval time.Duration, collect func(context.Context)) {
	if ctx.Err() != nil {
		return
	}
	collect(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ctx.Err() != nil {
				return // arrêt demandé en même temps que le tic
			}
			collect(ctx)
		}
	}
}

// WaitIdle attend, sans limite, la fin de la boucle de collecte (loopDone)
// puis celle des collectes encore en cours (drain), y compris celles
// abandonnées à l'échéance d'un cycle. Tant qu'il attend, il appelle warn
// toutes les every avec la durée écoulée.
//
// Aucune limite interne : sortir pendant une commande de gestion
// l'interromprait, et le pilote aacraid perdrait un bloc de commande jusqu'au
// redémarrage. Le seul délai est celui du superviseur (stop_grace_period,
// TimeoutStopSec), réglé par l'exploitant.
func WaitIdle(loopDone <-chan struct{}, drain func(time.Duration) bool, every time.Duration, warn func(waited time.Duration)) {
	start := time.Now()
	t := time.NewTicker(every)
	defer t.Stop()
	for waiting := true; waiting; {
		select {
		case <-loopDone:
			waiting = false
		case <-t.C:
			warn(time.Since(start))
		}
	}
	for !drain(every) {
		warn(time.Since(start))
	}
}
