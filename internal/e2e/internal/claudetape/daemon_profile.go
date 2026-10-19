package claudetape

type DaemonSuiteProfile struct {
	Root  string
	Roles []string
}

func DaemonSuiteProfiles() map[string]DaemonSuiteProfile {
	return map[string]DaemonSuiteProfile{
		"conversation-default":     {"TestInteractivePerConversationLiveness_Default", []string{"bootstrap", "created-1"}},
		"conversation-workspace":   {"TestInteractivePerConversationLiveness_WorkspaceFolder", []string{"bootstrap", "created-1"}},
		"conversation-turn-state":  {"TestInteractivePerConversationTurnStateLiveness", []string{"bootstrap", "created-1"}},
		"conversation-prompt-argv": {"TestInteractiveSystemPromptFile_LiveSpawnArgv", []string{"bootstrap", "created-1"}},
	}
}
