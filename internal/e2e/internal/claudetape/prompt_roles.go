package claudetape

// These are the original root's two persisted conversation/session identities.
func validPromptRole(b LifecycleRoleBinding) bool {
	switch b.Role {
	case "bare":
		return b.SessionID == "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb" && b.ConversationID == "bbbbbbbb-1111-4111-8111-bbbbbbbbbbbb"
	case "prompted":
		return b.SessionID == "aaaaaaaa-2222-4222-8222-aaaaaaaaaaaa" && b.ConversationID == "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	}
	return false
}
