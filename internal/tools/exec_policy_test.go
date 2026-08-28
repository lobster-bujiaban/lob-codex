package tools

import "testing"

func TestReadOnlyGitCompoundCommandDoesNotRequireApproval(t *testing.T) {
	policy := NewExecPolicy(t.TempDir())
	requirement, err := policy.Evaluate("pwd && rg --files -g 'AGENTS.md' | head -80 && git status --short && git diff --stat && git diff --cached --stat", nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if requirement.NeedsApproval {
		t.Fatalf("read-only compound command requires approval: %+v", requirement)
	}
}

func TestMutatingGitCommandStillRequiresApproval(t *testing.T) {
	policy := NewExecPolicy(t.TempDir())
	for _, command := range []string{"git add .", "git commit -m test", "git restore file.go", "git diff --output=review.patch", "git status --short && git clean -fd"} {
		requirement, err := policy.Evaluate(command, nil)
		if err != nil {
			t.Fatalf("Evaluate %q: %v", command, err)
		}
		if !requirement.NeedsApproval {
			t.Fatalf("mutating command %q did not require approval: %+v", command, requirement)
		}
	}
}

func TestGitPathOverrideStillRequiresApproval(t *testing.T) {
	policy := NewExecPolicy(t.TempDir())
	requirement, err := policy.Evaluate("git -C ../other status --short", nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !requirement.NeedsApproval {
		t.Fatalf("git path override did not require approval: %+v", requirement)
	}
}
