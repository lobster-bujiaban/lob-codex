package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var readOnlyCommands = []string{"file", "find", "head", "ls", "pwd", "rg", "sed", "stat", "tail", "wc"}
var readOnlyGitSubcommands = []string{
	"diff", "grep", "log", "ls-files", "rev-parse", "show", "status",
}

// ExecPolicyRequirement describes whether a command can run and which reusable
// prefix may be offered for Session approval.
type ExecPolicyRequirement struct {
	NeedsApproval bool
	Reason        string
	ProposedRule  []string
	MatchedRule   string
}

// ExecPolicy owns built-in, persisted amendment, and Session-scoped rules.
type ExecPolicy struct {
	mu              sync.RWMutex
	sessionRules    [][]string
	persistentRules [][]string
	rulesPath       string
	loadErr         error
}

type persistentExecPolicy struct {
	Rules [][]string `json:"rules"`
}

// NewExecPolicy creates the policy state owned by one Session router.
func NewExecPolicy(workspaceRoot string) *ExecPolicy {
	policy := &ExecPolicy{rulesPath: filepath.Join(workspaceRoot, "tmp", "exec-policy.rules")}
	contents, err := os.ReadFile(policy.rulesPath)
	if errors.Is(err, os.ErrNotExist) {
		return policy
	}
	if err != nil {
		policy.loadErr = err
		return policy
	}
	var stored persistentExecPolicy
	if err := json.Unmarshal(contents, &stored); err != nil {
		policy.loadErr = err
		return policy
	}
	for _, rule := range stored.Rules {
		if len(rule) >= 2 {
			policy.persistentRules = append(policy.persistentRules, append([]string(nil), rule...))
		}
	}
	return policy
}

// Evaluate parses one command and checks built-in and Session rules.
func (policy *ExecPolicy) Evaluate(command string, requestedPrefix []string) (ExecPolicyRequirement, error) {
	if policy.loadErr != nil {
		return ExecPolicyRequirement{}, fmt.Errorf("load exec policy: %w", policy.loadErr)
	}
	commands, plain, err := parsePlainShellCommands(command)
	if err != nil {
		return ExecPolicyRequirement{}, err
	}
	allReadOnly := plain && len(commands) > 0
	for _, tokens := range commands {
		if !isBuiltInReadOnly(tokens) {
			allReadOnly = false
			break
		}
	}
	if allReadOnly {
		names := make([]string, 0, len(commands))
		for _, tokens := range commands {
			names = append(names, filepath.Base(tokens[0]))
		}
		return ExecPolicyRequirement{MatchedRule: "built-in read-only: " + strings.Join(names, ", ")}, nil
	}
	tokens := commands[0]
	reusable := plain && len(commands) == 1
	if reusable {
		policy.mu.RLock()
		defer policy.mu.RUnlock()
		for _, rule := range policy.persistentRules {
			if hasTokenPrefix(tokens, rule) {
				return ExecPolicyRequirement{MatchedRule: "persistent prefix: " + strings.Join(rule, " ")}, nil
			}
		}
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

// parsePlainShellCommands mirrors Codex's word-only shell parser boundary for
// command sequences joined by safe control operators. Dynamic expansion,
// redirects, grouping, background jobs, and incomplete syntax stay opaque and
// therefore require approval.
func parsePlainShellCommands(command string) ([][]string, bool, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, false, errors.New("cmd must not be empty")
	}
	var commands [][]string
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped := false
	flushToken := func() {
		if token.Len() > 0 {
			tokens = append(tokens, token.String())
			token.Reset()
		}
	}
	flushCommand := func() error {
		flushToken()
		if len(tokens) == 0 {
			return errors.New("cmd contains an empty shell command")
		}
		commands = append(commands, tokens)
		tokens = nil
		return nil
	}
	for index := 0; index < len(command); index++ {
		character := rune(command[index])
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
			} else if character == '$' || character == '`' {
				if quote == '"' {
					return [][]string{strings.Fields(command)}, false, nil
				}
				token.WriteRune(character)
			} else {
				token.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '$' || character == '`' || strings.ContainsRune("><(){}\n\r", character) {
			return [][]string{strings.Fields(command)}, false, nil
		}
		if character == ';' || character == '|' || character == '&' {
			if character == '&' {
				if index+1 >= len(command) || command[index+1] != '&' {
					return [][]string{strings.Fields(command)}, false, nil
				}
				index++
			} else if character == '|' && index+1 < len(command) && command[index+1] == '|' {
				index++
			}
			if err := flushCommand(); err != nil {
				return nil, false, err
			}
			continue
		}
		if character == ' ' || character == '\t' {
			flushToken()
			continue
		}
		token.WriteRune(character)
	}
	if escaped || quote != 0 {
		return nil, false, errors.New("cmd contains an unfinished escape or quote")
	}
	if err := flushCommand(); err != nil {
		return nil, false, err
	}
	return commands, true, nil
}

// AddPersistentRule stores an approved amendment beneath the workspace tmp directory.
func (policy *ExecPolicy) AddPersistentRule(rule []string) error {
	if len(rule) < 2 {
		return errors.New("persistent exec policy rule requires at least two tokens")
	}
	copyOfRule := append([]string(nil), rule...)
	policy.mu.Lock()
	defer policy.mu.Unlock()
	for _, existing := range policy.persistentRules {
		if slices.Equal(existing, copyOfRule) {
			return nil
		}
	}
	rules := append(append([][]string(nil), policy.persistentRules...), copyOfRule)
	contents, err := json.MarshalIndent(persistentExecPolicy{Rules: rules}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode exec policy: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(policy.rulesPath), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	temporaryPath := policy.rulesPath + ".tmp"
	if err := os.WriteFile(temporaryPath, append(contents, '\n'), 0o600); err != nil {
		return fmt.Errorf("write exec policy: %w", err)
	}
	if err := os.Rename(temporaryPath, policy.rulesPath); err != nil {
		return fmt.Errorf("replace exec policy: %w", err)
	}
	policy.persistentRules = rules
	return nil
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
	command := filepath.Base(tokens[0])
	if command == "git" {
		return isBuiltInReadOnlyGit(tokens)
	}
	if !slices.Contains(readOnlyCommands, command) {
		return false
	}
	return hasSafeReadOnlyArguments(tokens[1:])
}

func isBuiltInReadOnlyGit(tokens []string) bool {
	if len(tokens) < 2 {
		return false
	}
	index := 1
	for index < len(tokens) && strings.HasPrefix(tokens[index], "-") {
		option := tokens[index]
		if option == "--no-pager" || option == "--literal-pathspecs" || option == "--no-optional-locks" {
			index++
			continue
		}
		if option == "-C" || option == "--git-dir" || option == "--work-tree" {
			return false
		}
		break
	}
	if index >= len(tokens) || !slices.Contains(readOnlyGitSubcommands, tokens[index]) {
		return false
	}
	arguments := tokens[index+1:]
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "--output") || argument == "--ext-diff" || argument == "--textconv" || strings.HasPrefix(argument, "--open-files-in-pager") {
			return false
		}
	}
	return hasSafeReadOnlyArguments(arguments)
}

func hasSafeReadOnlyArguments(tokens []string) bool {
	for _, token := range tokens {
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
