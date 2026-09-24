package codexsup

// Client-to-server methods this package sends. TestMethodNamesInSchema checks
// them against the committed schema's ClientRequest and ClientNotification.
const (
	methodInitialize    = "initialize"
	methodInitialized   = "initialized"
	methodThreadStart   = "thread/start"
	methodThreadResume  = "thread/resume"
	methodTurnStart     = "turn/start"
	methodTurnInterrupt = "turn/interrupt"
	methodAccountRead   = "account/read"
)

var clientRequests = []string{
	methodInitialize, methodThreadStart, methodThreadResume, methodTurnStart, methodTurnInterrupt,
	methodAccountRead,
}

var clientNotifications = []string{methodInitialized}

// Server request methods with a default answer other than an error.
const (
	methodCommandApproval     = "item/commandExecution/requestApproval"
	methodFileChangeApproval  = "item/fileChange/requestApproval"
	methodPermissionsApproval = "item/permissions/requestApproval"
	methodApplyPatchApproval  = "applyPatchApproval"
	methodExecCommandApproval = "execCommandApproval"
)

// serverRequests is every ServerRequest method at Codex 0.156.1. Each is
// registered on the transport so it reaches OnServerRequest or the default
// decline; an unlisted method is answered method-not-found by acp. The
// method-name test requires this list to equal the schema's exactly.
var serverRequests = []string{
	methodCommandApproval, methodFileChangeApproval, methodPermissionsApproval,
	methodApplyPatchApproval, methodExecCommandApproval,
	"item/tool/requestUserInput", "mcpServer/elicitation/request", "item/tool/call",
	"account/chatgptAuthTokens/refresh", "attestation/generate",
}

// serverNotifications is every ServerNotification method at Codex 0.156.1.
// acp drops a notification whose method is not registered, so this list is
// what reaches OnNotification; the method-name test requires it to equal the
// schema's exactly.
var serverNotifications = []string{
	"error", "thread/started", "thread/status/changed", "thread/archived",
	"thread/deleted", "thread/unarchived", "thread/closed", "thread/reverted",
	"skills/changed", "thread/name/updated", "thread/attachment/updated",
	"thread/goal/updated", "thread/goal/cleared", "thread/queue/changed",
	"project/changed", "thread/project/updated", "thread/environment/connected",
	"thread/environment/disconnected", "thread/settings/updated",
	"thread/tokenUsage/updated", "turn/started", "hook/started", "turn/completed",
	"hook/completed", "turn/diff/updated", "turn/plan/updated", "item/started",
	"item/autoApprovalReview/started", "item/autoApprovalReview/completed",
	"autoApprovalReview/strictReviewRequired", "item/completed",
	"item/agentMessage/delta", "item/plan/delta", "command/exec/outputDelta",
	"process/outputDelta", "process/exited", "item/commandExecution/outputDelta",
	"item/commandExecution/terminalInteraction", "item/fileChange/outputDelta",
	"item/fileChange/patchUpdated", "serverRequest/resolved",
	"item/mcpToolCall/progress", "mcpServer/oauthLogin/completed",
	"mcpServer/startupStatus/updated", "mcpServer/event/stream/notification",
	"account/updated", "account/rateLimits/updated", "app/list/updated",
	"remoteControl/status/changed", "externalAgentConfig/import/progress",
	"externalAgentConfig/import/completed", "fs/changed",
	"item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded",
	"item/reasoning/textDelta", "thread/compacted", "model/rerouted",
	"model/verification", "modelProvider/authRecoveryStarted",
	"modelProvider/authRecoveryCompleted", "turn/moderationMetadata",
	"model/safetyBuffering/updated", "warning", "guardianWarning",
	"deprecationNotice", "configWarning", "fuzzyFileSearch/sessionUpdated",
	"fuzzyFileSearch/sessionCompleted", "thread/realtime/started",
	"thread/realtime/itemAdded", "thread/realtime/item/started",
	"thread/realtime/item/transcript/delta", "thread/realtime/item/completed",
	"thread/realtime/transcript/delta", "thread/realtime/transcript/done",
	"thread/realtime/outputAudio/delta", "thread/realtime/sdp",
	"thread/realtime/error", "thread/realtime/closed",
	"windows/worldWritableWarning", "windowsSandbox/setupCompleted",
	"account/login/completed",
}
