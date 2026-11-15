package coord

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// chooseLocalDirectory opens a native chooser when the host has one. It never
// accepts a command or path from the browser; the typed path remains the
// fallback on headless systems and platforms without a native dialog.
func chooseLocalDirectory(ctx context.Context) (string, bool, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		if path, err := exec.LookPath("zenity"); err == nil {
			cmd = exec.CommandContext(ctx, path, "--file-selection", "--directory", "--title=Choose a Git checkout")
		} else if path, err := exec.LookPath("kdialog"); err == nil {
			cmd = exec.CommandContext(ctx, path, "--getexistingdirectory", ".", "Choose a Git checkout")
		} else {
			return "", false, errors.New("no native folder picker is installed; enter the checkout path")
		}
	case "darwin":
		path, err := exec.LookPath("osascript")
		if err != nil {
			return "", false, errors.New("native folder picker is unavailable; enter the checkout path")
		}
		cmd = exec.CommandContext(ctx, path, "-e", `POSIX path of (choose folder with prompt "Choose a Git checkout")`)
	case "windows":
		path, err := exec.LookPath("powershell.exe")
		if err != nil {
			return "", false, errors.New("native folder picker is unavailable; enter the checkout path")
		}
		script := `Add-Type -AssemblyName System.Windows.Forms; $d=New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description='Choose a Git checkout'; if($d.ShowDialog() -eq 'OK'){[Console]::Write($d.SelectedPath)}`
		cmd = exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
	default:
		return "", false, fmt.Errorf("native folder picker is unsupported on %s; enter the checkout path", runtime.GOOS)
	}
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if runtime.GOOS == "darwin" || exitErr.ExitCode() == 1 {
				return "", false, nil // The user dismissed the native chooser.
			}
			return "", false, fmt.Errorf("native folder picker failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", false, err
	}
	selected := strings.TrimSpace(string(output))
	return selected, selected != "", nil
}
