package monitor

// ActiveBandwidthByScope returns the sum of DownloadSpeed for all active tasks
// whose tracked scope and envKey match the given parameters.
//
// Tasks without a tracked scope are skipped (conservative:宁可少计也不阻塞,
// avoids hot-path DNS re-classification).
func ActiveBandwidthByScope(scope, envKey string) int64 {
	tr := State.GetTracker()
	if tr == nil || Cache == nil {
		return 0
	}
	var sum int64
	for _, t := range Cache.GetActive() {
		s, _, e, ok := tr.GetScopeAndEnv(t.GID)
		if !ok || s != scope || e != envKey {
			continue
		}
		sum += parseInt64(t.DownloadSpeed)
	}
	return sum
}
