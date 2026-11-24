// Copyright 2026 Benjamin Saiz
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Rôle de sous-processus : attend un signal d'arrêt, puis sort normalement
// en l'annonçant (arrêt propre). Un signal non capté le tue à la place.
func TestMain(m *testing.M) {
	if f := os.Getenv("HWRAID_PIPE_CHILD"); f != "" {
		// stderr est un tube dont le lecteur est déjà fermé.
		if os.Getenv("HWRAID_PIPE_IGNORE") == "1" {
			IgnoreBrokenPipe()
		}
		os.Stderr.WriteString("journal\n")
		_ = os.WriteFile(f, []byte("vivant"), 0o600)
		os.Exit(0)
	}
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
		// Toute la vie du sous-processus est bornée, lecture de « prêt »
		// comprise.
		timer := time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
		lines := make(chan string, 2)
		go func() {
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
		}()
		if l := <-lines; l != "prêt" { // les signaux sont captés
			t.Fatalf("%v : sous-processus non prêt (%q)", sig, l)
		}
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		last := <-lines
		err = cmd.Wait()
		timer.Stop()
		var ee *exec.ExitError
		if errors.As(err, &ee) || err != nil {
			t.Fatalf("%v : processus tué, en échec ou arrêté par le délai (%v)", sig, err)
		}
		if last != "arrêt propre" {
			t.Fatalf("%v : sortie sans arrêt propre (%q)", sig, last)
		}
	}
}

// Lecteur de stderr disparu : sans IgnoreBrokenPipe, l'écriture du journal
// tue le processus (témoin) ; avec, il continue.
func TestIgnoreBrokenPipe(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		mark := t.TempDir() + "/vivant"
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		r.Close() // lecteur disparu
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "HWRAID_PIPE_CHILD="+mark)
		if ignore {
			cmd.Env = append(cmd.Env, "HWRAID_PIPE_IGNORE=1")
		}
		cmd.Stderr = w
		err = cmd.Run()
		w.Close()
		_, statErr := os.Stat(mark)
		alive := err == nil && statErr == nil
		if alive != ignore {
			t.Fatalf("SIGPIPE ignoré=%v : processus vivant=%v (%v)", ignore, alive, err)
		}
	}
}
