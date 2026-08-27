package tools

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var readOnlyCommands = []string{"file", "find", "head", "ls", "pwd", "rg", "sed", "stat", "tail", "wc"}

// ExecPolicyRequirement describes whether a command can run and which reusable
// prefix may be offered for Session approval.
type ExecPolicyRequirement struct {
	NeedsApproval bool
	Reason        string
	ProposedRule  []string
	MatchedRule   string
}

// ExecPolicy owns built-in rules and Session-scoped approved prefixes.
type ExecPolicy struct {
	mu           sync.RWMutex
	sessionRules [][]string
}

// NewExecPolicy creates the policy state owned by one Session router.
func NewExecPolicy() *ExecPolicy {
	return &ExecPolicy{}
}

// Evaluate parses one command and checks built-in and Session rules.
func (policy *ExecPolicy) Evaluate(command string, requestedPrefix []string) (ExecPolicyRequirement, error) {
	tokens, reusable, err := parseSimpleCommand(command)
	if err != nil {
		return ExecPolicyRequirement{}, err
	}
	if reusable && isBuiltInReadOnly(tokens) {
		return ExecPolicyRequirement{MatchedRule: "built-in read-only: " + tokens[0]}, nil
	}
	if reusable {
		policy.mu.RLock()
		defer policy.mu.RUnlock()
		for _, rule := range policy.sessionRules {
			if hasTokenPrefix(tokens, rule) {
				return ExecPolicyRequirement{MatchedRule: "session prefix: " + strings.Join(rule, " ")}, nil
			}
		}
	}

	reason := "command is outside the automatic read-only policy"
	if !reusable {
		return ExecPolicyRequirement{NeedsApproval: true, Reason: reason + "; compound shell commands cannot be cached"}, nil
	}
	proposed := requestedPrefix
	if !validRequestedPrefix(tokens, proposed) {
		proposed = derivePrefixRule(tokens)
	}
	return ExecPolicyRequirement{
		NeedsApproval: true, Reason: reason, ProposedRule: proposed,
	}, nil
}

// AddSessionRule caches one exact argv prefix until the Session closes.
func (policy *ExecPolicy) AddSessionRule(rule []string) {
	if len(rule) == 0 {
		return
	}
	copyOfRule := append([]string(nil), rule...)
	policy.mu.Lock()
	defer policy.mu.Unlock()
	for _, existing := range policy.sessionRules {
		if slices.Equal(existing, copyOfRule) {
			return
		}
	}
	policy.sessionRules = append(policy.sessionRules, copyOfRule)
}

func parseSimpleCommand(command string) ([]string, bool, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, false, errors.New("cmd must not be empty")
	}
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped := false
	for _, character := range command {
		if escaped {
			token.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				token.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if strings.ContainsRune(";|&><`\n\r", character) || character == '$' {
			return strings.Fields(command), false, nil
		}
		if character == ' ' || character == '\t' {
			if token.Len() > 0 {
				tokens = append(tokens, token.String())
				token.Reset()
			}
			continue
		}
		token.WriteRune(character)
	}
	if escaped || quote != 0 {
		return nil, false, errors.New("cmd contains an unfinished escape or quote")
	}
	if token.Len() > 0 {
		tokens = append(tokens, token.String())
	}
	if len(tokens) == 0 {
		return nil, false, errors.New("cmd must not be empty")
	}
	return tokens, true, nil
}

func isBuiltInReadOnly(tokens []string) bool {
	if !slices.Contains(readOnlyCommands, filepath.Base(tokens[0])) {
		return false
	}
	for _, token := range tokens[1:] {
		trimmed := strings.Trim(token, "'\"")
		if filepath.IsAbs(trimmed) || trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.Contains(trimmed, "/../") {
			return false
		}
	}
	return true
}

func derivePrefixRule(tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	length := 2
	if len(tokens) >= 3 && slices.Contains([]string{"npm", "pnpm", "yarn"}, filepath.Base(tokens[0])) && tokens[1] == "run" {
		length = 3
	}
	return append([]string(nil), tokens[:length]...)
}

func validRequestedPrefix(tokens, prefix []string) bool {
	return len(prefix) >= 2 && hasTokenPrefix(tokens, prefix)
}

func hasTokenPrefix(tokens, prefix []string) bool {
	return len(prefix) <= len(tokens) && slices.Equal(tokens[:len(prefix)], prefix)
}

func formatPrefixRule(rule []string) string {
	return fmt.Sprintf("[%s]", strings.Join(rule, ", "))
}
