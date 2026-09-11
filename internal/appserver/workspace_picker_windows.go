//go:build windows

package appserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func chooseWorkspace(ctx context.Context) (string, bool, error) {
	script := `
Add-Type -AssemblyName System.Windows.Forms | Out-Null
[System.Windows.Forms.Application]::EnableVisualStyles()
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择新对话的工作区'
$dialog.ShowNewFolderButton = $true
if ($dialog.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) {
    exit 2
}
[Console]::Out.Write($dialog.SelectedPath)
`
	powershell, err := exec.LookPath("powershell")
	if err != nil {
		powershell = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	}
	command := exec.CommandContext(ctx, powershell, "-NoProfile", "-STA", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 2 {
			return "", true, nil
		}
		if ctx.Err() != nil {
			return "", true, nil
		}
		return "", false, fmt.Errorf("open workspace picker: %w", err)
	}
	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", true, nil
	}
	return selected, false, nil
}
