package execx

import (
	"context"
	"strings"
	"testing"
)

func TestCommandReturnsStdout(t *testing.T) {
	out, err := Command("sh")(context.Background(), t.TempDir(), "-c", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hi\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestCommandErrorCarriesStderrAndExitCode(t *testing.T) {
	_, err := Command("sh")(context.Background(), "", "-c", "echo boom >&2; exit 3")
	if err == nil {
		t.Fatal("expected an error")
	}
	if ExitCode(err) != 3 {
		t.Fatalf("ExitCode = %d, want 3", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not include stderr", err)
	}
}

func TestExitCodeWithoutProcess(t *testing.T) {
	if got := ExitCode(context.Canceled); got != -1 {
		t.Fatalf("ExitCode = %d, want -1", got)
	}
}
