package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type junieSessionSummary struct {
	projectDir string
	taskName   string
	createdAt  time.Time
	updatedAt  time.Time
}

type junieTranscriptMessage struct {
	ParsedMessage
	active bool
}

func parseJunieSession(
	ctx context.Context, path, machine string,
) (*ParsedSession, []ParsedMessage, error) {
	sessionID := filepath.Base(filepath.Dir(path))
	summary, present, err := loadJunieSessionSummary(ctx, path, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return parseJunieSessionWithSummary(ctx, path, machine, summary, present)
}

func parseJunieSessionWithSummary(
	ctx context.Context, path, machine string,
	summary junieSessionSummary, summaryPresent bool,
) (*ParsedSession, []ParsedMessage, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("open %s: not a regular file", path)
	}

	sourceSessionID := filepath.Base(filepath.Dir(path))

	lr := newLineReaderContext(ctx, f, maxLineSize)
	defer releaseLineReader(lr)

	var (
		entries          []junieTranscriptMessage
		startedAt        time.Time
		endedAt          time.Time
		sessionName      string
		sessionNameFound bool
		malformedLines   int
		lineNumber       int
	)
	userMessages := make(map[string]int)
	assistantMessages := make(map[string]int)

	appendMessage := func(role RoleType, content string, timestamp time.Time) int {
		entries = append(entries, junieTranscriptMessage{
			ParsedMessage: ParsedMessage{
				Role:          role,
				Content:       content,
				Timestamp:     timestamp,
				IsSystem:      role == RoleSystem,
				ContentLength: len(content),
			},
			active: strings.TrimSpace(content) != "",
		})
		return len(entries) - 1
	}
	setActive := func(ids gjson.Result, active bool) {
		ids.ForEach(func(_, id gjson.Result) bool {
			if index, ok := userMessages[id.Str]; ok {
				entries[index].active = active
			}
			return true
		})
	}

	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		lineNumber++
		if err := contextErrEvery(ctx, lineNumber); err != nil {
			return nil, nil, err
		}
		if !gjson.Valid(line) {
			malformedLines++
			continue
		}

		event := gjson.Parse(line)
		timestamp := junieTimestamp(event.Get("timestampMs"))
		if !timestamp.IsZero() {
			if startedAt.IsZero() {
				startedAt = timestamp
			}
			endedAt = timestamp
		}

		switch event.Get("kind").Str {
		case "UserPromptEvent":
			content := firstNonEmptyJSONLString(
				event.Get("presentablePrompt").Str,
				event.Get("prompt").Str,
			)
			requestID := event.Get("requestId").Str
			active := event.Get("delivery").Str != "Failed" &&
				strings.TrimSpace(content) != ""
			if index, ok := userMessages[requestID]; requestID != "" && ok {
				entries[index].Content = content
				entries[index].ContentLength = len(content)
				entries[index].Timestamp = timestamp
				entries[index].active = active
				continue
			}
			index := appendMessage(RoleUser, content, timestamp)
			entries[index].active = active
			if requestID != "" {
				userMessages[requestID] = index
			}

		case "UserResponseEvent":
			appendMessage(RoleUser, event.Get("prompt").Str, timestamp)

		case "UserAsyncResponseEvent":
			var responses []string
			event.Get("entries").ForEach(func(_, entry gjson.Result) bool {
				response := strings.TrimSpace(strings.Join([]string{
					entry.Get("question").Str,
					entry.Get("answer").Str,
				}, "\n"))
				if response != "" {
					responses = append(responses, response)
				}
				return true
			})
			appendMessage(RoleUser, strings.Join(responses, "\n\n"), timestamp)

		case "UserMessagesCommittedToHistory":
			setActive(event.Get("userMessageIds"), true)

		case "UserMessagesDroppedFromHistory", "UserMessagesFailedInHistory":
			setActive(event.Get("userMessageIds"), false)

		case "SessionTitleSetEvent":
			sessionName = event.Get("name").Str
			sessionNameFound = true

		case "SystemMessageEvent":
			text := strings.TrimSpace(event.Get("text").Str)
			details := strings.TrimSpace(event.Get("details").Str)
			if details != "" && details != text {
				text = strings.TrimSpace(text + "\n\n" + details)
			}
			appendMessage(RoleSystem, text, timestamp)

		case "AgentTaskFailedEvent":
			appendMessage(RoleSystem, "Agent task failed", timestamp)

		case "SessionA2uxEvent":
			agentEvent := event.Get("event.agentEvent")
			var content string
			switch agentEvent.Get("kind").Str {
			case "MarkdownBlockUpdatedEvent":
				content = strings.TrimSpace(agentEvent.Get("text").Str)
			case "ResultBlockUpdatedEvent":
				content = strings.TrimSpace(strings.TrimPrefix(
					agentEvent.Get("result").Str, "<!-- ANSWER -->",
				))
			default:
				continue
			}
			stepID := agentEvent.Get("stepId").Str
			if index, ok := assistantMessages[stepID]; stepID != "" && ok {
				entries[index].Content = content
				entries[index].ContentLength = len(content)
				entries[index].Timestamp = timestamp
				entries[index].active = content != ""
				continue
			}
			index := appendMessage(RoleAssistant, content, timestamp)
			if stepID != "" {
				assistantMessages[stepID] = index
			}
		}
	}
	if err := lr.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading Junie session %s: %w", path, err)
	}

	messages := make([]ParsedMessage, 0, len(entries))
	firstMessage := ""
	userCount := 0
	for _, entry := range entries {
		if !entry.active || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		entry.Ordinal = len(messages)
		messages = append(messages, entry.ParsedMessage)
		if entry.Role == RoleUser {
			userCount++
			if firstMessage == "" {
				firstMessage = truncate(
					strings.ReplaceAll(entry.Content, "\n", " "), 300,
				)
			}
		}
	}
	if len(messages) == 0 {
		return nil, nil, nil
	}

	if !summary.createdAt.IsZero() &&
		(startedAt.IsZero() || summary.createdAt.Before(startedAt)) {
		startedAt = summary.createdAt
	}
	if summary.updatedAt.After(endedAt) {
		endedAt = summary.updatedAt
	}
	if !sessionNameFound && summaryPresent {
		sessionName = summary.taskName
	}
	if strings.TrimSpace(sessionName) != "" {
		firstMessage = sessionName
	}

	project := ExtractProjectFromCwdWithBranchContext(
		WithoutFilesystemProjectDiscovery(ctx), summary.projectDir, "",
	)
	if project == "" {
		project = "junie"
	}
	cwd := ""
	if filepath.IsAbs(summary.projectDir) || looksLikeWindowsPath(summary.projectDir) {
		cwd = filepath.Clean(summary.projectDir)
	}

	sess := &ParsedSession{
		ID:                 "junie:" + sourceSessionID,
		SourceSessionID:    sourceSessionID,
		Project:            project,
		Machine:            machine,
		Agent:              AgentJunie,
		Cwd:                cwd,
		FirstMessage:       firstMessage,
		SessionName:        sessionName,
		SessionNamePresent: sessionNameFound || summaryPresent,
		StartedAt:          startedAt,
		EndedAt:            endedAt,
		MessageCount:       len(messages),
		UserMessageCount:   userCount,
		MalformedLines:     malformedLines,
		File: FileInfo{
			Path:  path,
			Size:  info.Size(),
			Mtime: info.ModTime().UnixNano(),
		},
	}
	return sess, messages, nil
}

func loadJunieSessionSummary(
	ctx context.Context, eventsPath, sessionID string,
) (junieSessionSummary, bool, error) {
	indexPath := filepath.Join(filepath.Dir(filepath.Dir(eventsPath)), "index.jsonl")
	summaries, present, err := loadJunieIndexSnapshot(ctx, indexPath)
	if err != nil || !present {
		return junieSessionSummary{}, false, err
	}
	line, found := summaries[sessionID]
	if !found {
		return junieSessionSummary{}, false, nil
	}
	return parseJunieSessionSummary(line), true, nil
}

func parseJunieSessionSummary(line string) junieSessionSummary {
	return junieSessionSummary{
		projectDir: gjson.Get(line, "projectDir").Str,
		taskName:   gjson.Get(line, "taskName").Str,
		createdAt:  junieTimestamp(gjson.Get(line, "createdAt")),
		updatedAt:  junieTimestamp(gjson.Get(line, "updatedAt")),
	}
}

func junieTimestamp(value gjson.Result) time.Time {
	if value.Type != gjson.Number || value.Int() <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value.Int())
}
