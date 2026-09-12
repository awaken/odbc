package odbc

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Exercise the real recipes with a recorder; no container daemon is contacted.
func TestMakefileSQLServerRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX make shell")
	}
	dir, err := os.MkdirTemp("../../../tmp", "odbc-make-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	recorder := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$ODBC_TEST_ARGS\"\n"
	if err = os.WriteFile(filepath.Join(dir, "docker"), []byte(recorder), 0700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"test-mssql", "test-mssql-freetds", "test-mssql-race"} {
		for _, keep := range []string{"0", "1"} {
			t.Run(target+"/keep="+keep, func(t *testing.T) {
				log := filepath.Join(dir, "args")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := osexec.CommandContext(ctx, "make", target)
				cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "ODBC_TEST_ARGS="+log, "ODBC_TEST_KEEP="+keep)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("make: %v: %s", err, out)
				}
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Fields(string(data))
				if slices.Contains(args, "--rm") != (keep == "0") {
					t.Error("container retention does not match ODBC_TEST_KEEP")
				}
				for _, flag := range []string{"-it", "-i", "-t", "--interactive", "--tty"} {
					if slices.Contains(args, flag) {
						t.Errorf("noninteractive target uses %s", flag)
					}
				}
			})
		}
	}
}
