package odbc

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaitDBFailureBoundaries(t *testing.T) {
	for _, backend := range []string{"mssql", "mysql"} {
		for _, mode := range []string{"dead", "not-ready", "blocked-logs", "blocked-create", "database-error", "large-error", "existing", "ready"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				output, calls, err := runWaitDBFixture(t, backend, mode)
				if mode == "existing" || mode == "ready" {
					if err != nil {
						t.Fatalf("valid startup failed: %v\n%s", err, output)
					}
					return
				}
				if err == nil {
					t.Fatal("permanent startup failure was accepted")
				}
				if exit, ok := errors.AsType[*osexec.ExitError](err); ok && exit.ExitCode() == 124 {
					t.Fatalf("startup needed the outer timeout instead of stopping itself\n%s", output)
				}
				if mode == "large-error" && len(output) > 6000 {
					t.Errorf("unbounded diagnostic: %d bytes", len(output))
				}
				if !strings.Contains(output, "ODBC startup") {
					t.Errorf("missing startup diagnostic: %s", output)
				}
				if (mode == "database-error" || mode == "blocked-create" || mode == "large-error") && strings.Count(calls, "exec\n") != 1 {
					t.Errorf("permanent database error was retried: %q", calls)
				}
			})
		}
	}
}

func runWaitDBFixture(t *testing.T, backend, mode string) (string, string, error) {
	t.Helper()
	makefile, err := filepath.Abs("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	mock := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$1" >> "$ODBC_TEST_CALLS"
case "$1" in
network|run) exit 0 ;;
inspect)
  if [[ "$ODBC_TEST_MODE" == dead ]]; then printf 'false\n'; else printf 'true\n'; fi ;;
logs)
  if [[ "$ODBC_TEST_MODE" == blocked-logs ]]; then exec sleep 20; fi
  if [[ "$ODBC_TEST_MODE" == not-ready ]]; then printf 'still starting\n'; exit 0; fi
  if [[ "$ODBC_TEST_BACKEND" == mssql ]]; then
    printf 'SQL Server is now ready for client connections\n'
  else
    printf 'Version: fixture port: 3306\n'
  fi ;;
exec)
  if [[ "$ODBC_TEST_MODE" == blocked-create ]]; then exec sleep 20; fi
  if [[ "$ODBC_TEST_MODE" == large-error ]]; then printf '%20000s\n' failure >&2; exit 2; fi
  if [[ "$ODBC_TEST_MODE" == dead ]]; then printf 'container is not running\n' >&2; exit 3; fi
  if [[ "$ODBC_TEST_MODE" == database-error ]]; then printf 'synthetic permanent database failure\n' >&2; exit 2; fi
  if [[ "$ODBC_TEST_MODE" == existing && "${ODBC_TEST_DB_QUERY:-}" != *"IF"* ]]; then exit 2; fi ;;
*) printf 'unexpected mock command\n' >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := osexec.Command("timeout", "--kill-after=1s", "3s", "make", "--no-print-directory", "-f", makefile,
		"start-"+backend, "ODBC_READY_TIMEOUT=1", "ODBC_COMMAND_TIMEOUT=1", "PASSWORD=synthetic")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ODBC_TEST_MODE="+mode, "ODBC_TEST_BACKEND="+backend, "ODBC_TEST_CALLS="+calls)
	output, runErr := cmd.CombinedOutput()
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	return string(output), string(data), runErr
}


func TestWaitDBRejectsInvalidOptions(t *testing.T) {
	script, err := filepath.Abs("waitdb.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"ODBC_READY_TIMEOUT=0", "ODBC_READY_TIMEOUT=invalid", "ODBC_READY_TIMEOUT=3601", "ODBC_COMMAND_TIMEOUT=-1", "DB_NAME=bad name", "DB_NAME="} {
		cmd := osexec.Command("timeout", "3s", "bash", script, "mssql", "fixture")
		cmd.Env = append(os.Environ(), "DB_NAME=test", "ODBC_READY_TIMEOUT=1", "ODBC_COMMAND_TIMEOUT=1", option)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "ODBC startup") {
			t.Errorf("invalid option %s: %v, %s", option, err, output)
		}
	}
}
