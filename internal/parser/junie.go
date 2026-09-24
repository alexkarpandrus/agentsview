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

type junieParserState struct {
	entries           []junieTranscriptMessage
	userMessages      map[string]int
	assistantMessages map[string]int
	startedAt         time.Time
	endedAt           time.Time
	sessionName       string
	sessionNameFound  bool
	malformedLines    int
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

	state := junieParserState{
		userMessages:      make(map[string]int),
		assistantMessages: make(map[string]int),
	}
	lr := newLineReaderContext(ctx, f, maxLineSize)
	defer releaseLineReader(lr)
	for lineNumber := 1; ; lineNumber++ {
		line, ok := lr.next()
		if !ok {
			break
		}
		if err := contextErrEvery(ctx, lineNumber); err != nil {
			return nil, nil, err
		}
		if !gjson.Valid(line) {
			state.malformedLines++
			continue
		}
		state.consumeEvent(gjson.Parse(line))
	}
	if err := lr.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading Junie session %s: %w", path, err)
	}
	return state.session(
		ctx, path, machine, filepath.Base(filepath.Dir(path)),
		info.Size(), info.ModTime().UnixNano(), summary, summaryPresent,
	)
}

func (s *junieParserState) consumeEvent(event gjson.Result) {
	timestamp := junieTimestamp(event.Get("timestampMs"))
	if !timestamp.IsZero() {
		if s.startedAt.IsZero() {
			s.startedAt = timestamp
		}
		s.endedAt = timestamp
	}

	switch event.Get("kind").Str {
	case "UserPromptEvent":
		s.consumeUserPrompt(event, timestamp)
	case "UserResponseEvent":
		s.appendMessage(RoleUser, event.Get("prompt").Str, timestamp)
	case "UserAsyncResponseEvent":
		s.consumeAsyncResponse(event, timestamp)
	case "UserMessagesCommittedToHistory":
		s.setUserMessagesActive(event.Get("userMessageIds"), true)
	case "UserMessagesDroppedFromHistory", "UserMessagesFailedInHistory":
		s.setUserMessagesActive(event.Get("userMessageIds"), false)
	case "SessionTitleSetEvent":
		s.sessionName = event.Get("name").Str
		s.sessionNameFound = true
	case "SystemMessageEvent":
		s.consumeSystemMessage(event, timestamp)
	case "AgentTaskFailedEvent":
		s.appendMessage(RoleSystem, "Agent task failed", timestamp)
	case "SessionA2uxEvent":
		s.consumeA2UXEvent(event, timestamp)
	}
}

func (s *junieParserState) consumeUserPrompt(event gjson.Result, timestamp time.Time) {
	content := firstNonEmptyJSONLString(
		event.Get("presentablePrompt").Str,
		event.Get("prompt").Str,
	)
	requestID := event.Get("requestId").Str
	active := event.Get("delivery").Str != "Failed" && strings.TrimSpace(content) != ""
	if index, ok := s.userMessages[requestID]; requestID != "" && ok {
		s.entries[index].Content = content
		s.entries[index].ContentLength = len(content)
		s.entries[index].Timestamp = timestamp
		s.entries[index].active = active
		return
	}
	index := s.appendMessage(RoleUser, content, timestamp)
	s.entries[index].active = active
	if requestID != "" {
		s.userMessages[requestID] = index
	}
}

func (s *junieParserState) consumeAsyncResponse(event gjson.Result, timestamp time.Time) {
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
	s.appendMessage(RoleUser, strings.Join(responses, "\n\n"), timestamp)
}

func (s *junieParserState) consumeSystemMessage(event gjson.Result, timestamp time.Time) {
	text := strings.TrimSpace(event.Get("text").Str)
	details := strings.TrimSpace(event.Get("details").Str)
	if details != "" && details != text {
		text = strings.TrimSpace(text + "\n\n" + details)
	}
	s.appendMessage(RoleSystem, text, timestamp)
}

func (s *junieParserState) consumeA2UXEvent(event gjson.Result, timestamp time.Time) {
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
		return
	}
	stepID := agentEvent.Get("stepId").Str
	if index, ok := s.assistantMessages[stepID]; stepID != "" && ok {
		s.entries[index].Content = content
		s.entries[index].ContentLength = len(content)
		s.entries[index].Timestamp = timestamp
		s.entries[index].active = content != ""
		return
	}
	index := s.appendMessage(RoleAssistant, content, timestamp)
	if stepID != "" {
		s.assistantMessages[stepID] = index
	}
}

func (s *junieParserState) appendMessage(
	role RoleType, content string, timestamp time.Time,
) int {
	s.entries = append(s.entries, junieTranscriptMessage{
		ParsedMessage: ParsedMessage{
			Role:          role,
			Content:       content,
			Timestamp:     timestamp,
			IsSystem:      role == RoleSystem,
			ContentLength: len(content),
		},
		active: strings.TrimSpace(content) != "",
	})
	return len(s.entries) - 1
}

func (s *junieParserState) setUserMessagesActive(ids gjson.Result, active bool) {
	ids.ForEach(func(_, id gjson.Result) bool {
		if index, ok := s.userMessages[id.Str]; ok {
			s.entries[index].active = active
		}
		return true
	})
}

func (s *junieParserState) session(
	ctx context.Context, path, machine, sourceSessionID string,
	fileSize, mtime int64,
	summary junieSessionSummary, summaryPresent bool,
) (*ParsedSession, []ParsedMessage, error) {
	messages := make([]ParsedMessage, 0, len(s.entries))
	firstMessage := ""
	userCount := 0
	for _, entry := range s.entries {
		if !entry.active || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		entry.Ordinal = len(messages)
		messages = append(messages, entry.ParsedMessage)
		if entry.Role == RoleUser {
			userCount++
			if firstMessage == "" {
				firstMessage = truncate(strings.ReplaceAll(entry.Content, "\n", " "), 300)
			}
		}
	}
	if len(messages) == 0 {
		return nil, nil, nil
	}

	if !summary.createdAt.IsZero() &&
		(s.startedAt.IsZero() || summary.createdAt.Before(s.startedAt)) {
		s.startedAt = summary.createdAt
	}
	if summary.updatedAt.After(s.endedAt) {
		s.endedAt = summary.updatedAt
	}
	if !s.sessionNameFound && summaryPresent {
		s.sessionName = summary.taskName
	}
	if strings.TrimSpace(s.sessionName) != "" {
		firstMessage = s.sessionName
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

	session := &ParsedSession{
		ID:                 "junie:" + sourceSessionID,
		SourceSessionID:    sourceSessionID,
		Project:            project,
		Machine:            machine,
		Agent:              AgentJunie,
		Cwd:                cwd,
		FirstMessage:       firstMessage,
		SessionName:        s.sessionName,
		SessionNamePresent: s.sessionNameFound || summaryPresent,
		StartedAt:          s.startedAt,
		EndedAt:            s.endedAt,
		MessageCount:       len(messages),
		UserMessageCount:   userCount,
		MalformedLines:     s.malformedLines,
		File: FileInfo{
			Path:  path,
			Size:  fileSize,
			Mtime: mtime,
		},
	}
	return session, messages, nil
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
