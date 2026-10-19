// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const vaultSetUsage = `usage:
  gate4ai vault set <alias>                   asks for the value (input hidden, Enter — done)
  printf %s SECRET | gate4ai vault set <alias>  the value from stdin (scripts)
  gate4ai vault set <alias> --from-env VAR      the value from an environment variable
  (or in the Web UI: Secrets — every field of an account has its own "set" form)`

// readSecret — значение секрета: из переменной окружения, скрытым вводом в
// терминале (до Enter) или из stdin (скрипты). Пусто — ошибка с подсказкой,
// где ввести значение.
func (e *Env) readSecret(alias, fromEnv string) (string, error) {
	if fromEnv != "" {
		v := e.Getenv(fromEnv)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", fromEnv)
		}
		return v, nil
	}
	if f, ok := e.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(e.Stderr, "Value of %s (input hidden, Enter — done): ", "@"+strings.TrimPrefix(alias, "@"))
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(e.Stderr)
		if err != nil {
			return "", err
		}
		if len(b) == 0 {
			return "", errors.New("empty value: nothing is saved")
		}
		return string(b), nil
	}
	b, err := io.ReadAll(io.LimitReader(e.Stdin, 1<<20))
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", errors.New("empty value: there is no terminal to ask in (for example, inside an agent session); " +
			"run gate4ai vault set " + alias + " in your own terminal — it asks with hidden input — or set it in the Web UI: Secrets")
	}
	return v, nil
}
