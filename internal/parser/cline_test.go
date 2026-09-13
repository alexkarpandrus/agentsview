package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/money"
)

// clineRIDDigestForTest recomputes the stable chain-root digest the parser
// derives from a raw session ID, so tests assert the exact normative rule
// (rid-<first 32 hex of SHA-256>) rather than a hard-coded snapshot.
func clineRIDDigestForTest(t *testing.T, rawSessionID string) string {
	t.Helper()
	h := sha256.New()
	_, err := h.Write([]byte(rawSessionID))
	require.NoError(t, err)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// clineTeammateResultBySource returns the index lookup for a teammate result by
// its raw source session ID, so tests assert identity by source identity,
// never by result index (winners sort by content authority, chains are ordered
// by root identity).
func clineTeammateResultBySource(
	results []ParseResult,
) func(string) (int, bool) {
	bySource := make(map[string]int)
	for i := 1; i < len(results); i++ {
		bySource[results[i].Session.SourceSessionID] = i
	}
	return func(sourceSessionID string) (int, bool) {
		i, ok := bySource[sourceSessionID]
		return i, ok
	}
}

func TestCleanClinePrompt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain prompt",
			input: "Please fix the button styling",
			want:  "Please fix the button styling",
		},
		{
			name:  "user_input mode act",
			input: `<user_input mode="act">Please fix the button styling</user_input>`,
			want:  "Please fix the button styling",
		},
		{
			name:  "user_input mode plan",
			input: `<user_input mode="plan">Investigate the architecture</user_input>`,
			want:  "Investigate the architecture",
		},
		{
			name:  "user_input no mode",
			input: `<user_input>Run unit tests</user_input>`,
			want:  "Run unit tests",
		},
		{
			name:  "multiline user_input with whitespace",
			input: "  <user_input mode=\"act\">\nLine 1\nLine 2\n</user_input>  ",
			want:  "Line 1\nLine 2",
		},
		{
			name:  "user_input plan with mode_notice",
			input: `<user_input mode="plan"><mode_notice>The user switched from act mode to plan mode before sending this message.</mode_notice>` + "\n" + `if today is your birthday what would you do</user_input>`,
			want:  "if today is your birthday what would you do",
		},
		{
			name:  "user_input act with mode_notice",
			input: `<user_input mode="act"><mode_notice>The user switched from plan mode to act mode before sending this message.</mode_notice>` + "\n" + `Implement the feature</user_input>`,
			want:  "Implement the feature",
		},
		{
			name:  "empty user_input",
			input: `<user_input mode="act"></user_input>`,
			want:  "",
		},
		{
			name:  "empty user_input with whitespace",
			input: "<user_input mode=\"act\">   \n  </user_input>",
			want:  "",
		},
		{
			name:  "empty user_input with only mode_notice",
			input: `<user_input mode="plan"><mode_notice>The user switched from act mode to plan mode before sending this message.</mode_notice></user_input>`,
			want:  "",
		},
		{
			name:  "standalone mode_notice without user_input",
			input: `<mode_notice>The user switched from act mode to plan mode before sending this message.</mode_notice> hello world`,
			want:  "hello world",
		},
		{
			name:  "multiple mode_notices with text in between",
			input: `<user_input mode="plan"><mode_notice>First notice</mode_notice>Part A <mode_notice>Second notice</mode_notice>Part B</user_input>`,
			want:  "Part A Part B",
		},
		{
			name:  "unclosed mode_notice preserved fail-visible",
			input: `<user_input mode="plan"><mode_notice>Partial truncated notice without closing tag</user_input>`,
			want:  "<mode_notice>Partial truncated notice without closing tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanClinePrompt(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseClineSession_Full(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000000_abcde"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"version": 1,
		"session_id": "1789000000000_abcde",
		"source": "cli",
		"started_at": "2026-09-10T10:00:00.000Z",
		"ended_at": "2026-09-10T10:05:00.000Z",
		"exit_code": 0,
		"status": "completed",
		"model": "deepseek/deepseek-v4-flash",
		"cwd": "/workspace/astro-spectrometer",
		"prompt": "<user_input mode=\"act\">Calibrate orbital telescope spectrometer frequency</user_input>",
		"metadata": {
			"title": "Calibrate orbital telescope spectrometer frequency",
			"totalCost": 0.015,
			"git": {
				"branch": "instruments/optics"
			},
			"usage": {
				"inputTokens": 20000,
				"outputTokens": 500,
				"cacheReadTokens": 15000,
				"cacheWriteTokens": 0,
				"totalCost": 0.015
			}
		}
	}`
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "msg_001",
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "<user_input mode=\"act\">Calibrate orbital telescope spectrometer frequency</user_input>"
					}
				],
				"ts": 1789034400000
			},
			{
				"id": "msg_002",
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "I should read the optics definition and run tests."
					},
					{
						"type": "text",
						"text": "I will inspect the optics instrumentation files."
					},
					{
						"type": "tool_use",
						"id": "call_001",
						"name": "read_files",
						"input": {"files": [{"path": "instruments/optics.py"}]}
					},
					{
						"type": "tool_use",
						"id": "call_002",
						"name": "Skill",
						"input": {"skill": "stellar-cartography"}
					}
				],
				"ts": 1789034402000,
				"modelInfo": {
					"id": "deepseek/deepseek-v4-flash"
				},
				"metrics": {
					"inputTokens": 5000,
					"outputTokens": 200,
					"cacheReadTokens": 4000,
					"cacheWriteTokens": 0
				}
			},
			{
				"id": "msg_003",
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_001",
						"name": "read_files",
						"content": [
							{
								"query": "instruments/optics.py",
								"result": "class Spectrometer:\n    def calibrate_frequency(self): return 432.0",
								"success": true
							}
						]
					},
					{
						"type": "tool_result",
						"tool_use_id": "call_002",
						"name": "Skill",
						"content": "Skill loaded successfully."
					}
				],
				"ts": 1789034403000
			},
			{
				"id": "msg_004",
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "Spectrometer optics calibrated to 432.0nm and telemetry frequency is locked."
					}
				],
				"ts": 1789034405000,
				"metrics": {
					"inputTokens": 6000,
					"outputTokens": 100,
					"cacheReadTokens": 5000,
					"cacheWriteTokens": 0
				}
			}
		]
	}`
	messagesPath := filepath.Join(taskDir, sessionID+".messages.json")
	require.NoError(t, os.WriteFile(messagesPath, []byte(messagesJSON), 0o644))

	sess, msgs, err := parseClineSession(metaPath, "astro-spectrometer", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, "cline:"+sessionID, sess.ID)
	assert.Equal(t, AgentCline, sess.Agent)
	assert.Equal(t, "Calibrate orbital telescope spectrometer frequency", sess.FirstMessage)
	assert.Equal(t, "Calibrate orbital telescope spectrometer frequency", sess.SessionName)
	assert.Equal(t, "astro_spectrometer", sess.Project)
	assert.Equal(t, "/workspace/astro-spectrometer", sess.Cwd)
	assert.Equal(t, "instruments/optics", sess.GitBranch)
	assert.Equal(t, TerminationClean, sess.TerminationStatus)
	assert.Equal(t, 5, sess.MessageCount)
	assert.Equal(t, 1, sess.UserMessageCount)

	// Peak context tokens = max(5000+4000, 6000+5000) = 11000
	assert.True(t, sess.HasPeakContextTokens)
	assert.Equal(t, 11000, sess.PeakContextTokens)

	// Total output tokens from usage (capped/extended by metadata usage)
	assert.True(t, sess.HasTotalOutputTokens)
	assert.Equal(t, 500, sess.TotalOutputTokens)

	// Reported totalCost is preserved on an authoritative usage event with zero tokens
	// to avoid double-counting per-message token usage.
	require.Len(t, sess.UsageEvents, 1)
	require.NotNil(t, sess.UsageEvents[0].Cost)
	assert.Equal(t, money.MustParseDollars("0.015"), *sess.UsageEvents[0].Cost)
	assert.Zero(t, sess.UsageEvents[0].InputTokens)
	assert.Zero(t, sess.UsageEvents[0].OutputTokens)
	assert.Zero(t, sess.UsageEvents[0].CacheReadInputTokens)
	assert.Zero(t, sess.UsageEvents[0].CacheCreationInputTokens)

	// Messages checks
	require.Len(t, msgs, 5)

	// msg 0: user prompt
	assert.Equal(t, RoleUser, msgs[0].Role)
	assert.False(t, msgs[0].IsSystem)
	assert.Equal(t, "Calibrate orbital telescope spectrometer frequency", msgs[0].Content)

	// msg 1: assistant thinking message (separated from tool calls)
	assert.Equal(t, RoleAssistant, msgs[1].Role)
	assert.True(t, msgs[1].HasThinking)
	assert.False(t, msgs[1].HasToolUse)
	assert.Equal(t, "I should read the optics definition and run tests.", msgs[1].ThinkingText)
	assert.Equal(t, "[Thinking]\nI should read the optics definition and run tests.\n[/Thinking]", msgs[1].Content)

	// msg 2: assistant with tool calls and text
	assert.Equal(t, RoleAssistant, msgs[2].Role)
	assert.False(t, msgs[2].HasThinking)
	assert.True(t, msgs[2].HasToolUse)
	assert.Equal(t, "I will inspect the optics instrumentation files.", msgs[2].Content)
	require.Len(t, msgs[2].ToolCalls, 2)

	tc0 := msgs[2].ToolCalls[0]
	assert.Equal(t, "call_001", tc0.ToolUseID)
	assert.Equal(t, "read_files", tc0.ToolName)
	assert.Equal(t, "Read", tc0.Category)
	require.Len(t, tc0.ResultEvents, 1)
	assert.Equal(t, "completed", tc0.ResultEvents[0].Status)
	assert.Contains(t, tc0.ResultEvents[0].Content, "class Spectrometer")

	tc1 := msgs[2].ToolCalls[1]
	assert.Equal(t, "call_002", tc1.ToolUseID)
	assert.Equal(t, "Skill", tc1.ToolName)
	assert.Equal(t, "Tool", tc1.Category)
	assert.Equal(t, "stellar-cartography", tc1.SkillName)
	require.Len(t, tc1.ResultEvents, 1)
	assert.Equal(t, "completed", tc1.ResultEvents[0].Status)
	assert.Equal(t, "Skill loaded successfully.", tc1.ResultEvents[0].Content)

	require.NotEmpty(t, msgs[2].TokenUsage)
	assert.JSONEq(t, `{"input_tokens":5000,"output_tokens":200,"cache_read_input_tokens":4000,"cache_creation_input_tokens":0}`, string(msgs[2].TokenUsage))
	assert.True(t, msgs[2].HasOutputTokens)
	assert.Equal(t, 200, msgs[2].OutputTokens)
	assert.True(t, msgs[2].HasContextTokens)
	assert.Equal(t, 9000, msgs[2].ContextTokens)

	// msg 3: tool results only -> system flag and subtype
	assert.Equal(t, RoleUser, msgs[3].Role)
	assert.True(t, msgs[3].IsSystem)
	assert.Equal(t, SourceSubtypeToolResult, msgs[3].SourceSubtype)
	require.Len(t, msgs[3].ToolResults, 2)
	assert.Equal(t, "call_001", msgs[3].ToolResults[0].ToolUseID)
	assert.Contains(t, DecodeContent(msgs[3].ToolResults[0].ContentRaw), "class Spectrometer")
	assert.Equal(t, len(tc0.ResultEvents[0].Content), msgs[3].ToolResults[0].ContentLength)

	// msg 4: final assistant text
	assert.Equal(t, RoleAssistant, msgs[4].Role)
	assert.Equal(t, "Spectrometer optics calibrated to 432.0nm and telemetry frequency is locked.", msgs[4].Content)
	require.NotEmpty(t, msgs[4].TokenUsage)
	assert.JSONEq(t, `{"input_tokens":6000,"output_tokens":100,"cache_read_input_tokens":5000,"cache_creation_input_tokens":0}`, string(msgs[4].TokenUsage))
	assert.True(t, msgs[4].HasOutputTokens)
	assert.Equal(t, 100, msgs[4].OutputTokens)
	assert.True(t, msgs[4].HasContextTokens)
	assert.Equal(t, 11000, msgs[4].ContextTokens)
}

func TestParseClineSession_FallbackUsageEvent(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000099_fallback"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"version": 1,
		"session_id": "1789000000099_fallback",
		"started_at": "2026-09-10T10:00:00.000Z",
		"ended_at": "2026-09-10T10:05:00.000Z",
		"status": "completed",
		"model": "deepseek/deepseek-v4-flash",
		"metadata": {
			"totalCost": 0.015,
			"usage": {
				"inputTokens": 20000,
				"outputTokens": 500,
				"cacheReadTokens": 15000,
				"cacheWriteTokens": 0,
				"totalCost": 0.015
			}
		}
	}`
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	// No messages file exists; parser should emit fallback aggregate usage event.
	sess, msgs, err := parseClineSession(metaPath, "test-proj", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Empty(t, msgs)

	require.Len(t, sess.UsageEvents, 1)
	ue := sess.UsageEvents[0]
	assert.Equal(t, "cline:"+sessionID, ue.SessionID)
	assert.Equal(t, "deepseek/deepseek-v4-flash", ue.Model)
	assert.Equal(t, 20000, ue.InputTokens)
	assert.Equal(t, 500, ue.OutputTokens)
	assert.Equal(t, 15000, ue.CacheReadInputTokens)
	require.NotNil(t, ue.Cost)
	assert.Equal(t, money.MustParseDollars("0.015"), *ue.Cost)
}

func TestParseClineSession_CommandError(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000001_err"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000001_err",
		"status": "failed",
		"prompt": "build hydrophone telemetry"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "build hydrophone telemetry"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_cmd",
						"name": "execute_command",
						"input": {"commands": ["make -C firmware build"]}
					}
				],
				"ts": 2000
			},
			{
				"id": "m3",
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_cmd",
						"name": "execute_command",
						"content": [
							{
								"query": "make -C firmware build",
								"result": "exit code 1: missing hydrophone header",
								"error": "Command exited with code 1",
								"success": false
							}
						]
					}
				],
				"ts": 3000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, msgs, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Since status was "failed", termination status is Truncated
	assert.Equal(t, TerminationTruncated, sess.TerminationStatus)
	require.Len(t, msgs, 3)

	tc := msgs[1].ToolCalls[0]
	assert.Equal(t, "Bash", tc.Category)
	require.Len(t, tc.ResultEvents, 1)
	assert.Equal(t, "errored", tc.ResultEvents[0].Status)
	assert.Contains(t, tc.ResultEvents[0].Content, "missing hydrophone header")
}

func TestParseClineSession_OrphanedToolCall(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000002_orphan"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000002_orphan",
		"status": "completed",
		"prompt": "tune hydrophone filter"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	// Tool call with no tool_result
	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "tune hydrophone filter"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_edit",
						"name": "edit_file",
						"input": {"path": "firmware/filter.c"}
					}
				],
				"ts": 2000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, _, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Tool call pending detected despite status="completed" in metadata
	assert.Equal(t, TerminationToolCallPending, sess.TerminationStatus)
}

func TestParseClineSession_AttemptCompletionEnding(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000007_complete"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000007_complete",
		"status": "completed",
		"prompt": "build filter"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "build filter"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "Filter has been built successfully."
					},
					{
						"type": "tool_use",
						"id": "call_done",
						"name": "attempt_completion",
						"input": {"result": "Built successfully"}
					}
				],
				"ts": 2000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, _, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, TerminationClean, sess.TerminationStatus)
}

func TestParseClineSession_AttemptCompletionWithUnresolvedPrecedingToolCall(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000008_unresolved"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000008_unresolved",
		"status": "completed",
		"prompt": "run command"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "run command"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_cmd",
						"name": "execute_command",
						"input": {"command": "go test ./..."}
					},
					{
						"type": "tool_use",
						"id": "call_done",
						"name": "attempt_completion",
						"input": {"result": "Done"}
					}
				],
				"ts": 2000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, _, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Preceding unresolved tool call flags termination as pending despite attempt_completion
	assert.Equal(t, TerminationToolCallPending, sess.TerminationStatus)
}

func TestParseClineSession_AttemptCompletionWithResolvedPrecedingToolCall(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000009_resolved"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000009_resolved",
		"status": "completed",
		"prompt": "run command"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "run command"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_cmd",
						"name": "execute_command",
						"input": {"command": "go test ./..."}
					}
				],
				"ts": 2000
			},
			{
				"id": "m3",
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_cmd",
						"content": "ok"
					}
				],
				"ts": 3000
			},
			{
				"id": "m4",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_done",
						"name": "attempt_completion",
						"input": {"result": "Done"}
					}
				],
				"ts": 4000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, _, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// All preceding tool calls resolved, attempt_completion marks session clean
	assert.Equal(t, TerminationClean, sess.TerminationStatus)
}

func TestParseClineSession_ThinkingContentInlining(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000010_thinking_inline"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000010_thinking_inline",
		"status": "completed",
		"prompt": "explain this"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "explain this"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "Let me think about how to explain this."
					},
					{
						"type": "text",
						"text": "Here is the explanation."
					}
				],
				"ts": 2000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, messages, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, messages, 2)

	asst := messages[1]
	assert.True(t, asst.HasThinking)
	assert.Equal(t, "Let me think about how to explain this.", asst.ThinkingText)
	assert.Equal(t, "[Thinking]\nLet me think about how to explain this.\n[/Thinking]\n\nHere is the explanation.", asst.Content)
}

func TestParseClineSession_ThinkingOnlyEnding(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000003_thinking"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000003_thinking",
		"status": "completed",
		"prompt": "think about it"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "think about it"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "Still thinking..."
					}
				],
				"ts": 2000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, messages, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Interrupted mid-thought detected
	assert.Equal(t, TerminationToolCallPending, sess.TerminationStatus)
	assert.Equal(t, "[Thinking]\nStill thinking...\n[/Thinking]", messages[1].Content)
	assert.Equal(t, "Still thinking...", messages[1].ThinkingText)
	assert.True(t, messages[1].HasThinking)
}

func TestParseClineSession_ThinkingAndToolSeparated(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000005_thinking_tool"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000005_thinking_tool",
		"status": "completed",
		"prompt": "run status check"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "m1",
				"role": "user",
				"content": [{"type": "text", "text": "run status check"}],
				"ts": 1000
			},
			{
				"id": "m2",
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "Checking repository state before executing tools."
					},
					{
						"type": "tool_use",
						"id": "call_status_1",
						"name": "run_commands",
						"input": {"command": "git status"}
					}
				],
				"ts": 2000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, messages, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Thinking and tool call are separated into two distinct messages
	require.Len(t, messages, 3)
	assert.Equal(t, 3, sess.MessageCount)

	// Message 1: Thinking block only
	assert.Equal(t, RoleAssistant, messages[1].Role)
	assert.True(t, messages[1].HasThinking)
	assert.False(t, messages[1].HasToolUse)
	assert.Equal(t, "Checking repository state before executing tools.", messages[1].ThinkingText)
	assert.Equal(t, "[Thinking]\nChecking repository state before executing tools.\n[/Thinking]", messages[1].Content)
	assert.Equal(t, "m2:thinking", messages[1].SourceUUID)

	// Message 2: Tool call block (content is empty, ready for tool-group rendering)
	assert.Equal(t, RoleAssistant, messages[2].Role)
	assert.False(t, messages[2].HasThinking)
	assert.True(t, messages[2].HasToolUse)
	assert.Empty(t, messages[2].Content)
	assert.Equal(t, "m2", messages[2].SourceUUID)
	require.Len(t, messages[2].ToolCalls, 1)
	assert.Equal(t, "call_status_1", messages[2].ToolCalls[0].ToolUseID)
	assert.Equal(t, "run_commands", messages[2].ToolCalls[0].ToolName)
}

func TestParseClineSession_EmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000004_empty"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000004_empty",
		"started_at": "2026-09-10T12:00:00.000Z",
		"prompt": "hello"
	}`
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	sess, msgs, err := parseClineSession(metaPath, "test-proj", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Empty(t, msgs)
	assert.Equal(t, "hello", sess.SessionName)
	assert.Equal(t, "hello", sess.FirstMessage)
}

func TestParseClineSession_ZeroCostAuthoritative(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000005_zerocost"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	zeroVal := jsontext.Value("0")
	meta := clineSessionMetadata{
		SessionID: sessionID,
		Model:     "ollama/llama3",
		Metadata: clineMetadataObj{
			TotalCost: &zeroVal,
			Usage: &clineUsageObj{
				InputTokens:  1000,
				OutputTokens: 200,
				TotalCost:    &zeroVal,
			},
		},
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, data, 0o644))

	sess, _, err := parseClineSession(metaPath, "", "")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.UsageEvents, 1)
	require.NotNil(t, sess.UsageEvents[0].Cost)
	assert.Equal(t, money.MustParseDollars("0"), *sess.UsageEvents[0].Cost)
}

func TestParseClineSession_MessageMetricsZeroCostAuthoritative(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000006_zerocost_msgs"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	zeroVal := jsontext.Value("0")
	meta := clineSessionMetadata{
		SessionID: sessionID,
		Model:     "ollama/llama3",
		Metadata: clineMetadataObj{
			TotalCost: &zeroVal,
			Usage: &clineUsageObj{
				InputTokens:  1000,
				OutputTokens: 200,
				TotalCost:    &zeroVal,
			},
		},
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, data, 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "msg_001",
				"role": "user",
				"content": [{"type": "text", "text": "hello"}],
				"ts": 1789034400000
			},
			{
				"id": "msg_002",
				"role": "assistant",
				"content": [{"type": "text", "text": "world"}],
				"ts": 1789034402000,
				"metrics": {
					"inputTokens": 1000,
					"outputTokens": 200
				}
			}
		]
	}`
	messagesPath := filepath.Join(taskDir, sessionID+".messages.json")
	require.NoError(t, os.WriteFile(messagesPath, []byte(messagesJSON), 0o644))

	sess, msgs, err := parseClineSession(metaPath, "test-proj", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, msgs, 2)
	assert.Equal(t, 200, sess.TotalOutputTokens)

	// Authoritative zero cost must be preserved on usage event with 0 tokens
	require.Len(t, sess.UsageEvents, 1)
	require.NotNil(t, sess.UsageEvents[0].Cost)
	assert.Equal(t, money.MustParseDollars("0"), *sess.UsageEvents[0].Cost)
	assert.Zero(t, sess.UsageEvents[0].InputTokens)
	assert.Zero(t, sess.UsageEvents[0].OutputTokens)
	assert.Zero(t, sess.UsageEvents[0].CacheReadInputTokens)
	assert.Zero(t, sess.UsageEvents[0].CacheCreationInputTokens)
}

func TestParseClineSession_ContentBlockToolResults(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000088_content_blocks"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000088_content_blocks",
		"started_at": "2026-09-10T10:00:00Z",
		"status": "completed"
	}`
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"role": "user",
				"ts": 1789000000000,
				"content": [
					{
						"type": "text",
						"text": "Run inspection"
					}
				]
			},
			{
				"role": "assistant",
				"ts": 1789000001000,
				"content": [
					{
						"type": "tool_use",
						"id": "call_blk_1",
						"name": "read_files",
						"input": {"paths": ["main.go"]}
					}
				]
			},
			{
				"role": "user",
				"ts": 1789000002000,
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_blk_1",
						"content": [
							{"type": "text", "text": "package main\n\nfunc main() {}"}
						]
					}
				]
			}
		]
	}`
	messagesPath := filepath.Join(taskDir, sessionID+".messages.json")
	require.NoError(t, os.WriteFile(messagesPath, []byte(messagesJSON), 0o644))

	sess, msgs, err := parseClineSession(metaPath, "test-proj", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, msgs, 3)

	// Verify tool call was paired with decoded result
	require.Len(t, msgs[1].ToolCalls, 1)
	tc := msgs[1].ToolCalls[0]
	assert.Equal(t, "call_blk_1", tc.ToolUseID)
	require.Len(t, tc.ResultEvents, 1)
	assert.Equal(t, "completed", tc.ResultEvents[0].Status)
	assert.Equal(t, "package main\n\nfunc main() {}", tc.ResultEvents[0].Content)

	// Verify tool result preserved raw JSON in ContentRaw and can be decoded
	require.Len(t, msgs[2].ToolResults, 1)
	tr := msgs[2].ToolResults[0]
	assert.Equal(t, "call_blk_1", tr.ToolUseID)
	assert.Equal(t, len("package main\n\nfunc main() {}"), tr.ContentLength)
	assert.JSONEq(t, `[{"type": "text", "text": "package main\n\nfunc main() {}"}]`, tr.ContentRaw)
	assert.Equal(t, "package main\n\nfunc main() {}", DecodeContent(tr.ContentRaw))
}

func TestParseClineSession_ProviderPropagation(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000001_provider"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"version": 1,
		"session_id": "1789000000001_provider",
		"provider": "openrouter",
		"model": "deepseek-chat",
		"started_at": "2026-09-10T10:00:00.000Z",
		"ended_at": "2026-09-10T10:05:00.000Z",
		"metadata": {
			"totalCost": 0.05,
			"usage": {
				"inputTokens": 1000,
				"outputTokens": 200,
				"totalCost": 0.05
			}
		}
	}`
	metaPath := filepath.Join(taskDir, sessionID+".json")
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	messagesJSON := `{
		"messages": [
			{
				"id": "msg_001",
				"role": "user",
				"content": [{"type": "text", "text": "Hello"}],
				"ts": 1789034400000
			},
			{
				"id": "msg_002",
				"role": "assistant",
				"content": [{"type": "text", "text": "Hi there"}],
				"ts": 1789034401000
			},
			{
				"id": "msg_003",
				"role": "assistant",
				"modelInfo": {
					"id": "claude-3-5-sonnet",
					"provider": "anthropic"
				},
				"content": [{"type": "text", "text": "Switched model"}],
				"ts": 1789034402000
			}
		]
	}`
	messagesPath := filepath.Join(taskDir, sessionID+".messages.json")
	require.NoError(t, os.WriteFile(messagesPath, []byte(messagesJSON), 0o644))

	sess, msgs, err := parseClineSession(metaPath, "test-proj", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, msgs, 3)

	// User message should have empty ProviderID
	assert.Empty(t, msgs[0].ProviderID)

	// First assistant message should inherit session-level provider ("openrouter")
	assert.Equal(t, "openrouter", msgs[1].ProviderID)
	assert.Equal(t, "deepseek-chat", msgs[1].Model)

	// Second assistant message has per-message modelInfo overriding provider to "anthropic"
	assert.Equal(t, "anthropic", msgs[2].ProviderID)
	assert.Equal(t, "claude-3-5-sonnet", msgs[2].Model)

	// Aggregate usage event should carry session-level provider ("openrouter")
	require.NotEmpty(t, sess.UsageEvents)
	assert.Equal(t, "openrouter", sess.UsageEvents[0].ProviderID)
	assert.Equal(t, "deepseek-chat", sess.UsageEvents[0].Model)
}

func TestParseClineSession_CanonicalSessionIDValidation(t *testing.T) {
	dir := t.TempDir()

	// Case 1: Matching metadata session_id succeeds
	sess1Dir := filepath.Join(dir, "sess-canonical-1")
	require.NoError(t, os.MkdirAll(sess1Dir, 0o755))
	meta1 := filepath.Join(sess1Dir, "sess-canonical-1.json")
	require.NoError(t, os.WriteFile(meta1, []byte(`{"session_id":"sess-canonical-1"}`), 0o644))
	sess1, _, err := parseClineSession(meta1, "proj", "local")
	require.NoError(t, err)
	assert.Equal(t, "cline:sess-canonical-1", sess1.ID)

	// Case 2: Omitted metadata session_id falls back to canonical ID
	sess2Dir := filepath.Join(dir, "sess-canonical-2")
	require.NoError(t, os.MkdirAll(sess2Dir, 0o755))
	meta2 := filepath.Join(sess2Dir, "sess-canonical-2.json")
	require.NoError(t, os.WriteFile(meta2, []byte(`{}`), 0o644))
	sess2, _, err := parseClineSession(meta2, "proj", "local")
	require.NoError(t, err)
	assert.Equal(t, "cline:sess-canonical-2", sess2.ID)

	// Case 3: Mismatch between metadata session_id and directory name is rejected
	sess3Dir := filepath.Join(dir, "sess-canonical-3")
	require.NoError(t, os.MkdirAll(sess3Dir, 0o755))
	meta3 := filepath.Join(sess3Dir, "sess-canonical-3.json")
	require.NoError(t, os.WriteFile(meta3, []byte(`{"session_id":"stale-or-evil-id"}`), 0o644))
	_, _, err = parseClineSession(meta3, "proj", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match canonical id")

	// Case 4: Mismatch between filename and directory name is rejected
	sess4Dir := filepath.Join(dir, "sess-canonical-4")
	require.NoError(t, os.MkdirAll(sess4Dir, 0o755))
	meta4 := filepath.Join(sess4Dir, "wrong-name.json")
	require.NoError(t, os.WriteFile(meta4, []byte(`{"session_id":"sess-canonical-4"}`), 0o644))
	_, _, err = parseClineSession(meta4, "proj", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match directory")

	// Case 5: Malformed directory name (e.g. underscore prefix) is rejected
	sess5Dir := filepath.Join(dir, "_invalid_sess")
	require.NoError(t, os.MkdirAll(sess5Dir, 0o755))
	meta5 := filepath.Join(sess5Dir, "_invalid_sess.json")
	require.NoError(t, os.WriteFile(meta5, []byte(`{"session_id":"_invalid_sess"}`), 0o644))
	_, _, err = parseClineSession(meta5, "proj", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cline session directory name")
}

func TestIsClineTeammateMessagesFile(t *testing.T) {
	sessionID := "1789000000000_mocksess"
	tests := []struct {
		filename string
		want     bool
	}{
		{"worker-scout__sub1abc.messages.json", true},
		{"diff-checker__sub2xyz.messages.json", true},
		{"worker__task1.messages.json", true},
		{"my_agent__123.messages.json", true},
		{"1789000000000_mocksess.messages.json", false},
		{"1789000000000_mocksess.json", false},
		{"other.json", false},
		{"worker-scout.messages.json", false},
		{"__sub1abc.messages.json", false},
		{"worker-scout__.messages.json", false},
		{"foo__bar__.messages.json", false},
		{".hidden__t1.messages.json", false},
		{"_agent__t1.messages.json", false},
		{"../evil__t1.messages.json", false},
		{"foo/bar__t1.messages.json", false},
		{"foo\\bar__t1.messages.json", false},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			assert.Equal(t, tt.want, IsClineTeammateMessagesFile(sessionID, tt.filename))
		})
	}
}

func TestParseClineSession_TeammateSubagents(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-parent")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{
		"session_id": "sess-parent",
		"started_at": "2026-09-10T10:00:00Z",
		"cwd": "/workspace/teamproject",
		"provider": "anthropic",
		"model": "claude-3-5-sonnet",
		"metadata": {
			"title": "Lead Coordinator",
			"git": { "branch": "feat/team-feature" }
		}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-parent.json"), []byte(metaJSON), 0o644))

	parentMsgsJSON := `{
		"version": 1,
		"agent": "lead",
		"sessionId": "sess-parent",
		"messages": [
			{
				"id": "msg_user_1",
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Spawn git-scout to inspect repository status"
					}
				],
				"ts": 1789207950000
			},
			{
				"id": "msg_asst_1",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_spawn_scout",
						"name": "team_spawn_teammate",
						"input": {
							"agentId": "git-scout",
							"rolePrompt": "You are a git recon specialist."
						}
					}
				],
				"ts": 1789207960000
			},
			{
				"id": "msg_user_2",
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_spawn_scout",
						"content": "{\"agentId\":\"git-scout\",\"status\":\"spawned\"}"
					}
				],
				"ts": 1789207961000
			},
			{
				"id": "msg_asst_2",
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_run_scout",
						"name": "team_run_task",
						"input": {
							"agentId": "git-scout",
							"taskId": "task_0001",
							"runMode": "sync",
							"task": "Collect git status. taskId: task_0001"
						}
					}
				],
				"ts": 1789207970000
			},
			{
				"id": "msg_user_3",
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_run_scout",
						"content": "{\"agentId\":\"git-scout\",\"status\":\"completed\"}"
					}
				],
				"ts": 1789207980000
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-parent.messages.json"), []byte(parentMsgsJSON), 0o644))

	teammateMsgsJSON := `{
		"version": 1,
		"updated_at": "2026-09-10T10:05:00Z",
		"agent": "teammate",
		"sessionId": "sess-parent__teamtask__git-scout__t1abc",
		"taskType": "team",
		"origin": {
			"source": "cli",
			"mode": "team",
			"sessionId": "sess-parent__teamtask__git-scout__t1abc",
			"parentThreadId": "sess-parent",
			"subagent": "git-scout",
			"version": "3.0.61"
		},
		"messages": [
			{
				"id": "msg_sub_user_1",
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Collect git status. taskId: task_0001"
					}
				],
				"ts": 1789207971000
			},
			{
				"id": "msg_sub_asst_1",
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "Executing git commands"
					},
					{
						"type": "tool_use",
						"id": "call_sub_cmd_1",
						"name": "run_commands",
						"input": {
							"commands": ["git status --short", "git branch"]
						}
					}
				],
				"ts": 1789207975000,
				"modelInfo": {
					"id": "claude-3-5-haiku",
					"provider": "anthropic"
				},
				"metrics": {
					"inputTokens": 200,
					"outputTokens": 80,
					"cacheReadTokens": 50,
					"cacheWriteTokens": 0
				}
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "git-scout__t1abc.messages.json"), []byte(teammateMsgsJSON), 0o644))

	metaPath := filepath.Join(sessDir, "sess-parent.json")

	// 1. Test parseClineSessionWithTeammates directly
	results, err := parseClineSessionWithTeammates(metaPath, "teamproject", "local", nil)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Verify Parent
	parent := results[0]
	assert.Equal(t, "cline:sess-parent", parent.Session.ID)
	assert.Empty(t, parent.Session.ParentSessionID)
	assert.Equal(t, RelNone, parent.Session.RelationshipType)
	assert.Equal(t, "Lead Coordinator", parent.Session.SessionName)

	// Verify Tool Calls in Parent are annotated with child SubagentSessionID
	require.Len(t, parent.Messages, 5)
	spawnCall := parent.Messages[1].ToolCalls[0]
	assert.Empty(t, spawnCall.SubagentSessionID)
	assert.Equal(t, "Task", spawnCall.Category)

	runCall := parent.Messages[3].ToolCalls[0]
	assert.Equal(t, "cline:sess-parent__teammate__git-scout", runCall.SubagentSessionID)
	assert.Equal(t, "Task", runCall.Category)
	require.Len(t, runCall.ResultEvents, 1)
	assert.Equal(t, "cline:sess-parent__teammate__git-scout", runCall.ResultEvents[0].SubagentSessionID)
	assert.Equal(t, "git-scout", runCall.ResultEvents[0].AgentID)

	// Verify Child Subagent
	child := results[1]
	assert.Equal(t, "cline:sess-parent__teammate__git-scout", child.Session.ID)
	assert.Equal(t, "sess-parent__teamtask__git-scout__t1abc", child.Session.SourceSessionID)
	assert.Equal(t, "cline:sess-parent", child.Session.ParentSessionID)
	assert.Equal(t, RelSubagent, child.Session.RelationshipType)
	assert.Equal(t, "Teammate: git-scout", child.Session.SessionName)
	assert.Equal(t, "feat/team-feature", child.Session.GitBranch)
	assert.Equal(t, "teamproject", child.Session.Project)
	assert.Equal(t, "/workspace/teamproject", child.Session.Cwd)
	assert.Equal(t, AgentCline, child.Session.Agent)
	assert.Equal(t, 3, child.Session.MessageCount)
	assert.Equal(t, 1, child.Session.UserMessageCount)
	assert.Equal(t, "Collect git status. taskId: task_0001", child.Session.FirstMessage)
	assert.Equal(t, 80, child.Session.TotalOutputTokens)
	assert.Equal(t, 250, child.Session.PeakContextTokens)

	// Verify child messages
	require.Len(t, child.Messages, 3)
	assert.True(t, child.Messages[1].HasThinking)
	assert.False(t, child.Messages[1].HasToolUse)
	assert.Equal(t, "Executing git commands", child.Messages[1].ThinkingText)
	assert.False(t, child.Messages[2].HasThinking)
	assert.True(t, child.Messages[2].HasToolUse)
	require.Len(t, child.Messages[2].ToolCalls, 1)
	assert.Equal(t, "run_commands", child.Messages[2].ToolCalls[0].ToolName)
	assert.Equal(t, "Bash", child.Messages[2].ToolCalls[0].Category)

	// 2. Verify parseClineSession (backwards compatibility) returns parent with annotated tool calls
	sess, msgs, err := parseClineSession(metaPath, "teamproject", "local")
	require.NoError(t, err)
	assert.Equal(t, "cline:sess-parent", sess.ID)
	assert.Empty(t, msgs[1].ToolCalls[0].SubagentSessionID)
	assert.Equal(t, "cline:sess-parent__teammate__git-scout", msgs[3].ToolCalls[0].SubagentSessionID)
}

func TestParseClineTeammates_ContinuationCoalescing(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-cont")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{
		"session_id": "sess-cont",
		"started_at": "2026-09-12T10:00:00Z",
		"cwd": "/workspace",
		"provider": "anthropic",
		"model": "claude-3-5-sonnet"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-cont.json"), []byte(metaJSON), 0o644))

	// Older snapshot: 2 messages, 80 output tokens
	olderJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T15:44:47.850Z",
		"agent": "teammate",
		"sessionId": "sess-cont__teamtask__path-guard__60SKaJ",
		"taskType": "team",
		"origin": {
			"source": "cli",
			"mode": "team",
			"sessionId": "sess-cont__teamtask__path-guard__60SKaJ",
			"parentThreadId": "sess-cont",
			"subagent": "path-guard",
			"version": "3.0.61"
		},
		"messages": [
			{
				"id": "msg_1",
				"role": "user",
				"content": [{"type": "text", "text": "Initial task prompt"}],
				"ts": 1789227695000
			},
			{
				"id": "msg_2",
				"role": "assistant",
				"content": [{"type": "text", "text": "First response"}],
				"ts": 1789227700000,
				"metrics": {
					"inputTokens": 100,
					"outputTokens": 80
				}
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "path-guard__60SKaJ.messages.json"), []byte(olderJSON), 0o644))

	// Newer continuation snapshot: 4 messages, exact prefix superset of older, 120 output tokens
	newerJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T15:51:50.696Z",
		"agent": "teammate",
		"sessionId": "sess-cont__teamtask__path-guard__8saA60",
		"taskType": "team",
		"origin": {
			"source": "cli",
			"mode": "team",
			"sessionId": "sess-cont__teamtask__path-guard__8saA60",
			"parentThreadId": "sess-cont",
			"subagent": "path-guard",
			"version": "3.0.61"
		},
		"messages": [
			{
				"id": "msg_1",
				"role": "user",
				"content": [{"type": "text", "text": "Initial task prompt"}],
				"ts": 1789227695000
			},
			{
				"id": "msg_2",
				"role": "assistant",
				"content": [{"type": "text", "text": "First response"}],
				"ts": 1789227700000,
				"metrics": {
					"inputTokens": 100,
					"outputTokens": 80
				}
			},
			{
				"id": "msg_3",
				"role": "user",
				"content": [{"type": "text", "text": "Continue task"}],
				"ts": 1789227800000
			},
			{
				"id": "msg_4",
				"role": "assistant",
				"content": [{"type": "text", "text": "Final response"}],
				"ts": 1789227850000,
				"metrics": {
					"inputTokens": 150,
					"outputTokens": 120
				}
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "path-guard__8saA60.messages.json"), []byte(newerJSON), 0o644))

	results, err := parseClineSessionWithTeammates(filepath.Join(sessDir, "sess-cont.json"), "proj", "local", nil)
	require.NoError(t, err)
	// 1 parent + 1 subagent (coalesced from 2 files)
	require.Len(t, results, 2)

	sub := results[1]
	assert.Equal(t, "cline:sess-cont__teammate__path-guard", sub.Session.ID)
	assert.Equal(t, "sess-cont__teamtask__path-guard__8saA60", sub.Session.SourceSessionID)
	assert.Equal(t, "Teammate: path-guard", sub.Session.SessionName)
	assert.Equal(t, 4, sub.Session.MessageCount)
	assert.Equal(t, 2, sub.Session.UserMessageCount)

	// Usage must come from the winner file ONLY: 80 + 120 = 200, NOT 80 + 80 + 120 = 280 (double counted)
	assert.Equal(t, 200, sub.Session.TotalOutputTokens)

	// EndedAt must advance to the newer continuation's timestamp
	wantEndedAt, ok := parseClineTimestamp("2026-09-12T15:51:50.696Z")
	require.True(t, ok)
	assert.Equal(t, wantEndedAt.UTC(), sub.Session.EndedAt.UTC())
}

func TestParseClineTeammates_IdenticalSnapshots(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-ident")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{"session_id": "sess-ident", "cwd": "/workspace"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-ident.json"), []byte(metaJSON), 0o644))

	// Two snapshot files with identical message IDs but different update timestamps
	snap1 := `{
		"version": 1,
		"updated_at": "2026-09-12T15:00:00.000Z",
		"sessionId": "sess-ident__teamtask__scout__s1",
		"origin": {"subagent": "scout"},
		"messages": [{"id": "m1", "role": "user", "content": [{"type": "text", "text": "hi"}], "ts": 1000}]
	}`
	snap2 := `{
		"version": 1,
		"updated_at": "2026-09-12T15:05:00.000Z",
		"sessionId": "sess-ident__teamtask__scout__s2",
		"origin": {"subagent": "scout"},
		"messages": [{"id": "m1", "role": "user", "content": [{"type": "text", "text": "hi"}], "ts": 1000}]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "scout__s1.messages.json"), []byte(snap1), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "scout__s2.messages.json"), []byte(snap2), 0o644))

	results, err := parseClineSessionWithTeammates(filepath.Join(sessDir, "sess-ident.json"), "proj", "local", nil)
	require.NoError(t, err)
	// Exactly 1 subagent session produced
	require.Len(t, results, 2)
	assert.Equal(t, "cline:sess-ident__teammate__scout", results[1].Session.ID)
	// Tiebreaker picks the one with newer updatedAt
	assert.Equal(t, "sess-ident__teamtask__scout__s2", results[1].Session.SourceSessionID)
}

func TestParseClineTeammates_DistinctRunsAndForks(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-runs")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{"session_id": "sess-runs", "cwd": "/workspace"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-runs.json"), []byte(metaJSON), 0o644))

	// Run 1: initial task
	run1 := `{
		"version": 1,
		"updated_at": "2026-09-12T15:00:00.000Z",
		"sessionId": "sess-runs__teamtask__worker__r1",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "run1_msg1", "role": "user", "content": [{"type": "text", "text": "task 1"}], "ts": 1000},
			{"id": "run1_msg2", "role": "assistant", "content": [{"type": "text", "text": "done 1"}], "ts": 1100}
		]
	}`
	// Run 2: distinct run (continueConversation: false), totally different initial message ID
	run2 := `{
		"version": 1,
		"updated_at": "2026-09-12T16:00:00.000Z",
		"sessionId": "sess-runs__teamtask__worker__r2",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "run2_msg1", "role": "user", "content": [{"type": "text", "text": "task 2"}], "ts": 2000},
			{"id": "run2_msg2", "role": "assistant", "content": [{"type": "text", "text": "done 2"}], "ts": 2100}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "worker__r1.messages.json"), []byte(run1), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "worker__r2.messages.json"), []byte(run2), 0o644))

	results, err := parseClineSessionWithTeammates(filepath.Join(sessDir, "sess-runs.json"), "proj", "local", nil)
	require.NoError(t, err)
	// 1 parent + 2 distinct subagent runs
	require.Len(t, results, 3)

	// Identity derives from the chain root, not the winner ranking: run r1
	// sorts behind run r2 (later updatedAt) yet owns the bare form as the
	// oldest chain, while run r2 gets the stable root-derived suffix.
	bySource := clineTeammateResultBySource(results)
	r1Idx, foundR1 := bySource("sess-runs__teamtask__worker__r1")
	require.True(t, foundR1, "run r1 must be emitted")
	assert.Equal(t, "cline:sess-runs__teammate__worker", results[r1Idx].Session.ID)
	r2Idx, foundR2 := bySource("sess-runs__teamtask__worker__r2")
	require.True(t, foundR2, "run r2 must be emitted")
	assert.Equal(t,
		"cline:sess-runs__teammate__worker__rid-"+clineRIDDigestForTest(t, "sess-runs__teamtask__worker__r2"),
		results[r2Idx].Session.ID)
}

func TestParseClineTeammates_DivergentTailFork(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-fork")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{"session_id": "sess-fork", "cwd": "/workspace"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-fork.json"), []byte(metaJSON), 0o644))

	// Common prefix m1, m2, but fork at m3
	forkA := `{
		"version": 1,
		"updated_at": "2026-09-12T15:00:00.000Z",
		"sessionId": "sess-fork__teamtask__analyst__fa",
		"origin": {"subagent": "analyst"},
		"messages": [
			{"id": "m1", "role": "user", "content": [{"type": "text", "text": "task"}], "ts": 1000},
			{"id": "m2", "role": "assistant", "content": [{"type": "text", "text": "step"}], "ts": 1100},
			{"id": "m3_a", "role": "assistant", "content": [{"type": "text", "text": "branch A"}], "ts": 1200}
		]
	}`
	forkB := `{
		"version": 1,
		"updated_at": "2026-09-12T15:05:00.000Z",
		"sessionId": "sess-fork__teamtask__analyst__fb",
		"origin": {"subagent": "analyst"},
		"messages": [
			{"id": "m1", "role": "user", "content": [{"type": "text", "text": "task"}], "ts": 1000},
			{"id": "m2", "role": "assistant", "content": [{"type": "text", "text": "step"}], "ts": 1100},
			{"id": "m3_b", "role": "assistant", "content": [{"type": "text", "text": "branch B"}], "ts": 1250}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "analyst__fa.messages.json"), []byte(forkA), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "analyst__fb.messages.json"), []byte(forkB), 0o644))

	results, err := parseClineSessionWithTeammates(filepath.Join(sessDir, "sess-fork.json"), "proj", "local", nil)
	require.NoError(t, err)
	// Divergent tails are preserved as distinct runs rather than dropping data
	require.Len(t, results, 3)

	// Each fork roots its own chain, so each gets a stable root-derived ID:
	// fork fa owns the bare form (lexicographically smallest root session ID)
	// and fork fb gets the stable digest suffix.
	bySource := clineTeammateResultBySource(results)
	forkAIdx, foundA := bySource("sess-fork__teamtask__analyst__fa")
	require.True(t, foundA, "fork fa must be emitted")
	assert.Equal(t, "cline:sess-fork__teammate__analyst", results[forkAIdx].Session.ID)
	forkBIdx, foundB := bySource("sess-fork__teamtask__analyst__fb")
	require.True(t, foundB, "fork fb must be emitted")
	assert.Equal(t,
		"cline:sess-fork__teammate__analyst__rid-"+clineRIDDigestForTest(t, "sess-fork__teamtask__analyst__fb"),
		results[forkBIdx].Session.ID)
}

func TestParseClineTeammates_SafetyValidationAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-safe")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{"session_id": "sess-safe", "cwd": "/workspace"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-safe.json"), []byte(metaJSON), 0o644))

	// 1. Valid teammate
	validJSON := `{
		"version": 1,
		"origin": {"subagent": "good-scout"},
		"messages": [{"id": "m1", "role": "user", "content": [{"type": "text", "text": "ok"}], "ts": 1000}]
	}`
	validPath := filepath.Join(sessDir, "good-scout__ok.messages.json")
	require.NoError(t, os.WriteFile(validPath, []byte(validJSON), 0o644))

	// 2. Unsafe subagent name in origin: contains double underscore / __teammate__
	badTeammate := `{"version": 1, "origin": {"subagent": "agent__teammate__evil"}, "messages": [{"id": "m1", "role": "user", "content": [{"type": "text", "text": "bad"}], "ts": 1000}]}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "evil__1.messages.json"), []byte(badTeammate), 0o644))

	// 3. Unsafe subagent name in origin: starts with underscore
	badUnderscore := `{"version": 1, "origin": {"subagent": "_hidden"}, "messages": [{"id": "m1", "role": "user", "content": [{"type": "text", "text": "bad"}], "ts": 1000}]}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "hidden__1.messages.json"), []byte(badUnderscore), 0o644))

	// 4. Unsafe subagent name in origin: contains path separators
	badPath := `{"version": 1, "origin": {"subagent": "../traversal"}, "messages": [{"id": "m1", "role": "user", "content": [{"type": "text", "text": "bad"}], "ts": 1000}]}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "traversal__1.messages.json"), []byte(badPath), 0o644))

	// 5. Symlinked teammate file: must be skipped
	symlinkPath := filepath.Join(sessDir, "symlink__t1.messages.json")
	if err := os.Symlink(validPath, symlinkPath); err == nil {
		defer os.Remove(symlinkPath)
	}

	results, err := parseClineSessionWithTeammates(filepath.Join(sessDir, "sess-safe.json"), "proj", "local", nil)
	require.NoError(t, err)

	// Only parent + the one valid subagent should be returned. All invalid names and symlinks skipped.
	require.Len(t, results, 2)
	assert.Equal(t, "cline:sess-safe", results[0].Session.ID)
	assert.Equal(t, "cline:sess-safe__teammate__good-scout", results[1].Session.ID)
}

func TestParseClineTeammates_EmptyFileSuperseded(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-empty")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{"session_id": "sess-empty", "cwd": "/workspace"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "sess-empty.json"), []byte(metaJSON), 0o644))

	// Empty file with 0 messages
	emptyJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T15:00:00.000Z",
		"origin": {"subagent": "worker"},
		"messages": []
	}`
	// Non-empty file with 2 messages
	nonEmptyJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T15:05:00.000Z",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "m1", "role": "user", "content": [{"type": "text", "text": "start"}], "ts": 1000},
			{"id": "m2", "role": "assistant", "content": [{"type": "text", "text": "done"}], "ts": 1100}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "worker__e1.messages.json"), []byte(emptyJSON), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "worker__e2.messages.json"), []byte(nonEmptyJSON), 0o644))

	results, err := parseClineSessionWithTeammates(filepath.Join(sessDir, "sess-empty.json"), "proj", "local", nil)
	require.NoError(t, err)
	// Exactly 1 subagent session (non-empty supersedes empty)
	require.Len(t, results, 2)
	assert.Equal(t, "cline:sess-empty__teammate__worker", results[1].Session.ID)
	assert.Equal(t, 2, results[1].Session.MessageCount)
}

func TestParseClineSession_MultiTurnUserInputCleaning(t *testing.T) {
	dir := t.TempDir()
	sessionID := "1789000000001_multi"
	taskDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(taskDir, 0o755))

	metaJSON := `{
		"session_id": "1789000000001_multi",
		"prompt": "<user_input mode=\"act\">Initial prompt from user</user_input>",
		"cwd": "/workspace",
		"started_at": "2026-09-12T10:00:00.000Z"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".json"), []byte(metaJSON), 0o644))

	// Turn 0: User prompt in act mode
	// Turn 1: Assistant reply
	// Turn 2: User prompt in plan mode with mode_notice
	// Turn 3: Assistant reply
	// Turn 4: User empty input with act mode (approval) -> should be omitted
	// Turn 5: Assistant final reply
	messagesJSON := `{
		"version": 1,
		"messages": [
			{
				"id": "msg-0",
				"role": "user",
				"ts": 1789200000000,
				"content": [
					{"type": "text", "text": "<user_input mode=\"act\">Initial prompt from user</user_input>"}
				]
			},
			{
				"id": "msg-1",
				"role": "assistant",
				"ts": 1789200001000,
				"content": [
					{"type": "text", "text": "Understood, working on it."}
				]
			},
			{
				"id": "msg-2",
				"role": "user",
				"ts": 1789200002000,
				"content": [
					{"type": "text", "text": "<user_input mode=\"plan\"><mode_notice>The user switched from act mode to plan mode before sending this message.</mode_notice>\nNow investigate the architecture</user_input>"}
				]
			},
			{
				"id": "msg-3",
				"role": "assistant",
				"ts": 1789200003000,
				"content": [
					{"type": "text", "text": "Here is the architectural plan."}
				]
			},
			{
				"id": "msg-4",
				"role": "user",
				"ts": 1789200004000,
				"content": [
					{"type": "text", "text": "<user_input mode=\"act\"></user_input>"}
				]
			},
			{
				"id": "msg-5",
				"role": "assistant",
				"ts": 1789200005000,
				"content": [
					{"type": "text", "text": "Proceeding with execution."}
				]
			}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, sessionID+".messages.json"), []byte(messagesJSON), 0o644))

	sess, msgs, err := parseClineSession(filepath.Join(taskDir, sessionID+".json"), "test-proj", "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Verify session metadata: empty approval was dropped, so exactly 2 user messages
	assert.Equal(t, 2, sess.UserMessageCount)
	assert.Equal(t, 5, sess.MessageCount)
	assert.Equal(t, "Initial prompt from user", sess.FirstMessage)

	require.Len(t, msgs, 5)

	// Turn 0: stripped
	assert.Equal(t, RoleUser, msgs[0].Role)
	assert.Equal(t, 0, msgs[0].Ordinal)
	assert.Equal(t, "Initial prompt from user", msgs[0].Content)

	// Turn 1: assistant
	assert.Equal(t, RoleAssistant, msgs[1].Role)
	assert.Equal(t, 1, msgs[1].Ordinal)

	// Turn 2: stripped user_input and mode_notice
	assert.Equal(t, RoleUser, msgs[2].Role)
	assert.Equal(t, 2, msgs[2].Ordinal)
	assert.Equal(t, "Now investigate the architecture", msgs[2].Content)

	// Turn 3: assistant
	assert.Equal(t, RoleAssistant, msgs[3].Role)
	assert.Equal(t, 3, msgs[3].Ordinal)

	// Turn 4: assistant (since the empty user approval at msg-4 was skipped, ordinal remains contiguous)
	assert.Equal(t, RoleAssistant, msgs[4].Role)
	assert.Equal(t, 4, msgs[4].Ordinal)
	assert.Equal(t, "Proceeding with execution.", msgs[4].Content)
}

func TestParseClineTeammates_StoredHintsAndRootDisappearance(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sess-hints")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaJSON := `{"session_id": "sess-hints", "cwd": "/workspace"}`
	metaPath := filepath.Join(sessDir, "sess-hints.json")
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	// 1. Two distinct runs produce IDs A (bare) and B (rid).
	run1JSON := `{
		"version": 1,
		"sessionId": "sess-hints__teamtask__worker__r1",
		"origin": {"subagent": "worker"},
		"messages": [{"id": "m1_1", "role": "user", "content": [{"type": "text", "text": "task 1"}], "ts": 1000}]
	}`
	run2JSON := `{
		"version": 1,
		"sessionId": "sess-hints__teamtask__worker__r2",
		"origin": {"subagent": "worker"},
		"messages": [{"id": "m2_1", "role": "user", "content": [{"type": "text", "text": "task 2"}], "ts": 2000}]
	}`
	r1Path := filepath.Join(sessDir, "worker__r1.messages.json")
	r2Path := filepath.Join(sessDir, "worker__r2.messages.json")
	require.NoError(t, os.WriteFile(r1Path, []byte(run1JSON), 0o644))
	require.NoError(t, os.WriteFile(r2Path, []byte(run2JSON), 0o644))

	resInitial, err := parseClineSessionWithTeammates(metaPath, "proj", "local", nil)
	require.NoError(t, err)
	require.Len(t, resInitial, 3)

	bySource := clineTeammateResultBySource(resInitial)
	idx1, found1 := bySource("sess-hints__teamtask__worker__r1")
	require.True(t, found1)
	idA := resInitial[idx1].Session.ID
	assert.Equal(t, "cline:sess-hints__teammate__worker", idA)

	idx2, found2 := bySource("sess-hints__teamtask__worker__r2")
	require.True(t, found2)
	idB := resInitial[idx2].Session.ID
	assert.Equal(t, "cline:sess-hints__teammate__worker__rid-"+clineRIDDigestForTest(t, "sess-hints__teamtask__worker__r2"), idB)

	// Phase 2: Add a third run that sorts first (lexicographically smaller rawSessionID: r0).
	// With stored hints for r1 and r2, idA and idB remain unchanged; r0 gets a new rid-derived ID.
	run0JSON := `{
		"version": 1,
		"sessionId": "sess-hints__teamtask__worker__r0",
		"origin": {"subagent": "worker"},
		"messages": [{"id": "m0_1", "role": "user", "content": [{"type": "text", "text": "task 0"}], "ts": 500}]
	}`
	r0Path := filepath.Join(sessDir, "worker__r0.messages.json")
	require.NoError(t, os.WriteFile(r0Path, []byte(run0JSON), 0o644))

	hints := map[string]string{
		r1Path: idA,
		r2Path: idB,
	}
	resWithRun0, err := parseClineSessionWithTeammates(metaPath, "proj", "local", hints)
	require.NoError(t, err)
	require.Len(t, resWithRun0, 4)

	bySource = clineTeammateResultBySource(resWithRun0)
	idx1After, found1After := bySource("sess-hints__teamtask__worker__r1")
	require.True(t, found1After)
	assert.Equal(t, idA, resWithRun0[idx1After].Session.ID, "idA must remain unchanged when r0 is added")

	idx2After, found2After := bySource("sess-hints__teamtask__worker__r2")
	require.True(t, found2After)
	assert.Equal(t, idB, resWithRun0[idx2After].Session.ID, "idB must remain unchanged when r0 is added")

	idx0After, found0After := bySource("sess-hints__teamtask__worker__r0")
	require.True(t, found0After)
	assert.Equal(t,
		"cline:sess-hints__teammate__worker__rid-"+clineRIDDigestForTest(t, "sess-hints__teamtask__worker__r0"),
		resWithRun0[idx0After].Session.ID)

	// Phase 3: Root-file disappearance while continuation remains.
	contDir := filepath.Join(dir, "sess-root-disappear")
	require.NoError(t, os.MkdirAll(contDir, 0o755))
	contMetaPath := filepath.Join(contDir, "sess-root-disappear.json")
	require.NoError(t, os.WriteFile(contMetaPath, []byte(`{"session_id": "sess-root-disappear", "cwd": "/workspace"}`), 0o644))

	// Chain 1: root1 + next1
	root1JSON := `{
		"version": 1,
		"sessionId": "sess-root-disappear__teamtask__scout__a_root",
		"origin": {"subagent": "scout"},
		"messages": [{"id": "cm1", "role": "user", "content": [{"type": "text", "text": "start"}], "ts": 1000}]
	}`
	next1JSON := `{
		"version": 1,
		"sessionId": "sess-root-disappear__teamtask__scout__z_next",
		"origin": {"subagent": "scout"},
		"messages": [
			{"id": "cm1", "role": "user", "content": [{"type": "text", "text": "start"}], "ts": 1000},
			{"id": "cm2", "role": "assistant", "content": [{"type": "text", "text": "more"}], "ts": 1100}
		]
	}`
	// Chain 2: independent run 2
	runBJSON := `{
		"version": 1,
		"sessionId": "sess-root-disappear__teamtask__scout__m_other",
		"origin": {"subagent": "scout"},
		"messages": [{"id": "bm1", "role": "user", "content": [{"type": "text", "text": "other"}], "ts": 1050}]
	}`
	root1Path := filepath.Join(contDir, "scout__a_root.messages.json")
	next1Path := filepath.Join(contDir, "scout__z_next.messages.json")
	runBPath := filepath.Join(contDir, "scout__m_other.messages.json")
	require.NoError(t, os.WriteFile(root1Path, []byte(root1JSON), 0o644))
	require.NoError(t, os.WriteFile(next1Path, []byte(next1JSON), 0o644))
	require.NoError(t, os.WriteFile(runBPath, []byte(runBJSON), 0o644))

	// Before disappearance:
	// Chain 1 root (a_root) sorts before Chain 2 (m_other).
	// So Chain 1 gets bare form ("...__teammate__scout"), Chain 2 gets rid suffix.
	resContInit, err := parseClineSessionWithTeammates(contMetaPath, "proj", "local", nil)
	require.NoError(t, err)
	require.Len(t, resContInit, 3)
	bySrcCont := clineTeammateResultBySource(resContInit)
	c1Idx, _ := bySrcCont("sess-root-disappear__teamtask__scout__z_next")
	assert.Equal(t, "cline:sess-root-disappear__teammate__scout", resContInit[c1Idx].Session.ID)

	// Now remove root1 file (scout__a_root), leaving next1 and m_other.
	require.NoError(t, os.Remove(root1Path))

	// Case 4A: With exact stored hint for next1Path, existing ID is preserved!
	resWithHint, err := parseClineSessionWithTeammates(contMetaPath, "proj", "local", map[string]string{
		next1Path: "cline:sess-root-disappear__teammate__scout",
	})
	require.NoError(t, err)
	require.Len(t, resWithHint, 3)
	bySrcWithHint := clineTeammateResultBySource(resWithHint)
	c1WithHintIdx, _ := bySrcWithHint("sess-root-disappear__teamtask__scout__z_next")
	assert.Equal(t, "cline:sess-root-disappear__teammate__scout", resWithHint[c1WithHintIdx].Session.ID)

	// Case 4B: Without reachable hint:
	// m_other sorts before z_next, so m_other takes bare form, and z_next falls back to new root-derived ID.
	resWithoutHint, err := parseClineSessionWithTeammates(contMetaPath, "proj", "local", nil)
	require.NoError(t, err)
	require.Len(t, resWithoutHint, 3)
	bySrcNoHint := clineTeammateResultBySource(resWithoutHint)
	c1NoHintIdx, _ := bySrcNoHint("sess-root-disappear__teamtask__scout__z_next")
	assert.Equal(t,
		"cline:sess-root-disappear__teammate__scout__rid-"+clineRIDDigestForTest(t, "sess-root-disappear__teamtask__scout__z_next"),
		resWithoutHint[c1NoHintIdx].Session.ID)

	// Legacy __run2 hint is ignored and not reused
	resLegacy, err := parseClineSessionWithTeammates(contMetaPath, "proj", "local", map[string]string{
		next1Path: "cline:sess-root-disappear__teammate__scout__run2",
	})
	require.NoError(t, err)
	require.Len(t, resLegacy, 3)
	bySrcLegacy := clineTeammateResultBySource(resLegacy)
	c1LegacyIdx, _ := bySrcLegacy("sess-root-disappear__teamtask__scout__z_next")
	assert.NotEqual(t, "cline:sess-root-disappear__teammate__scout__run2", resLegacy[c1LegacyIdx].Session.ID)
}
