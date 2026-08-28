package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wrapPatch(body string) string {
	return "*** Begin Patch\n" + strings.TrimRight(body, "\n") + "\n*** End Patch"
}

func TestApplyPatchAddUpdateDelete(t *testing.T) {
	dir := t.TempDir()
	policy := NewExecPolicy(dir)
	executor := ApplyPatchExecutor{Policy: policy}
	reviewer := func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalApproved, nil
	}

	add := wrapPatch("*** Add File: notes.txt\n+hello\n+world\n")
	output, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "add", Name: "apply_patch", Arguments: patchJSON(add)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(output, "A ") || !strings.Contains(output, "notes.txt") {
		t.Fatalf("add summary = %q", output)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "hello\nworld\n"; got != want {
		t.Fatalf("added contents = %q, want %q", got, want)
	}

	update := wrapPatch("*** Update File: notes.txt\n@@\n hello\n-world\n+WORLD\n")
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "upd", Name: "apply_patch", Arguments: patchJSON(update)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	contents, _ = os.ReadFile(filepath.Join(dir, "notes.txt"))
	if got, want := string(contents), "hello\nWORLD\n"; got != want {
		t.Fatalf("updated contents = %q, want %q", got, want)
	}

	del := wrapPatch("*** Delete File: notes.txt\n")
	output, err = executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "del", Name: "apply_patch", Arguments: patchJSON(del)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(output, "D ") {
		t.Fatalf("delete summary = %q", output)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists: %v", err)
	}
}

func TestApplyPatchUnicodeDashAndEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unicode.py")
	if err := os.WriteFile(path, []byte("import asyncio # local import \u2013 avoids top\u2011level dep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := wrapPatch("*** Update File: unicode.py\n@@\n-import asyncio # local import - avoids top-level dep\n+import asyncio # HELLO\n")
	executor := ApplyPatchExecutor{Policy: NewExecPolicy(dir)}
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "u", Name: "apply_patch", Arguments: patchJSON(patch)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    func(context.Context, ApprovalRequest) (ApprovalDecision, error) { return ApprovalApproved, nil },
	}); err != nil {
		t.Fatalf("unicode update: %v", err)
	}
	contents, _ := os.ReadFile(path)
	if got, want := string(contents), "import asyncio # HELLO\n"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}

func TestApplyPatchInterleavedChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "interleaved.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne\nf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := wrapPatch("*** Update File: interleaved.txt\n@@\n a\n-b\n+B\n@@\n c\n d\n-e\n+E\n@@\n f\n+g\n*** End of File\n")
	executor := ApplyPatchExecutor{Policy: NewExecPolicy(dir)}
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "i", Name: "apply_patch", Arguments: patchJSON(patch)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    func(context.Context, ApprovalRequest) (ApprovalDecision, error) { return ApprovalApproved, nil },
	}); err != nil {
		t.Fatalf("interleaved: %v", err)
	}
	contents, _ := os.ReadFile(path)
	if got, want := string(contents), "a\nB\nc\nd\nE\nf\ng\n"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}

func TestApplyPatchRejectsWorkspaceEscapeAndGit(t *testing.T) {
	dir := t.TempDir()
	executor := ApplyPatchExecutor{Policy: NewExecPolicy(dir)}
	reviewer := func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalApproved, nil
	}
	escape := wrapPatch("*** Add File: ../../escape.txt\n+nope\n")
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "esc", Name: "apply_patch", Arguments: patchJSON(escape)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := wrapPatch("*** Add File: .git/config\n+bad\n")
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "git", Name: "apply_patch", Arguments: patchJSON(git)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	}); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("git error = %v", err)
	}
}

func TestApplyPatchSessionApprovalSkipsSecondPrompt(t *testing.T) {
	dir := t.TempDir()
	executor := ApplyPatchExecutor{Policy: NewExecPolicy(dir)}
	prompts := 0
	reviewer := func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		prompts++
		return ApprovalApprovedForSession, nil
	}
	first := wrapPatch("*** Add File: a.txt\n+one\n")
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "1", Name: "apply_patch", Arguments: patchJSON(first)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	}); err != nil {
		t.Fatal(err)
	}
	second := wrapPatch("*** Add File: b.txt\n+two\n")
	if _, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "2", Name: "apply_patch", Arguments: patchJSON(second)},
		Environment: Environment{WorkspaceRoot: dir, WorkingDirectory: dir},
		Reviewer:    reviewer,
	}); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
}

func patchJSON(patch string) string {
	encoded, _ := json.Marshal(map[string]string{"input": patch})
	return string(encoded)
}
