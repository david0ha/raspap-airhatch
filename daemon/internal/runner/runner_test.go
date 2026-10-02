package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func helperCommand(t *testing.T, mode string, args ...string) (string, []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable, append([]string{"-test.run=^TestProcessHelper$", "--", mode}, args...)
}

func TestProcessHelper(t *testing.T) {
	index := 0
	for i, arg := range os.Args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index == 0 {
		return
	}
	switch os.Args[index] {
	case "stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "echo":
		fmt.Fprint(os.Stdout, strings.Join(os.Args[index+1:], "|"))
	case "output":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 4096))
		fmt.Fprint(os.Stderr, strings.Repeat("y", 4096))
	case "sleep":
		time.Sleep(time.Minute)
	case "fail":
		fmt.Fprint(os.Stderr, "failure")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestExecArgv(t *testing.T) {
	name, args := helperCommand(t, "echo", "a b", "$(touch impossible)", "; echo injected", "\"quotes\"")
	out, err := NewExec(5*time.Second, 1024).Run(context.Background(), name, args...)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a b|$(touch impossible)|; echo injected|\"quotes\"" {
		t.Fatalf("argv changed: %q", out)
	}
}

func TestExecOutputLimit(t *testing.T) {
	name, args := helperCommand(t, "output")
	out, err := NewExec(5*time.Second, 100).Run(context.Background(), name, args...)
	if !errors.Is(err, ErrOutputLimit) || len(out) != 100 {
		t.Fatalf("len=%d err=%v", len(out), err)
	}
}

func TestExecTimeoutAndCancel(t *testing.T) {
	name, args := helperCommand(t, "sleep")
	_, err := NewExec(30*time.Millisecond, 1024).Run(context.Background(), name, args...)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewExec(5*time.Second, 1024).Run(ctx, name, args...)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestExecFailure(t *testing.T) {
	name, args := helperCommand(t, "fail")
	out, err := NewExec(5*time.Second, 1024).Run(context.Background(), name, args...)
	if err == nil || string(out) != "failure" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestFake(t *testing.T) {
	wantErr := errors.New("failure")
	f := NewFake(Step{Name: "tool", Args: []string{"first"}, Output: []byte("ok")}, Step{Name: "tool", Args: []string{"second"}, Err: wantErr})
	out, err := f.Run(context.Background(), "tool", "wrong")
	if err == nil || f.Remaining() != 2 {
		t.Fatalf("mismatch consumed step: %v %d", err, f.Remaining())
	}
	out, err = f.Run(context.Background(), "tool", "first")
	if err != nil || string(out) != "ok" {
		t.Fatalf("first: %q %v", out, err)
	}
	_, err = f.Run(context.Background(), "tool", "second")
	if !errors.Is(err, wantErr) || f.Remaining() != 0 {
		t.Fatalf("second: %v", err)
	}
	_, err = f.Run(context.Background(), "unexpected")
	if err == nil {
		t.Fatal("unscripted command accepted")
	}
	calls := f.Calls()
	calls[0].Args[0] = "mutated"
	if reflect.DeepEqual(calls, f.Calls()) {
		t.Fatal("calls share mutable storage")
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := NewFake()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, _ = f.Run(context.Background(), "tool")
			_ = f.Calls()
			_ = f.Remaining()
		})
	}
	wg.Wait()
	if len(f.Calls()) != 20 {
		t.Fatalf("got %d calls", len(f.Calls()))
	}
}

func TestExecInput(t *testing.T) {
	name, args := helperCommand(t, "stdin")
	out, err := NewExec(5*time.Second, 1024).RunInput(context.Background(), []byte("secret\n"), name, args...)
	if err != nil || string(out) != "secret\n" {
		t.Fatalf("stdin: %q %v", out, err)
	}
}
func TestFakeInput(t *testing.T) {
	f := NewFake(Step{Name: "php", Input: []byte("secret")})
	if _, err := f.RunInput(context.Background(), []byte("wrong"), "php"); err == nil {
		t.Fatal("accepted wrong input")
	}
	if _, err := f.RunInput(context.Background(), []byte("secret"), "php"); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	calls[1].Input[0] = 'x'
	if string(f.Calls()[1].Input) != "secret" {
		t.Fatal("input aliases call storage")
	}
}
