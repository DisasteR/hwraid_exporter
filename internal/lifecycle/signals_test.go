// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Rôle de sous-processus : attend un signal d'arrêt, puis sort normalement
// en l'annonçant (arrêt propre). Un signal non capté le tue à la place.
func TestMain(m *testing.M) {
	if os.Getenv("HWRAID_SIGNAL_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), TerminationSignals()...)
		defer stop()
		os.Stdout.WriteString("prêt\n")
		<-ctx.Done()
		os.Stdout.WriteString("arrêt propre\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// SIGTERM, SIGINT et SIGHUP passent par l'arrêt propre : le processus
// n'est pas tué par le signal.
func TestTerminationSignalsStopGracefully(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "HWRAID_SIGNAL_CHILD=1")
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, _ := out.Read(buf) // « prêt » : les signaux sont captés
		if !strings.Contains(string(buf[:n]), "prêt") {
			t.Fatalf("%v : sous-processus non prêt (%q)", sig, buf[:n])
		}
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				t.Fatalf("%v : processus tué ou en échec (%v)", sig, err)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%v : le processus ne s'arrête pas", sig)
		}
	}
}
