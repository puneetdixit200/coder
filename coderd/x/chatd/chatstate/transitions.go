package chatstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/sqlc-dev/pqtype"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/coderd/database"
	coderdpubsub "github.com/coder/coder/v2/coderd/pubsub"
	"github.com/coder/coder/v2/coderd/x/chatd/chatprompt"
	"github.com/coder/coder/v2/codersdk"
)

// =============================================================================
// CreateChat
//
// CreateChat is the standalone N -> R0 entry point. It is a
// package-level function rather than a method on [ChatMachine]
// because no chat-scoped machine can exist before the chat row is
// written.
// =============================================================================

// CreateChatInput configures [CreateChat].
type CreateChatInput struct {
	OrganizationID    uuid.UUID
	OwnerID           uuid.UUID
	WorkspaceID       uuid.NullUUID
	BuildID           uuid.NullUUID
	AgentID           uuid.NullUUID
	ParentChatID      uuid.NullUUID
	RootChatID        uuid.NullUUID
	LastModelConfigID uuid.UUID
	Title             string
	Mode              database.NullChatMode
	PlanMode          database.NullChatPlanMode
	MCPServerIDs      []uuid.UUID
	Labels            pqtype.NullRawMessage
	DynamicTools      pqtype.NullRawMessage
	ClientType        database.ChatClientType
	InitialMessages   []Message
}

// CreateChatResult is the value returned by [CreateChat]. It carries
// the new chat row and the inserted initial history.
type CreateChatResult struct {
	Chat            database.Chat
	InitialMessages []database.ChatMessage
}

// CreateChat creates a brand new chat with initial history in a single
// transaction.
//
// Validation:
//   - InitialMessages must be non-empty.
//   - The final entry in InitialMessages must be a user message.
//   - All other entries must be non-user messages (system, assistant, tool).
//
// After commit CreateChat publishes a `chat:update` message describing
// the new chat snapshot. Because the new chat has no worker assigned,
// CreateChat also publishes an ownership hint so workers can race to
// acquire the runnable chat.
func CreateChat(
	ctx context.Context,
	store database.Store,
	publisher Publisher,
	input CreateChatInput,
) (CreateChatResult, error) {
	if store == nil {
		return CreateChatResult{}, xerrors.New("chatstate: CreateChat called with nil store")
	}
	if publisher == nil {
		return CreateChatResult{}, xerrors.New("chatstate: CreateChat called with nil publisher")
	}
	if len(input.InitialMessages) == 0 {
		return CreateChatResult{}, newTransitionError(
			TransitionCreateChat, StateN,
			"initial messages must include at least one user message",
		)
	}
	last := input.InitialMessages[len(input.InitialMessages)-1]
	if last.Role != database.ChatMessageRoleUser {
		return CreateChatResult{}, newTransitionError(
			TransitionCreateChat, StateN,
			"final initial message must be a user message",
		)
	}
	for i, m := range input.InitialMessages[:len(input.InitialMessages)-1] {
		if m.Role == database.ChatMessageRoleUser {
			return CreateChatResult{}, newTransitionError(
				TransitionCreateChat, StateN,
				"initial message #"+itoa(i)+" must not be a user message",
			)
		}
	}

	var (
		result        CreateChatResult
		runnableState bool
	)
	err := store.InTx(func(store database.Store) error {
		chat, ierr := store.InsertChatWithSnapshot(ctx, database.InsertChatWithSnapshotParams{
			OrganizationID:    input.OrganizationID,
			OwnerID:           input.OwnerID,
			WorkspaceID:       input.WorkspaceID,
			BuildID:           input.BuildID,
			AgentID:           input.AgentID,
			ParentChatID:      input.ParentChatID,
			RootChatID:        input.RootChatID,
			LastModelConfigID: input.LastModelConfigID,
			Title:             input.Title,
			Mode:              input.Mode,
			PlanMode:          input.PlanMode,
			Status:            database.ChatStatusRunning,
			MCPServerIDs:      input.MCPServerIDs,
			Labels:            input.Labels,
			DynamicTools:      input.DynamicTools,
			ClientType:        input.ClientType,
		})
		if ierr != nil {
			return xerrors.Errorf("insert chat: %w", ierr)
		}
		// Insert the initial history under the new chat row. The
		// message revision trigger advances `history_version` to the
		// current `snapshot_version` (which is 1 for a brand new chat).
		inserted, ierr := store.InsertChatMessages(ctx, toInsertParams(chat.ID, input.InitialMessages))
		if ierr != nil {
			return xerrors.Errorf("insert initial messages: %w", ierr)
		}
		refreshed, gerr := store.GetChatByID(ctx, chat.ID)
		if gerr != nil {
			return xerrors.Errorf("reload chat after initial messages: %w", gerr)
		}
		result = CreateChatResult{
			Chat:            refreshed,
			InitialMessages: inserted,
		}
		runnableState = ClassifyExecutionState(refreshed, false, true).IsRunnable()
		return nil
	}, nil)
	if err != nil {
		return CreateChatResult{}, err
	}
	updateChannel := coderdpubsub.ChatStateUpdateChannel(result.Chat.ID)
	if perr := publisher.Publish(updateChannel, buildChatUpdateMessage(result.Chat)); perr != nil {
		return result, xerrors.Errorf("publish %s: %w", updateChannel, perr)
	}
	if runnableState {
		if perr := publisher.Publish(coderdpubsub.ChatStateOwnershipChannel, buildChatOwnershipMessage(result.Chat)); perr != nil {
			return result, xerrors.Errorf("publish ownership: %w", perr)
		}
	}
	return result, nil
}

// itoa is a tiny stdlib-free integer-to-string helper. Avoids
// importing strconv only for CreateChat's validation message.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// =============================================================================
// Shared transition helpers
// =============================================================================

// applyExecutionStateUpdate is a small adapter so transition methods
// do not have to repeat the UpdateChatExecutionState boilerplate.
// The state machine writes status, archived, last_error, ownership
// identifiers, and the requires-action deadline as one atomic update.
type executionStateUpdate struct {
	Status                   database.ChatStatus
	Archived                 bool
	WorkerID                 uuid.NullUUID
	RunnerID                 uuid.NullUUID
	LastError                pqtype.NullRawMessage
	RequiresActionDeadlineAt sql.NullTime
}

func (tx *Tx) applyExecutionState(u executionStateUpdate) (database.Chat, error) {
	return tx.store.UpdateChatExecutionState(tx.ctx, database.UpdateChatExecutionStateParams{
		ID:                       tx.chatID,
		Status:                   u.Status,
		Archived:                 u.Archived,
		WorkerID:                 u.WorkerID,
		RunnerID:                 u.RunnerID,
		LastError:                u.LastError,
		RequiresActionDeadlineAt: u.RequiresActionDeadlineAt,
	})
}

// insertMessages inserts the given Message batch under the current
// chat. The message-revision trigger handles history_version and
// generation_attempt bookkeeping automatically.
func (tx *Tx) insertMessages(messages []Message) ([]database.ChatMessage, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	inserted, err := tx.store.InsertChatMessages(tx.ctx, toInsertParams(tx.chatID, messages))
	if err != nil {
		return nil, xerrors.Errorf("insert messages: %w", err)
	}
	return inserted, nil
}

// clearQueue deletes all queued messages on the chat and returns the
// IDs that were deleted in queue order.
func (tx *Tx) clearQueue() ([]int64, error) {
	queued, err := tx.store.GetChatQueuedMessagesByPosition(tx.ctx, tx.chatID)
	if err != nil {
		return nil, xerrors.Errorf("get queued for clear: %w", err)
	}
	if len(queued) == 0 {
		return nil, nil
	}
	if _, err := tx.store.DeleteAllChatQueuedMessagesReturningCount(tx.ctx, tx.chatID); err != nil {
		return nil, xerrors.Errorf("delete queued: %w", err)
	}
	ids := make([]int64, len(queued))
	for i, q := range queued {
		ids[i] = q.ID
	}
	return ids, nil
}

// insertQueuedMessage inserts a queued user message. created_by falls
// back to chats.owner_id only when the message does not supply one.
func (tx *Tx) insertQueuedMessage(ownerFallback uuid.UUID, m Message) (database.ChatQueuedMessage, error) {
	createdBy := ownerFallback
	if m.CreatedBy.Valid {
		createdBy = m.CreatedBy.UUID
	}
	rawContent := m.Content.RawMessage
	if !m.Content.Valid || len(rawContent) == 0 {
		rawContent = json.RawMessage("null")
	}
	return tx.store.InsertChatQueuedMessageWithCreator(tx.ctx, database.InsertChatQueuedMessageWithCreatorParams{
		ChatID:        tx.chatID,
		Content:       rawContent,
		ModelConfigID: m.ModelConfigID,
		CreatedBy:     createdBy,
	})
}

// messageFromQueuedRow synthesizes a Message from a stored queued row,
// suitable for promoting into active history.
func messageFromQueuedRow(q database.ChatQueuedMessage) Message {
	return Message{
		Role:           database.ChatMessageRoleUser,
		Content:        pqtype.NullRawMessage{RawMessage: q.Content, Valid: q.Content != nil},
		Visibility:     database.ChatMessageVisibilityBoth,
		ModelConfigID:  q.ModelConfigID,
		CreatedBy:      uuid.NullUUID{UUID: q.CreatedBy, Valid: true},
		ContentVersion: chatprompt.CurrentContentVersion,
	}
}

// =============================================================================
// SetArchived
// =============================================================================

// SetArchivedInput configures [Tx.SetArchived].
type SetArchivedInput struct {
	Archived bool
}

// SetArchivedResult is returned by [Tx.SetArchived].
type SetArchivedResult struct{}

// SetArchived sets or clears the chat's archived marker.
func (tx *Tx) SetArchived(input SetArchivedInput) (SetArchivedResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionSetArchived)
	if err != nil {
		return SetArchivedResult{}, err
	}
	if input.Archived == chat.Archived {
		// The matrix only allows SetArchived(true) from W/E0/E1 and
		// SetArchived(false) from XW/XE0/XE1. A request whose Archived
		// field already matches the chat's current archived flag is
		// the wrong direction (or a no-op) and must be rejected so we
		// do not silently roll the snapshot or publish a chat:update.
		return SetArchivedResult{}, newTransitionError(
			TransitionSetArchived, from,
			"SetArchived input matches the current archived flag",
		)
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   chat.Status,
		Archived:                 input.Archived,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: chat.RequiresActionDeadlineAt,
	}); err != nil {
		return SetArchivedResult{}, xerrors.Errorf("update archive: %w", err)
	}
	return SetArchivedResult{}, nil
}

// =============================================================================
// SendMessage
// =============================================================================

// BusyBehavior controls how SendMessage behaves when the chat is
// currently busy (R*/I*/A*). From idle/error states the two behaviors
// are equivalent.
type BusyBehavior string

const (
	BusyBehaviorQueue     BusyBehavior = "queue"
	BusyBehaviorInterrupt BusyBehavior = "interrupt"
)

// SendMessageInput configures [Tx.SendMessage].
type SendMessageInput struct {
	Message      Message
	BusyBehavior BusyBehavior
}

// SendMessageResult is returned by [Tx.SendMessage].
type SendMessageResult struct {
	InsertedMessages []database.ChatMessage
	QueuedMessage    *database.ChatQueuedMessage
}

// SendMessage admits a new user message. Depending on input state and
// BusyBehavior, the message lands directly in history, in the queue,
// or replaces the queue head as part of a running-state promotion.
func (tx *Tx) SendMessage(input SendMessageInput) (SendMessageResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionSendMessage)
	if err != nil {
		return SendMessageResult{}, err
	}
	if input.Message.Role != database.ChatMessageRoleUser {
		return SendMessageResult{}, newTransitionError(
			TransitionSendMessage, from,
			"SendMessage requires a user message",
		)
	}
	switch input.BusyBehavior {
	case BusyBehaviorQueue, BusyBehaviorInterrupt:
		// ok
	default:
		// Reject unknown / empty BusyBehavior up front so an invalid
		// value cannot fall through to the queue path on busy states
		// or be silently ignored on idle states. The callers in chatd
		// default empty to queue; chatstate is the lower-level API
		// and refuses to guess.
		return SendMessageResult{}, newTransitionError(
			TransitionSendMessage, from,
			"invalid BusyBehavior",
		)
	}
	switch from {
	// Idle / empty-queue error: insert directly into history, clear
	// last_error, leave queue alone.
	case StateW, StateE0:
		return tx.sendMessageDirect(chat, input.Message)

	// Error-with-queue: append to tail, promote previous head into
	// history, clear last_error.
	case StateE1:
		return tx.sendMessageE1(chat, input.Message)

	// Running with no queue.
	case StateR0:
		if input.BusyBehavior == BusyBehaviorInterrupt {
			return tx.sendMessageQueueAndSetStatus(chat, input.Message, database.ChatStatusInterrupting, chat.LastError, chat.RequiresActionDeadlineAt)
		}
		return tx.sendMessageQueueAndSetStatus(chat, input.Message, chat.Status, chat.LastError, chat.RequiresActionDeadlineAt)

	// Running with queue.
	case StateR1:
		if input.BusyBehavior == BusyBehaviorInterrupt {
			return tx.sendMessageQueueAndSetStatus(chat, input.Message, database.ChatStatusInterrupting, chat.LastError, chat.RequiresActionDeadlineAt)
		}
		return tx.sendMessageQueueAndSetStatus(chat, input.Message, chat.Status, chat.LastError, chat.RequiresActionDeadlineAt)

	// Interrupting: queue regardless of busy behavior.
	case StateI0, StateI1:
		return tx.sendMessageQueueAndSetStatus(chat, input.Message, chat.Status, chat.LastError, chat.RequiresActionDeadlineAt)

	// Requires-action: queue keeps A*; interrupt cancels pending
	// dynamic calls and resumes in running.
	case StateA0, StateA1:
		if input.BusyBehavior == BusyBehaviorInterrupt {
			return tx.sendMessageInterruptRequiresAction(chat, input.Message)
		}
		return tx.sendMessageQueueAndSetStatus(chat, input.Message, chat.Status, chat.LastError, chat.RequiresActionDeadlineAt)
	}
	return SendMessageResult{}, newTransitionError(TransitionSendMessage, from, "unhandled state in SendMessage")
}

func (tx *Tx) sendMessageDirect(chat database.Chat, m Message) (SendMessageResult, error) {
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, "Tool execution interrupted by new user message", false)
	if err != nil {
		return SendMessageResult{}, err
	}
	inserted, err := tx.insertMessages(append(cancels, m))
	if err != nil {
		return SendMessageResult{}, err
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                pqtype.NullRawMessage{},
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return SendMessageResult{}, xerrors.Errorf("set running: %w", err)
	}
	return SendMessageResult{
		InsertedMessages: inserted,
	}, nil
}

func (tx *Tx) sendMessageE1(chat database.Chat, m Message) (SendMessageResult, error) {
	queued, err := tx.insertQueuedMessage(chat.OwnerID, m)
	if err != nil {
		return SendMessageResult{}, xerrors.Errorf("insert queued: %w", err)
	}
	head, err := tx.store.GetChatQueuedMessageHead(tx.ctx, tx.chatID)
	if err != nil {
		return SendMessageResult{}, xerrors.Errorf("get queue head: %w", err)
	}
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, "Tool execution interrupted by queued message promotion", false)
	if err != nil {
		return SendMessageResult{}, err
	}
	promoted := messageFromQueuedRow(head)
	inserted, err := tx.insertMessages(append(cancels, promoted))
	if err != nil {
		return SendMessageResult{}, err
	}
	if _, err := tx.store.DeleteChatQueuedMessageReturningCount(tx.ctx, database.DeleteChatQueuedMessageReturningCountParams{
		ID:     head.ID,
		ChatID: tx.chatID,
	}); err != nil {
		return SendMessageResult{}, xerrors.Errorf("delete promoted queued head: %w", err)
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                pqtype.NullRawMessage{},
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return SendMessageResult{}, xerrors.Errorf("set running: %w", err)
	}
	return SendMessageResult{
		InsertedMessages: inserted,
		QueuedMessage:    &queued,
	}, nil
}

func (tx *Tx) sendMessageQueueAndSetStatus(
	chat database.Chat,
	m Message,
	status database.ChatStatus,
	lastError pqtype.NullRawMessage,
	deadline sql.NullTime,
) (SendMessageResult, error) {
	queued, err := tx.insertQueuedMessage(chat.OwnerID, m)
	if err != nil {
		return SendMessageResult{}, xerrors.Errorf("insert queued: %w", err)
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   status,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                lastError,
		RequiresActionDeadlineAt: deadline,
	}); err != nil {
		return SendMessageResult{}, xerrors.Errorf("update status: %w", err)
	}
	return SendMessageResult{
		QueuedMessage: &queued,
	}, nil
}

func (tx *Tx) sendMessageInterruptRequiresAction(chat database.Chat, m Message) (SendMessageResult, error) {
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, "Tool execution interrupted by user message", true)
	if err != nil {
		return SendMessageResult{}, err
	}
	if _, err := tx.insertMessages(cancels); err != nil {
		return SendMessageResult{}, err
	}
	return tx.sendMessageQueueAndSetStatus(chat, m, database.ChatStatusRunning, chat.LastError, sql.NullTime{})
}

// =============================================================================
// EditMessage
// =============================================================================

// EditMessageInput configures [Tx.EditMessage].
type EditMessageInput struct {
	MessageID             int64
	CreatedBy             uuid.UUID
	Content               pqtype.NullRawMessage
	ModelConfigIDOverride uuid.NullUUID
}

// EditMessageResult is returned by [Tx.EditMessage].
type EditMessageResult struct {
	ReplacementMessage      database.ChatMessage
	DeletedMessageIDs       []int64
	DeletedQueuedMessageIDs []int64
	CancellationMessages    []database.ChatMessage
}

// EditMessage replaces an earlier user message and discards the
// active-history suffix that followed it.
func (tx *Tx) EditMessage(input EditMessageInput) (EditMessageResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionEditMessage)
	if err != nil {
		return EditMessageResult{}, err
	}
	target, err := tx.store.GetChatMessageByID(tx.ctx, input.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return EditMessageResult{}, ErrMessageNotFound
	}
	if err != nil {
		return EditMessageResult{}, xerrors.Errorf("get target message: %w", err)
	}
	if target.ChatID != tx.chatID {
		return EditMessageResult{}, ErrMessageNotFound
	}
	if target.Deleted {
		return EditMessageResult{}, ErrMessageNotFound
	}
	if target.Role != database.ChatMessageRoleUser {
		return EditMessageResult{}, newTransitionError(
			TransitionEditMessage, from,
			"only user messages can be edited",
		)
	}

	suffix, err := tx.store.GetChatMessagesByChatID(tx.ctx, database.GetChatMessagesByChatIDParams{
		ChatID:  tx.chatID,
		AfterID: target.ID - 1, // include target and everything after
	})
	if err != nil {
		return EditMessageResult{}, xerrors.Errorf("get suffix messages: %w", err)
	}
	deletedIDs := make([]int64, 0, len(suffix))
	for _, m := range suffix {
		if !m.Deleted {
			deletedIDs = append(deletedIDs, m.ID)
		}
	}

	if err := tx.store.SoftDeleteChatMessageByID(tx.ctx, target.ID); err != nil {
		return EditMessageResult{}, xerrors.Errorf("soft-delete target: %w", err)
	}
	if err := tx.store.SoftDeleteChatMessagesAfterID(tx.ctx, database.SoftDeleteChatMessagesAfterIDParams{
		ChatID:  tx.chatID,
		AfterID: target.ID,
	}); err != nil {
		return EditMessageResult{}, xerrors.Errorf("soft-delete suffix: %w", err)
	}

	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, "Tool execution interrupted by message edit", false)
	if err != nil {
		return EditMessageResult{}, err
	}
	cancellationMessages, err := tx.insertMessages(cancels)
	if err != nil {
		return EditMessageResult{}, err
	}

	modelConfig := target.ModelConfigID
	if input.ModelConfigIDOverride.Valid {
		modelConfig = input.ModelConfigIDOverride
	}
	replacement := Message{
		Role:           database.ChatMessageRoleUser,
		Content:        input.Content,
		Visibility:     target.Visibility,
		ModelConfigID:  modelConfig,
		CreatedBy:      uuid.NullUUID{UUID: input.CreatedBy, Valid: true},
		ContentVersion: chatprompt.CurrentContentVersion,
	}
	insertedReplacement, err := tx.insertMessages([]Message{replacement})
	if err != nil {
		return EditMessageResult{}, err
	}
	var replacementRow database.ChatMessage
	if len(insertedReplacement) == 1 {
		replacementRow = insertedReplacement[0]
	}

	deletedQueuedIDs, err := tx.clearQueue()
	if err != nil {
		return EditMessageResult{}, err
	}

	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                pqtype.NullRawMessage{},
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return EditMessageResult{}, xerrors.Errorf("set running: %w", err)
	}
	return EditMessageResult{
		ReplacementMessage:      replacementRow,
		DeletedMessageIDs:       deletedIDs,
		DeletedQueuedMessageIDs: deletedQueuedIDs,
		CancellationMessages:    cancellationMessages,
	}, nil
}

// =============================================================================
// DeleteQueuedMessage
// =============================================================================

// DeleteQueuedMessageInput configures [Tx.DeleteQueuedMessage].
type DeleteQueuedMessageInput struct {
	QueuedMessageID int64
}

// DeleteQueuedMessageResult is returned by [Tx.DeleteQueuedMessage].
type DeleteQueuedMessageResult struct {
	DeletedQueuedMessage database.ChatQueuedMessage
}

// DeleteQueuedMessage removes a single queued user message.
func (tx *Tx) DeleteQueuedMessage(input DeleteQueuedMessageInput) (DeleteQueuedMessageResult, error) {
	_, _, err := tx.requireFromAllowed(TransitionDeleteQueuedMessage)
	if err != nil {
		return DeleteQueuedMessageResult{}, err
	}
	target, err := tx.store.GetChatQueuedMessageByID(tx.ctx, database.GetChatQueuedMessageByIDParams{
		ID:     input.QueuedMessageID,
		ChatID: tx.chatID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteQueuedMessageResult{}, ErrQueuedMessageNotFound
	}
	if err != nil {
		return DeleteQueuedMessageResult{}, xerrors.Errorf("get queued: %w", err)
	}
	rows, err := tx.store.DeleteChatQueuedMessageReturningCount(tx.ctx, database.DeleteChatQueuedMessageReturningCountParams{
		ID:     input.QueuedMessageID,
		ChatID: tx.chatID,
	})
	if err != nil {
		return DeleteQueuedMessageResult{}, xerrors.Errorf("delete queued: %w", err)
	}
	if rows == 0 {
		return DeleteQueuedMessageResult{}, ErrQueuedMessageNotFound
	}
	return DeleteQueuedMessageResult{
		DeletedQueuedMessage: target,
	}, nil
}

// =============================================================================
// PromoteQueuedMessage
// =============================================================================

// PromoteQueuedMessageInput configures [Tx.PromoteQueuedMessage].
type PromoteQueuedMessageInput struct {
	QueuedMessageID int64
}

// PromoteQueuedMessageResult is returned by [Tx.PromoteQueuedMessage].
type PromoteQueuedMessageResult struct {
	QueuedMessage        database.ChatQueuedMessage
	InsertedMessage      *database.ChatMessage
	ReorderedQueueOnly   bool
	CancellationMessages []database.ChatMessage
}

// PromoteQueuedMessage promotes the target queued message to the
// queue head; from E1/A1 it also pops it into active history.
func (tx *Tx) PromoteQueuedMessage(input PromoteQueuedMessageInput) (PromoteQueuedMessageResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionPromoteQueuedMessage)
	if err != nil {
		return PromoteQueuedMessageResult{}, err
	}
	target, err := tx.store.GetChatQueuedMessageByID(tx.ctx, database.GetChatQueuedMessageByIDParams{
		ID:     input.QueuedMessageID,
		ChatID: tx.chatID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return PromoteQueuedMessageResult{}, ErrQueuedMessageNotFound
	}
	if err != nil {
		return PromoteQueuedMessageResult{}, xerrors.Errorf("get queued: %w", err)
	}
	rows, err := tx.store.ReorderChatQueuedMessageToHead(tx.ctx, database.ReorderChatQueuedMessageToHeadParams{
		ID:     input.QueuedMessageID,
		ChatID: tx.chatID,
	})
	if err != nil {
		return PromoteQueuedMessageResult{}, xerrors.Errorf("reorder queue: %w", err)
	}
	reorderOnly := rows > 0

	// R1/I1: leave the target at the queue head and transition to
	// status `interrupting` so the worker can drain the in-flight
	// generation before promoting the queue head into active history.
	// No history row is inserted here and no queue rows are deleted.
	if from == StateR1 || from == StateI1 {
		if _, err := tx.applyExecutionState(executionStateUpdate{
			Status:                   database.ChatStatusInterrupting,
			Archived:                 false,
			WorkerID:                 chat.WorkerID,
			RunnerID:                 chat.RunnerID,
			LastError:                chat.LastError,
			RequiresActionDeadlineAt: chat.RequiresActionDeadlineAt,
		}); err != nil {
			return PromoteQueuedMessageResult{}, xerrors.Errorf("set interrupting: %w", err)
		}
		return PromoteQueuedMessageResult{
			QueuedMessage:      target,
			ReorderedQueueOnly: reorderOnly,
		}, nil
	}

	// E1/A1: synthesize cancellations, pop the head, insert into
	// history, set running. Both paths insert a queued user message
	// into active history, so every outstanding tool call must be
	// closed (not just dynamic ones) to keep the LLM history valid.
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, "Tool execution interrupted by queued message promotion", false)
	if err != nil {
		return PromoteQueuedMessageResult{}, err
	}
	promotedMsg := messageFromQueuedRow(target)
	inserted, err := tx.insertMessages(append(cancels, promotedMsg))
	if err != nil {
		return PromoteQueuedMessageResult{}, err
	}
	if _, err := tx.store.DeleteChatQueuedMessageReturningCount(tx.ctx, database.DeleteChatQueuedMessageReturningCountParams{
		ID:     target.ID,
		ChatID: tx.chatID,
	}); err != nil {
		return PromoteQueuedMessageResult{}, xerrors.Errorf("delete promoted queued: %w", err)
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                pqtype.NullRawMessage{},
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return PromoteQueuedMessageResult{}, xerrors.Errorf("set running: %w", err)
	}
	cancellations := inserted[:len(inserted)-1]
	insertedUserMsg := inserted[len(inserted)-1]
	return PromoteQueuedMessageResult{
		QueuedMessage:        target,
		InsertedMessage:      &insertedUserMsg,
		CancellationMessages: cancellations,
		ReorderedQueueOnly:   reorderOnly,
	}, nil
}

// =============================================================================
// Interrupt
// =============================================================================

// InterruptInput configures [Tx.Interrupt].
type InterruptInput struct {
	Reason string
}

// InterruptResult is returned by [Tx.Interrupt].
type InterruptResult struct {
	CancellationMessages []database.ChatMessage
}

// Interrupt requests interruption of an active or requires-action
// chat.
func (tx *Tx) Interrupt(input InterruptInput) (InterruptResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionInterrupt)
	if err != nil {
		return InterruptResult{}, err
	}
	switch from {
	case StateR0, StateR1:
		if _, err := tx.applyExecutionState(executionStateUpdate{
			Status:                   database.ChatStatusInterrupting,
			Archived:                 false,
			WorkerID:                 chat.WorkerID,
			RunnerID:                 chat.RunnerID,
			LastError:                chat.LastError,
			RequiresActionDeadlineAt: chat.RequiresActionDeadlineAt,
		}); err != nil {
			return InterruptResult{}, xerrors.Errorf("set interrupting: %w", err)
		}
		return InterruptResult{}, nil
	case StateA0, StateA1:
		reason := input.Reason
		if reason == "" {
			reason = "Tool execution interrupted by user"
		}
		cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, reason, true)
		if err != nil {
			return InterruptResult{}, err
		}
		inserted, err := tx.insertMessages(cancels)
		if err != nil {
			return InterruptResult{}, err
		}
		if _, err := tx.applyExecutionState(executionStateUpdate{
			Status:                   database.ChatStatusRunning,
			Archived:                 false,
			WorkerID:                 chat.WorkerID,
			RunnerID:                 chat.RunnerID,
			LastError:                chat.LastError,
			RequiresActionDeadlineAt: sql.NullTime{},
		}); err != nil {
			return InterruptResult{}, xerrors.Errorf("set running: %w", err)
		}
		return InterruptResult{
			CancellationMessages: inserted,
		}, nil
	}
	return InterruptResult{}, newTransitionError(TransitionInterrupt, from, "unhandled state in Interrupt")
}

// =============================================================================
// CompleteRequiresAction
// =============================================================================

// ToolResultInput is one submitted dynamic-tool result.
type ToolResultInput struct {
	ToolCallID string
	Output     json.RawMessage
	IsError    bool
}

// CompleteRequiresActionInput configures [Tx.CompleteRequiresAction].
type CompleteRequiresActionInput struct {
	CreatedBy     uuid.UUID
	ModelConfigID uuid.UUID
	Results       []ToolResultInput
}

// CompleteRequiresActionResult is returned by [Tx.CompleteRequiresAction].
type CompleteRequiresActionResult struct {
	InsertedMessages []database.ChatMessage
}

// CompleteRequiresAction validates and stores user-submitted tool
// results that satisfy the chat's pending dynamic tool calls, then
// returns the chat to running.
func (tx *Tx) CompleteRequiresAction(input CompleteRequiresActionInput) (CompleteRequiresActionResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionCompleteRequiresAction)
	if err != nil {
		return CompleteRequiresActionResult{}, err
	}
	pending, err := pendingDynamicToolCallIDs(tx.ctx, tx.store, chat)
	if err != nil {
		return CompleteRequiresActionResult{}, err
	}
	submitted := make(map[string]ToolResultInput, len(input.Results))
	for _, r := range input.Results {
		if _, dup := submitted[r.ToolCallID]; dup {
			return CompleteRequiresActionResult{}, newTransitionError(
				TransitionCompleteRequiresAction, from,
				"duplicate tool_call_id submitted",
			)
		}
		if !json.Valid(r.Output) {
			return CompleteRequiresActionResult{}, newTransitionError(
				TransitionCompleteRequiresAction, from,
				"tool result output is not valid JSON",
			)
		}
		submitted[r.ToolCallID] = r
	}
	if len(submitted) != len(pending) {
		return CompleteRequiresActionResult{}, newTransitionError(
			TransitionCompleteRequiresAction, from,
			"submitted tool results do not match pending tool calls",
		)
	}
	for id := range submitted {
		if _, ok := pending[id]; !ok {
			return CompleteRequiresActionResult{}, newTransitionError(
				TransitionCompleteRequiresAction, from,
				"submitted tool_call_id does not match a pending dynamic tool call",
			)
		}
	}
	messages := make([]Message, 0, len(input.Results))
	for _, r := range input.Results {
		part := codersdk.ChatMessagePart{
			Type:       codersdk.ChatMessagePartTypeToolResult,
			ToolCallID: r.ToolCallID,
			ToolName:   pending[r.ToolCallID],
			Result:     r.Output,
			IsError:    r.IsError,
		}
		raw, merr := chatprompt.MarshalParts([]codersdk.ChatMessagePart{part})
		if merr != nil {
			return CompleteRequiresActionResult{}, xerrors.Errorf("marshal tool result: %w", merr)
		}
		messages = append(messages, Message{
			Role:           database.ChatMessageRoleTool,
			Content:        raw,
			Visibility:     database.ChatMessageVisibilityBoth,
			CreatedBy:      uuid.NullUUID{UUID: input.CreatedBy, Valid: true},
			ModelConfigID:  uuid.NullUUID{UUID: input.ModelConfigID, Valid: true},
			ContentVersion: chatprompt.CurrentContentVersion,
		})
	}
	inserted, err := tx.insertMessages(messages)
	if err != nil {
		return CompleteRequiresActionResult{}, err
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return CompleteRequiresActionResult{}, xerrors.Errorf("set running: %w", err)
	}
	return CompleteRequiresActionResult{
		InsertedMessages: inserted,
	}, nil
}

// =============================================================================
// Ownership transitions: Acquire, Abandon
// =============================================================================

// AcquireInput configures [Tx.Acquire].
type AcquireInput struct {
	WorkerID uuid.UUID
	RunnerID uuid.UUID
}

// AcquireResult is returned by [Tx.Acquire].
type AcquireResult struct{}

// Acquire claims the chat for a worker/runner pair. Execution state
// is preserved.
//
// Acquire never inspects the chat's current ownership: it simply
// overwrites worker_id/runner_id with the supplied identifiers and
// upserts a fresh heartbeat. Detecting and recovering from stale
// leases is a worker-side fence concern outside the state machine.
// Callers that need to coordinate takeovers with the previous owner
// must arrange that out-of-band before calling Acquire.
func (tx *Tx) Acquire(input AcquireInput) (AcquireResult, error) {
	chat, _, err := tx.loadState()
	if err != nil {
		return AcquireResult{}, err
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   chat.Status,
		Archived:                 chat.Archived,
		WorkerID:                 uuid.NullUUID{UUID: input.WorkerID, Valid: true},
		RunnerID:                 uuid.NullUUID{UUID: input.RunnerID, Valid: true},
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: chat.RequiresActionDeadlineAt,
	}); err != nil {
		return AcquireResult{}, xerrors.Errorf("set ownership: %w", err)
	}
	if err := tx.store.UpsertChatHeartbeat(tx.ctx, database.UpsertChatHeartbeatParams{
		ChatID:   tx.chatID,
		RunnerID: input.RunnerID,
	}); err != nil {
		return AcquireResult{}, xerrors.Errorf("upsert heartbeat: %w", err)
	}
	// Acquire writes a fresh heartbeat itself, so the post-commit
	// ownership-hint logic in Update will evaluate the heartbeat as
	// fresh and skip publishing a `chat:ownership` hint.
	return AcquireResult{}, nil
}

// AbandonInput is intentionally empty. Ownership-fence checks belong
// outside the transition in caller code that reads the locked row before
// invoking Abandon.
type AbandonInput struct{}

// AbandonResult is returned by [Tx.Abandon].
type AbandonResult struct{}

// Abandon clears worker_id and runner_id from the locked chat row. It
// rejects calls when the chat is not currently owned (worker_id IS NULL).
// Callers that need to verify their own identity before abandoning should
// read the locked row through tx.Store() and compare values before
// invoking Abandon.
func (tx *Tx) Abandon(_ AbandonInput) (AbandonResult, error) {
	chat, from, err := tx.loadState()
	if err != nil {
		return AbandonResult{}, err
	}
	if !chat.WorkerID.Valid {
		return AbandonResult{}, newTransitionError(TransitionAbandon, from, "chat is not owned")
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   chat.Status,
		Archived:                 chat.Archived,
		WorkerID:                 uuid.NullUUID{},
		RunnerID:                 uuid.NullUUID{},
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: chat.RequiresActionDeadlineAt,
	}); err != nil {
		return AbandonResult{}, xerrors.Errorf("clear ownership: %w", err)
	}
	return AbandonResult{}, nil
}

// =============================================================================
// Worker-only transitions on running chats
// =============================================================================

// RecordGenerationAttemptInput is intentionally empty.
type RecordGenerationAttemptInput struct{}

// RecordGenerationAttemptResult is returned by [Tx.RecordGenerationAttempt].
type RecordGenerationAttemptResult struct {
	GenerationAttempt int64
}

// RecordGenerationAttempt durably records that the worker is
// attempting another generation under the current history version.
func (tx *Tx) RecordGenerationAttempt(_ RecordGenerationAttemptInput) (RecordGenerationAttemptResult, error) {
	_, _, err := tx.requireFromAllowed(TransitionRecordGenerationAttempt)
	if err != nil {
		return RecordGenerationAttemptResult{}, err
	}
	value, err := tx.store.IncrementChatGenerationAttempt(tx.ctx, tx.chatID)
	if err != nil {
		return RecordGenerationAttemptResult{}, xerrors.Errorf("increment generation attempt: %w", err)
	}
	return RecordGenerationAttemptResult{
		GenerationAttempt: value,
	}, nil
}

// CommitStepInput configures [Tx.CommitStep].
type CommitStepInput struct {
	Messages []Message
}

// CommitStepResult is returned by [Tx.CommitStep].
type CommitStepResult struct {
	InsertedMessages []database.ChatMessage
}

// CommitStep stores one durable message suffix while remaining
// running.
func (tx *Tx) CommitStep(input CommitStepInput) (CommitStepResult, error) {
	_, from, err := tx.requireFromAllowed(TransitionCommitStep)
	if err != nil {
		return CommitStepResult{}, err
	}
	if len(input.Messages) == 0 {
		return CommitStepResult{}, newTransitionError(
			TransitionCommitStep, from,
			"CommitStep requires at least one message",
		)
	}
	inserted, err := tx.insertMessages(input.Messages)
	if err != nil {
		return CommitStepResult{}, err
	}
	return CommitStepResult{
		InsertedMessages: inserted,
	}, nil
}

// =============================================================================
// EnterRequiresAction
// =============================================================================

// EnterRequiresActionInput is intentionally empty.
type EnterRequiresActionInput struct{}

// EnterRequiresActionResult is returned by [Tx.EnterRequiresAction].
type EnterRequiresActionResult struct {
	RequiresActionDeadlineAt sql.NullTime
}

// EnterRequiresAction parks the chat in requires_action with a
// database-time deadline of now() + 5 minutes.
func (tx *Tx) EnterRequiresAction(_ EnterRequiresActionInput) (EnterRequiresActionResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionEnterRequiresAction)
	if err != nil {
		return EnterRequiresActionResult{}, err
	}
	pending, err := pendingDynamicToolCallIDs(tx.ctx, tx.store, chat)
	if err != nil {
		return EnterRequiresActionResult{}, err
	}
	if len(pending) == 0 {
		return EnterRequiresActionResult{}, newTransitionError(
			TransitionEnterRequiresAction, from,
			"no pending dynamic tool calls",
		)
	}
	now, err := tx.store.GetDatabaseNow(tx.ctx)
	if err != nil {
		return EnterRequiresActionResult{}, xerrors.Errorf("get db now: %w", err)
	}
	deadline := sql.NullTime{Time: now.Add(5 * time.Minute), Valid: true}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRequiresAction,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: deadline,
	}); err != nil {
		return EnterRequiresActionResult{}, xerrors.Errorf("set requires_action: %w", err)
	}
	return EnterRequiresActionResult{
		RequiresActionDeadlineAt: deadline,
	}, nil
}

// =============================================================================
// FinishInterruption
// =============================================================================

// FinishInterruptionInput configures [Tx.FinishInterruption].
type FinishInterruptionInput struct {
	PartialMessages []Message
}

// FinishInterruptionResult is returned by [Tx.FinishInterruption].
type FinishInterruptionResult struct {
	InsertedMessages []database.ChatMessage
	PromotedMessage  *database.ChatMessage
}

// FinishInterruption commits an optional partial assistant/tool suffix
// and lands the chat in waiting (I0) or running with the next queued
// message promoted (I1).
func (tx *Tx) FinishInterruption(input FinishInterruptionInput) (FinishInterruptionResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionFinishInterruption)
	if err != nil {
		return FinishInterruptionResult{}, err
	}
	insertedPartial, err := tx.insertMessages(input.PartialMessages)
	if err != nil {
		return FinishInterruptionResult{}, err
	}
	pendingAll, err := pendingAllToolCallIDs(tx.ctx, tx.store, chat)
	if err != nil {
		return FinishInterruptionResult{}, err
	}
	if len(pendingAll) > 0 {
		return FinishInterruptionResult{}, newTransitionError(
			TransitionFinishInterruption, from,
			"outstanding tool calls remain after partial commit",
		)
	}

	if from == StateI0 {
		if _, err := tx.applyExecutionState(executionStateUpdate{
			Status:                   database.ChatStatusWaiting,
			Archived:                 false,
			WorkerID:                 chat.WorkerID,
			RunnerID:                 chat.RunnerID,
			LastError:                chat.LastError,
			RequiresActionDeadlineAt: sql.NullTime{},
		}); err != nil {
			return FinishInterruptionResult{}, xerrors.Errorf("set waiting: %w", err)
		}
		return FinishInterruptionResult{
			InsertedMessages: insertedPartial,
		}, nil
	}

	// I1: promote queue head into history.
	head, err := tx.store.GetChatQueuedMessageHead(tx.ctx, tx.chatID)
	if err != nil {
		return FinishInterruptionResult{}, xerrors.Errorf("get queue head: %w", err)
	}
	promotedMsg := messageFromQueuedRow(head)
	insertedHead, err := tx.insertMessages([]Message{promotedMsg})
	if err != nil {
		return FinishInterruptionResult{}, err
	}
	if _, err := tx.store.DeleteChatQueuedMessageReturningCount(tx.ctx, database.DeleteChatQueuedMessageReturningCountParams{
		ID:     head.ID,
		ChatID: tx.chatID,
	}); err != nil {
		return FinishInterruptionResult{}, xerrors.Errorf("delete promoted head: %w", err)
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return FinishInterruptionResult{}, xerrors.Errorf("set running: %w", err)
	}
	all := append([]database.ChatMessage{}, insertedPartial...)
	all = append(all, insertedHead...)
	var promoted *database.ChatMessage
	if len(insertedHead) == 1 {
		promoted = &insertedHead[0]
	}
	return FinishInterruptionResult{
		InsertedMessages: all,
		PromotedMessage:  promoted,
	}, nil
}

// =============================================================================
// FinishTurn
// =============================================================================

// FinishTurnInput is intentionally empty.
type FinishTurnInput struct{}

// FinishTurnResult is returned by [Tx.FinishTurn].
type FinishTurnResult struct {
	PromotedMessage *database.ChatMessage
}

// FinishTurn completes a running turn.
func (tx *Tx) FinishTurn(_ FinishTurnInput) (FinishTurnResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionFinishTurn)
	if err != nil {
		return FinishTurnResult{}, err
	}
	if from == StateR0 {
		if _, err := tx.applyExecutionState(executionStateUpdate{
			Status:                   database.ChatStatusWaiting,
			Archived:                 false,
			WorkerID:                 chat.WorkerID,
			RunnerID:                 chat.RunnerID,
			LastError:                chat.LastError,
			RequiresActionDeadlineAt: sql.NullTime{},
		}); err != nil {
			return FinishTurnResult{}, xerrors.Errorf("set waiting: %w", err)
		}
		return FinishTurnResult{}, nil
	}
	// R1.
	head, err := tx.store.GetChatQueuedMessageHead(tx.ctx, tx.chatID)
	if err != nil {
		return FinishTurnResult{}, xerrors.Errorf("get queue head: %w", err)
	}
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, "Tool execution interrupted by queued message promotion", false)
	if err != nil {
		return FinishTurnResult{}, err
	}
	promotedMsg := messageFromQueuedRow(head)
	inserted, err := tx.insertMessages(append(cancels, promotedMsg))
	if err != nil {
		return FinishTurnResult{}, err
	}
	if _, err := tx.store.DeleteChatQueuedMessageReturningCount(tx.ctx, database.DeleteChatQueuedMessageReturningCountParams{
		ID:     head.ID,
		ChatID: tx.chatID,
	}); err != nil {
		return FinishTurnResult{}, xerrors.Errorf("delete promoted head: %w", err)
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return FinishTurnResult{}, xerrors.Errorf("set running: %w", err)
	}
	var promoted *database.ChatMessage
	if len(inserted) > 0 {
		promoted = &inserted[len(inserted)-1]
	}
	return FinishTurnResult{
		PromotedMessage: promoted,
	}, nil
}

// =============================================================================
// FinishError
// =============================================================================

// FinishErrorInput configures [Tx.FinishError].
type FinishErrorInput struct {
	LastError pqtype.NullRawMessage
}

// FinishErrorResult is returned by [Tx.FinishError].
type FinishErrorResult struct{}

// FinishError parks the chat in error with the supplied last_error.
func (tx *Tx) FinishError(input FinishErrorInput) (FinishErrorResult, error) {
	chat, _, err := tx.requireFromAllowed(TransitionFinishError)
	if err != nil {
		return FinishErrorResult{}, err
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusError,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                input.LastError,
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return FinishErrorResult{}, xerrors.Errorf("set error: %w", err)
	}
	return FinishErrorResult{}, nil
}

// =============================================================================
// CancelRequiresAction
// =============================================================================

// CancelRequiresActionInput configures [Tx.CancelRequiresAction].
type CancelRequiresActionInput struct {
	Reason string
}

// CancelRequiresActionResult is returned by [Tx.CancelRequiresAction].
type CancelRequiresActionResult struct {
	CancellationMessages []database.ChatMessage
}

// CancelRequiresAction synthesizes cancellation results for every
// pending dynamic tool call and returns the chat to running.
func (tx *Tx) CancelRequiresAction(input CancelRequiresActionInput) (CancelRequiresActionResult, error) {
	chat, from, err := tx.requireFromAllowed(TransitionCancelRequiresAction)
	if err != nil {
		return CancelRequiresActionResult{}, err
	}
	reason := input.Reason
	if reason == "" {
		reason = "Tool execution timed out"
	}
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, reason, true)
	if err != nil {
		return CancelRequiresActionResult{}, err
	}
	if len(cancels) == 0 {
		return CancelRequiresActionResult{}, newTransitionError(
			TransitionCancelRequiresAction, from,
			"no pending dynamic tool calls to cancel",
		)
	}
	inserted, err := tx.insertMessages(cancels)
	if err != nil {
		return CancelRequiresActionResult{}, err
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusRunning,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                chat.LastError,
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return CancelRequiresActionResult{}, xerrors.Errorf("set running: %w", err)
	}
	return CancelRequiresActionResult{
		CancellationMessages: inserted,
	}, nil
}

// =============================================================================
// ReconcileInvalidState
// =============================================================================

// ReconcileInvalidStateInput configures [Tx.ReconcileInvalidState].
type ReconcileInvalidStateInput struct {
	LastError          pqtype.NullRawMessage
	CancellationReason string
}

// ReconcileInvalidStateResult is returned by [Tx.ReconcileInvalidState].
type ReconcileInvalidStateResult struct {
	CancellationMessages []database.ChatMessage
}

// ReconcileInvalidState moves an invalid execution-state combination
// into a valid error state. Queued messages are preserved; pending
// dynamic-tool calls are closed with synthetic cancellation results.
func (tx *Tx) ReconcileInvalidState(input ReconcileInvalidStateInput) (ReconcileInvalidStateResult, error) {
	chat, from, err := tx.loadState()
	if err != nil {
		return ReconcileInvalidStateResult{}, err
	}
	if from != StateInvalid {
		return ReconcileInvalidStateResult{}, newTransitionError(
			TransitionReconcileInvalidState, from,
			"reconcile is only valid for invalid states",
		)
	}
	reason := input.CancellationReason
	if reason == "" {
		reason = "Tool execution canceled due to invalid chat state"
	}
	cancels, err := synthesizePendingToolCancellations(tx.ctx, tx.store, chat, reason, true)
	if err != nil {
		return ReconcileInvalidStateResult{}, err
	}
	var inserted []database.ChatMessage
	if len(cancels) > 0 {
		inserted, err = tx.insertMessages(cancels)
		if err != nil {
			return ReconcileInvalidStateResult{}, err
		}
	}
	lastErr := input.LastError
	if !lastErr.Valid {
		lastErr = pqtype.NullRawMessage{
			RawMessage: json.RawMessage(`{"message":"chat was in an invalid state; send a new message or edit history to continue"}`),
			Valid:      true,
		}
	}
	if _, err := tx.applyExecutionState(executionStateUpdate{
		Status:                   database.ChatStatusError,
		Archived:                 false,
		WorkerID:                 chat.WorkerID,
		RunnerID:                 chat.RunnerID,
		LastError:                lastErr,
		RequiresActionDeadlineAt: sql.NullTime{},
	}); err != nil {
		return ReconcileInvalidStateResult{}, xerrors.Errorf("set error: %w", err)
	}
	return ReconcileInvalidStateResult{
		CancellationMessages: inserted,
	}, nil
}
