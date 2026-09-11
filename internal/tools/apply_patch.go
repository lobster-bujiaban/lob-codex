package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errPatchDenied = errors.New("apply_patch denied by user")

const applyPatchInstructions = `The apply_patch tool edits files with a stripped-down, file-oriented diff.
Do not wrap the patch in extra markdown. File paths should stay inside the workspace.

*** Begin Patch
*** Add File: path
+file contents
*** Delete File: path
*** Update File: path
*** Move to: newpath
@@ optional context
 context line
-removed
+added
*** End of File
*** End Patch`

// ApplyPatchExecutor applies Codex apply_patch hunks inside the workspace.
type ApplyPatchExecutor struct {
	Policy *ExecPolicy
}

func (ApplyPatchExecutor) Definition() Definition {
	return Definition{
		Type:        "function",
		Name:        "apply_patch",
		Description: applyPatchInstructions,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string", "description": "Complete apply_patch text, including Begin Patch / End Patch markers."},
				"patch": map[string]any{"type": "string", "description": "Alias for input."},
			},
			"additionalProperties": false,
		},
		Strict: false,
	}
}

func (executor ApplyPatchExecutor) Execute(ctx context.Context, invocation Invocation) (string, error) {
	patch, err := extractPatchInput(invocation.Call.Arguments)
	if err != nil {
		return "", err
	}
	hunks, err := parsePatch(patch)
	if err != nil {
		return "", fmt.Errorf("apply_patch verification failed: %w", err)
	}
	if err := executor.approve(ctx, invocation, hunks); err != nil {
		if errors.Is(err, errPatchDenied) {
			return err.Error(), nil
		}
		return "", err
	}
	affected, err := applyHunks(invocation.Environment.WorkspaceRoot, invocation.Environment.WorkingDirectory, hunks)
	if err != nil {
		return "", err
	}
	return formatPatchSummary(affected), nil
}

func (executor ApplyPatchExecutor) approve(ctx context.Context, invocation Invocation, hunks []patchHunk) error {
	requirement, err := executor.Policy.Evaluate("apply_patch", []string{"apply_patch"})
	if err != nil {
		return err
	}
	if !requirement.NeedsApproval {
		return nil
	}
	if invocation.Reviewer == nil {
		return errors.New("apply_patch requires approval, but no approval reviewer is connected")
	}
	paths := make([]string, 0, len(hunks))
	for _, hunk := range hunks {
		paths = append(paths, hunk.sourcePath())
	}
	decision, err := invocation.Reviewer(ctx, ApprovalRequest{
		CallID:           invocation.Call.CallID,
		Command:          "apply_patch " + strings.Join(paths, " "),
		WorkingDirectory: invocation.Environment.WorkingDirectory,
		Reason:           "apply_patch writes files in the workspace",
		ProposedPrefix:   []string{"apply_patch"},
	})
	if err != nil {
		return err
	}
	switch decision {
	case ApprovalApproved:
		return nil
	case ApprovalApprovedForSession:
		executor.Policy.AddSessionRule([]string{"apply_patch"})
		return nil
	case ApprovalApprovedWithAmendment:
		return executor.Policy.AddPersistentRule([]string{"apply_patch"})
	case ApprovalDenied:
		return errPatchDenied
	default:
		return fmt.Errorf("unsupported approval decision %q", decision)
	}
}

func extractPatchInput(arguments string) (string, error) {
	var fields struct {
		Input string `json:"input"`
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(arguments), &fields); err == nil {
		if strings.TrimSpace(fields.Input) != "" {
			return fields.Input, nil
		}
		if strings.TrimSpace(fields.Patch) != "" {
			return fields.Patch, nil
		}
	}
	var asString string
	if err := json.Unmarshal([]byte(arguments), &asString); err == nil && strings.Contains(asString, beginPatchMarker) {
		return asString, nil
	}
	if strings.Contains(arguments, beginPatchMarker) {
		return arguments, nil
	}
	return "", errors.New("apply_patch requires patch input")
}

type affectedPaths struct {
	added    []string
	modified []string
	deleted  []string
}

func applyHunks(workspaceRoot, workingDirectory string, hunks []patchHunk) (affectedPaths, error) {
	var affected affectedPaths
	for _, hunk := range hunks {
		switch item := hunk.(type) {
		case addFileHunk:
			path, err := joinUnderWorkspace(workspaceRoot, workingDirectory, item.path)
			if err != nil {
				return affected, err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return affected, fmt.Errorf("Failed to create parent directories for %s: %w", path, err)
			}
			if err := os.WriteFile(path, []byte(item.contents), 0o644); err != nil {
				return affected, fmt.Errorf("Failed to write file %s: %w", path, err)
			}
			affected.added = append(affected.added, path)
		case deleteFileHunk:
			path, err := joinUnderWorkspace(workspaceRoot, workingDirectory, item.path)
			if err != nil {
				return affected, err
			}
			if err := os.Remove(path); err != nil {
				return affected, fmt.Errorf("Failed to delete file %s: %w", path, err)
			}
			affected.deleted = append(affected.deleted, path)
		case updateFileHunk:
			path, err := joinUnderWorkspace(workspaceRoot, workingDirectory, item.path)
			if err != nil {
				return affected, err
			}
			original, err := os.ReadFile(path)
			if err != nil {
				return affected, fmt.Errorf("Failed to read file to update %s: %w", path, err)
			}
			contents, err := deriveUpdatedContents(path, string(original), item.chunks)
			if err != nil {
				return affected, err
			}
			dest := path
			if item.movedTo != "" {
				dest, err = joinUnderWorkspace(workspaceRoot, workingDirectory, item.movedTo)
				if err != nil {
					return affected, err
				}
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return affected, fmt.Errorf("Failed to create parent directories for %s: %w", dest, err)
				}
			}
			if err := os.WriteFile(dest, []byte(contents), 0o644); err != nil {
				return affected, fmt.Errorf("Failed to write file %s: %w", dest, err)
			}
			if dest != path {
				if err := os.Remove(path); err != nil {
					return affected, fmt.Errorf("Failed to remove original %s: %w", path, err)
				}
			}
			affected.modified = append(affected.modified, dest)
		}
	}
	return affected, nil
}

func formatPatchSummary(affected affectedPaths) string {
	var builder strings.Builder
	builder.WriteString("Success. Updated the following files:\n")
	for _, path := range affected.added {
		fmt.Fprintf(&builder, "A %s\n", path)
	}
	for _, path := range affected.modified {
		fmt.Fprintf(&builder, "M %s\n", path)
	}
	for _, path := range affected.deleted {
		fmt.Fprintf(&builder, "D %s\n", path)
	}
	return builder.String()
}
