package client

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The user-model inspection surface: plain client-owned structs mirroring the
// proto UserModelEntry + GetUserModelResponse, the unary RPC wrapper that maps
// proto → the structs, and the tea.Cmd constructor the ui's /usermodel panel
// calls. As with the soul/skills surfaces, NO proto type leaks past this file.

// UserModelEntry is one user-model fact's listing metadata (proto UserModelEntry,
// proto-free): its key + one-line description. The value is omitted — discovery is
// metadata only.
type UserModelEntry struct {
	Key         string
	Description string
}

// UserModelRevision is one proto-free lifecycle revision for read-only display.
type UserModelRevision struct {
	Key, Value, Description, Version, Status, Writer, Origin string
	SourceSessionID, SourceProposalID                        string
	UpdatedAt                                                time.Time
}

// UserModelDetail is an exact current value plus bounded lifecycle history.
type UserModelDetail struct {
	Current          UserModelRevision
	History          []UserModelRevision
	HistoryAvailable bool
}

// UserModel is the user-model index snapshot: the current entries plus aggregate
// size + hash, so the panel can show an at-a-glance footprint.
type UserModel struct {
	Entries   []UserModelEntry
	SizeBytes int64
	SHA256    string
	Detail    *UserModelDetail
}

// UserModelMsg carries a GetUserModel result for the /usermodel panel. Err is set
// on failure; the panel surfaces it rather than silently degrading.
type UserModelMsg struct {
	UserModel  UserModel
	Err        error
	Generation uint64
}

// UserModelDetailMsg is a distinct correlated exact-entry response.
type UserModelDetailMsg struct {
	UserModel  UserModel
	Err        error
	RequestKey string
	Generation uint64
}

// GetUserModel fetches the current user-model index (a LIVE read server-side).
func (c *Client) GetUserModel(ctx context.Context) (UserModel, error) {
	return c.getUserModel(ctx, "")
}

// GetUserModelEntry fetches exact read-only detail. Older servers ignore the
// additive key and return Detail=nil, which the UI reports honestly.
func (c *Client) GetUserModelEntry(ctx context.Context, key string) (UserModel, error) {
	return c.getUserModel(ctx, key)
}

func (c *Client) getUserModel(ctx context.Context, key string) (UserModel, error) {
	resp, err := c.svc.GetUserModel(ctx, &mecatlv1.GetUserModelRequest{Key: key})
	if err != nil {
		return UserModel{}, err
	}
	return mapUserModel(resp), nil
}

// mapUserModel maps a proto GetUserModelResponse (nil-safe) to the plain struct.
func mapUserModel(in *mecatlv1.GetUserModelResponse) UserModel {
	if in == nil {
		return UserModel{}
	}
	entries := in.GetEntries()
	out := make([]UserModelEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, UserModelEntry{Key: e.GetKey(), Description: e.GetDescription()})
	}
	result := UserModel{
		Entries:   out,
		SizeBytes: in.GetSizeBytes(),
		SHA256:    in.GetSha256(),
	}
	if detail := in.GetDetail(); detail != nil {
		history := make([]UserModelRevision, len(detail.GetHistory()))
		for i, revision := range detail.GetHistory() {
			history[i] = mapUserModelRevision(revision)
		}
		result.Detail = &UserModelDetail{Current: mapUserModelRevision(detail.GetCurrent()), History: history, HistoryAvailable: detail.GetHistoryAvailable()}
	}
	return result
}

func mapUserModelRevision(in *mecatlv1.UserModelRevision) UserModelRevision {
	if in == nil {
		return UserModelRevision{}
	}
	var updated time.Time
	if ts := in.GetUpdatedAt(); ts != nil && ts.IsValid() {
		updated = ts.AsTime()
	}
	return UserModelRevision{Key: in.GetKey(), Value: in.GetValue(), Description: in.GetDescription(), Version: in.GetVersion(), Status: in.GetStatus(), Writer: in.GetWriter(), Origin: in.GetOrigin(), SourceSessionID: in.GetSourceSessionId(), SourceProposalID: in.GetSourceProposalId(), UpdatedAt: updated}
}

// UserModelLister is the subset of *Client the ui's /usermodel panel needs.
// Splitting it out keeps the ui injectable with a fake for offline tests; *Client
// satisfies it.
type UserModelLister interface {
	GetUserModel(ctx context.Context) (UserModel, error)
}

// UserModelDetailer fetches exact read-only detail for one selected key.
type UserModelDetailer interface {
	GetUserModelEntry(ctx context.Context, key string) (UserModel, error)
}

// GetUserModelCmd fetches the user-model index off the update goroutine; the
// result (success or error) arrives as a UserModelMsg.
func GetUserModelCmd(ctx context.Context, c UserModelLister) tea.Cmd {
	return GetUserModelCmdTagged(ctx, c, 0)
}

// GetUserModelCmdTagged correlates an inventory response with one overlay opening.
func GetUserModelCmdTagged(ctx context.Context, c UserModelLister, generation uint64) tea.Cmd {
	return func() tea.Msg {
		um, err := c.GetUserModel(ctx)
		if err != nil {
			return UserModelMsg{Err: err, Generation: generation}
		}
		return UserModelMsg{UserModel: um, Generation: generation}
	}
}

// GetUserModelEntryCmd fetches selected-entry detail off the update goroutine.
func GetUserModelEntryCmd(ctx context.Context, c UserModelDetailer, key string) tea.Cmd {
	return GetUserModelEntryCmdTagged(ctx, c, key, 0)
}

// GetUserModelEntryCmdTagged correlates exact detail with its selected key and
// overlay generation so delayed responses cannot replace newer state.
func GetUserModelEntryCmdTagged(ctx context.Context, c UserModelDetailer, key string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		um, err := c.GetUserModelEntry(ctx, key)
		if err != nil {
			return UserModelDetailMsg{Err: err, RequestKey: key, Generation: generation}
		}
		return UserModelDetailMsg{UserModel: um, RequestKey: key, Generation: generation}
	}
}
