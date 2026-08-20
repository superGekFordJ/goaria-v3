package extension

func SanitizeCommitItemError(msg string) string {
	if msg == CommitItemErrorNotAllowed {
		return CommitItemErrorNotAllowed
	}
	return CommitItemErrorAddFailed
}

func SanitizeCommitItemErrors(errors map[string]string) map[string]string {
	if len(errors) == 0 {
		return nil
	}
	out := make(map[string]string, len(errors))
	for id, msg := range errors {
		out[id] = SanitizeCommitItemError(msg)
	}
	return out
}
