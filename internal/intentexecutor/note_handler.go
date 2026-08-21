package intentexecutor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
)

type NoteCreator interface {
	Create(context.Context, auth.Principal, note.CreateInput) (note.Note, bool, error)
}

type NoteProjector interface {
	ProjectPending(context.Context, auth.Principal, int) error
}

type DraftService interface {
	Create(context.Context, notedraft.Owner, string, string, string) (notedraft.Draft, error)
	Latest(context.Context, notedraft.Owner, string) (notedraft.Draft, error)
	Confirm(context.Context, notedraft.Owner, string, string, string) (notedraft.Draft, bool, error)
	Cancel(context.Context, notedraft.Owner, string, string, string) (notedraft.Draft, bool, error)
}

type Summarizer interface {
	Summarize(context.Context, []agent.Message) (string, string, error)
}

type NoteCreateHandler struct {
	Notes      NoteCreator
	Projector  NoteProjector
	Drafts     DraftService
	Summarizer Summarizer
}

func (h NoteCreateHandler) Execute(ctx context.Context, input routing.Input) (routing.Result, error) {
	owner := notedraft.Owner{UserID: input.Run.UserID, TenantID: input.Run.TenantID}
	switch input.Run.Decision.Reason {
	case "draft_confirm":
		return h.confirm(ctx, input, owner)
	case "draft_cancel":
		return h.cancel(ctx, input, owner)
	case "draft_modify":
		return h.modify(ctx, input, owner)
	}
	if input.Run.Decision.NeedsModel || requestsHistorySummary(input.Content) {
		return h.createDraft(ctx, input, owner)
	}
	return h.createDirect(ctx, input)
}

func (h NoteCreateHandler) createDirect(ctx context.Context, input routing.Input) (routing.Result, error) {
	if h.Notes == nil {
		return routing.Result{}, errors.New("note create service is unavailable")
	}
	content := explicitNoteContent(input.Content)
	if content == "" {
		return routing.Result{Text: "请在记笔记指令后补充要保存的具体内容。"}, nil
	}
	title := titleFromContent(content)
	created, replayed, err := h.Notes.Create(ctx, principal(input.Run), note.CreateInput{
		Title: title, Content: content, IdempotencyKey: "chat-run:" + input.Run.RunID,
	})
	if err != nil {
		return routing.Result{}, err
	}
	h.dispatchProjection(principal(input.Run))
	return routing.Result{Text: noteSavedText(created, replayed)}, nil
}

func (h NoteCreateHandler) createDraft(ctx context.Context, input routing.Input, owner notedraft.Owner) (routing.Result, error) {
	if h.Drafts == nil || h.Summarizer == nil {
		return routing.Result{}, errors.New("note draft service is unavailable")
	}
	title, content, err := h.Summarizer.Summarize(ctx, input.Messages)
	if err != nil {
		return routing.Result{}, err
	}
	draft, err := h.Drafts.Create(ctx, owner, input.Run.SessionID, title, content)
	if err != nil {
		return routing.Result{}, err
	}
	return draftResult(draft, "我整理了一条待确认笔记。确认内容无误后回复“确认保存”，确认前不会写入正式笔记。"), nil
}

func (h NoteCreateHandler) confirm(ctx context.Context, input routing.Input, owner notedraft.Owner) (routing.Result, error) {
	if h.Drafts == nil || h.Notes == nil {
		return routing.Result{}, errors.New("note services are unavailable")
	}
	draft, err := h.Drafts.Latest(ctx, owner, input.Run.SessionID)
	if err != nil {
		return routing.Result{Text: "当前会话没有可确认的待保存笔记，请先重新生成候选。"}, nil
	}
	created, replayed, err := h.Notes.Create(ctx, principal(input.Run), note.CreateInput{
		Title: draft.Title, Content: draft.Content, IdempotencyKey: "chat-draft:" + draft.ID + ":" + draft.ContentHash,
	})
	if err != nil {
		return routing.Result{}, err
	}
	h.dispatchProjection(principal(input.Run))
	if _, _, err := h.Drafts.Confirm(ctx, owner, input.Run.SessionID, draft.ID, draft.ContentHash); err != nil {
		return routing.Result{}, err
	}
	return routing.Result{Text: noteSavedText(created, replayed)}, nil
}

func (h NoteCreateHandler) dispatchProjection(principal auth.Principal) {
	if h.Projector == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = h.Projector.ProjectPending(ctx, principal, 5)
	}()
}

func (h NoteCreateHandler) cancel(ctx context.Context, input routing.Input, owner notedraft.Owner) (routing.Result, error) {
	if h.Drafts == nil {
		return routing.Result{}, errors.New("note draft service is unavailable")
	}
	draft, err := h.Drafts.Latest(ctx, owner, input.Run.SessionID)
	if err != nil {
		return routing.Result{Text: "当前会话没有可取消的待保存笔记。"}, nil
	}
	if _, _, err := h.Drafts.Cancel(ctx, owner, input.Run.SessionID, draft.ID, draft.ContentHash); err != nil {
		return routing.Result{}, err
	}
	return routing.Result{Text: "已取消这条候选笔记，没有写入正式笔记。"}, nil
}

func (h NoteCreateHandler) modify(ctx context.Context, input routing.Input, owner notedraft.Owner) (routing.Result, error) {
	if h.Drafts == nil {
		return routing.Result{}, errors.New("note draft service is unavailable")
	}
	if _, err := h.Drafts.Latest(ctx, owner, input.Run.SessionID); err != nil {
		return routing.Result{Text: "当前会话没有可修改的待保存笔记，请先生成候选。"}, nil
	}
	content := contentAfterAny(input.Content, "修改为", "改成", "调整为")
	if content == "" {
		return routing.Result{Text: "请补充候选笔记需要修改成的具体内容。"}, nil
	}
	draft, err := h.Drafts.Create(ctx, owner, input.Run.SessionID, titleFromContent(content), content)
	if err != nil {
		return routing.Result{}, err
	}
	return draftResult(draft, "候选笔记已更新。确认内容无误后回复“确认保存”。"), nil
}

func principal(run routing.RunContext) auth.Principal {
	return auth.Principal{UserID: run.UserID, TenantID: run.TenantID, AccessToken: run.AccessToken}
}

func draftResult(draft notedraft.Draft, text string) routing.Result {
	return routing.Result{Text: text, NeedConfirm: true, Candidate: &routing.NoteCandidate{
		ID: draft.ID, Title: draft.Title, Content: draft.Content, ContentHash: draft.ContentHash, ExpiresAt: draft.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	}}
}

func noteSavedText(created note.Note, replayed bool) string {
	if replayed {
		return fmt.Sprintf("这条笔记已经保存过，当前状态：%s。", created.Status)
	}
	return fmt.Sprintf("笔记已保存，当前索引状态：%s。", created.Status)
}

func requestsHistorySummary(input string) bool {
	return strings.Contains(input, "总结刚才") || strings.Contains(input, "总结以上") || strings.Contains(input, "从聊天历史")
}

func explicitNoteContent(input string) string {
	return contentAfterAny(input, "帮我记住", "记一笔", "保存笔记", "记录一下", "记下来")
}

func contentAfterAny(input string, prefixes ...string) string {
	for _, prefix := range prefixes {
		if index := strings.Index(input, prefix); index >= 0 {
			return strings.TrimSpace(strings.TrimLeft(input[index+len(prefix):], "：:，,。 "))
		}
	}
	return ""
}

func titleFromContent(content string) string {
	line := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	runes := []rune(line)
	if len(runes) > 60 {
		line = string(runes[:60])
	}
	if utf8.RuneCountInString(line) == 0 {
		return "聊天笔记"
	}
	return line
}
