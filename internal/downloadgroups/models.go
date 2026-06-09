package downloadgroups

import (
	"goaria-v3/internal/rpc"
)

const (
	DownloadGroupNameStatusStable   = rpc.DownloadGroupNameStatusStable
	DownloadGroupNameStatusPending  = rpc.DownloadGroupNameStatusPending
	DownloadGroupNameStatusFallback = rpc.DownloadGroupNameStatusFallback
	DownloadGroupNameStatusDegraded = rpc.DownloadGroupNameStatusDegraded

	DownloadGroupStatusUnknown  = "unknown"
	DownloadGroupStatusActive   = "active"
	DownloadGroupStatusPaused   = "paused"
	DownloadGroupStatusWaiting  = "waiting"
	DownloadGroupStatusError    = "error"
	DownloadGroupStatusComplete = "complete"

	DownloadGroupWarningMixedStatus     = "mixed_status"
	DownloadGroupWarningPartialError    = "partial_error"
	DownloadGroupWarningMissingMembers  = "missing_members"
	DownloadGroupWarningMissingMetadata = "missing_metadata"
	DownloadGroupWarningHistoryOnly     = "history_only"
	DownloadGroupWarningStaleGroup      = "stale_group"
	DownloadGroupWarningNamePending     = "name_pending"
	DownloadGroupWarningNameDegraded    = "name_degraded"
	DownloadGroupWarningGroupNotFound   = "group_not_found"
)

const (
	DownloadGroupOperationActionPause      = "pause"
	DownloadGroupOperationActionResume     = "resume"
	DownloadGroupOperationActionRemove     = "remove"
	DownloadGroupOperationActionOpenFolder = "open_folder"

	DownloadGroupOperationItemSucceeded = "succeeded"
	DownloadGroupOperationItemSkipped   = "skipped"
	DownloadGroupOperationItemFailed    = "failed"

	DownloadGroupOperationCodeGroupNotFound       = "group_not_found"
	DownloadGroupOperationCodeEmptyGroup          = "empty_group"
	DownloadGroupOperationCodeNoActionableMembers = "no_actionable_members"
	DownloadGroupOperationCodeStaleMember         = "stale_member"
	DownloadGroupOperationCodeMissingMember       = "missing_member"
	DownloadGroupOperationCodePartialFailure      = "partial_failure"
	DownloadGroupOperationCodeRPCError            = "rpc_error"
	DownloadGroupOperationCodePaused              = "paused"
	DownloadGroupOperationCodeAlreadyPaused       = "already_paused"
	DownloadGroupOperationCodeTerminalState       = "terminal_state"
	DownloadGroupOperationCodeHistoryOnly         = "history_only"
	DownloadGroupOperationCodeResumed             = "resumed"
	DownloadGroupOperationCodeAlreadyActive       = "already_active"
	DownloadGroupOperationCodeNotPaused           = "not_paused"
	DownloadGroupOperationCodeRemoved             = "removed"
	DownloadGroupOperationCodeRemovedStale        = "removed_stale_metadata"
	DownloadGroupOperationCodeRemoveAccepted      = "remove_accepted"
	DownloadGroupOperationCodeOpened              = "opened"
	DownloadGroupOperationCodeFolderUnavailable   = "folder_unavailable"
	DownloadGroupOperationCodeFolderUnsafe        = "folder_unsafe"
	DownloadGroupOperationCodeOpenFailed          = "open_failed"
)

type DownloadGroupListEnvelope struct {
	Groups    []DownloadGroupCard    `json:"groups"`
	UpdatedAt int64                  `json:"updated_at"`
	Degraded  bool                   `json:"degraded"`
	Warnings  []DownloadGroupWarning `json:"warnings,omitempty"`
}

type DownloadGroupDetailEnvelope struct {
	GroupKey  string                 `json:"group_key"`
	Found     bool                   `json:"found"`
	Group     DownloadGroupCard      `json:"group"`
	Tasks     DownloadGroupTaskLists `json:"tasks"`
	UpdatedAt int64                  `json:"updated_at"`
	Degraded  bool                   `json:"degraded"`
	Warnings  []DownloadGroupWarning `json:"warnings,omitempty"`
}

type DownloadGroupTaskLists struct {
	Active  []rpc.Task `json:"active"`
	Waiting []rpc.Task `json:"waiting"`
	Stopped []rpc.Task `json:"stopped"`
}

type DownloadGroupCard struct {
	GroupKey        string                    `json:"group_key"`
	DownloadGroup   *rpc.DownloadGroup        `json:"download_group,omitempty"`
	Kind            string                    `json:"kind"`
	DisplayName     string                    `json:"display_name"`
	FallbackName    string                    `json:"fallback_name"`
	NameStatus      string                    `json:"name_status"`
	Status          string                    `json:"status"`
	Degraded        bool                      `json:"degraded"`
	Warnings        []DownloadGroupWarning    `json:"warnings,omitempty"`
	Counts          DownloadGroupMemberCounts `json:"counts"`
	TotalLength     string                    `json:"total_length"`
	CompletedLength string                    `json:"completed_length"`
	DownloadSpeed   string                    `json:"download_speed"`
	Progress        float64                   `json:"progress"`
	CreatedAt       int64                     `json:"created_at"`
	UpdatedAt       int64                     `json:"updated_at"`
	FolderLabel     string                    `json:"folder_label,omitempty"`
	FolderPathHint  string                    `json:"folder_path_hint,omitempty"`
	HasFolder       bool                      `json:"has_folder"`
}

type DownloadGroupMemberCounts struct {
	Expected    int `json:"expected"`
	Resolved    int `json:"resolved"`
	Missing     int `json:"missing"`
	Active      int `json:"active"`
	Waiting     int `json:"waiting"`
	Paused      int `json:"paused"`
	Complete    int `json:"complete"`
	Error       int `json:"error"`
	HistoryOnly int `json:"history_only"`
}

type DownloadGroupWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Count    int    `json:"count,omitempty"`
}

type DownloadGroupOperationResult struct {
	GroupKey     string                             `json:"group_key"`
	Action       string                             `json:"action"`
	OK           bool                               `json:"ok"`
	Found        bool                               `json:"found"`
	Noop         bool                               `json:"noop"`
	TotalTargets int                                `json:"total_targets"`
	Succeeded    int                                `json:"succeeded"`
	Skipped      int                                `json:"skipped"`
	Failed       int                                `json:"failed"`
	Items        []DownloadGroupOperationItemResult `json:"items,omitempty"`
	Warnings     []DownloadGroupWarning             `json:"warnings,omitempty"`
	Refresh      DownloadGroupOperationRefreshHint  `json:"refresh"`
	UpdatedAt    int64                              `json:"updated_at"`

	attempted bool
}

type DownloadGroupOperationItemResult struct {
	GID     string `json:"gid,omitempty"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type DownloadGroupOperationRefreshHint struct {
	Tasks  bool   `json:"tasks"`
	Groups bool   `json:"groups"`
	Detail bool   `json:"detail"`
	Reason string `json:"reason,omitempty"`
}
