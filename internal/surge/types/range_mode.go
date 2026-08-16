package types

// RangeAcquisitionMode is the persisted policy for how Range capability is
// obtained. The gob zero-value (empty string) is ProbeAtEnqueue.
//
// FORK-PATCH: payload-first Range verification. Do not treat SupportsRange=false
// as proven unsupported — that pointer historically meant "no proof yet".
type RangeAcquisitionMode string

const (
	RangeAcquireProbeAtEnqueue      RangeAcquisitionMode = ""
	RangeAcquirePayloadFirstUnknown RangeAcquisitionMode = "payload_first_unknown"
	RangeAcquireRangeSupported      RangeAcquisitionMode = "range_supported"
	RangeAcquireRangeUnsupported    RangeAcquisitionMode = "range_unsupported"
)

// ResolveRangeAcquisitionMode maps a request mode plus the 286 *bool hint.
// Empty mode: nil → ProbeAtEnqueue; *true → RangeSupported; *false → PayloadFirstUnknown.
func ResolveRangeAcquisitionMode(mode RangeAcquisitionMode, supportsRange *bool) RangeAcquisitionMode {
	if mode != RangeAcquireProbeAtEnqueue {
		return mode
	}
	if supportsRange == nil {
		return RangeAcquireProbeAtEnqueue
	}
	if *supportsRange {
		return RangeAcquireRangeSupported
	}
	return RangeAcquirePayloadFirstUnknown
}

// SkipsServerProbe reports whether this mode (or a 286 non-nil pointer) skips
// add-time ProbeServerWithProxy. Size+filename gating stays with the caller.
func (m RangeAcquisitionMode) SkipsServerProbe() bool {
	switch m {
	case RangeAcquirePayloadFirstUnknown, RangeAcquireRangeSupported, RangeAcquireRangeUnsupported:
		return true
	default:
		return false
	}
}

// ShouldSkipServerProbe is the engine skip predicate after size+filename hold.
func ShouldSkipServerProbe(mode RangeAcquisitionMode, supportsRange *bool) bool {
	return ResolveRangeAcquisitionMode(mode, supportsRange).SkipsServerProbe() || supportsRange != nil
}

// ShouldUseConcurrent selects ConcurrentDownloader vs SingleDownloader.
// Empty mode keeps pre-287 in-memory behavior (SupportsRange bool).
func ShouldUseConcurrent(mode RangeAcquisitionMode, supportsRange bool) bool {
	switch mode {
	case RangeAcquirePayloadFirstUnknown, RangeAcquireRangeSupported:
		return true
	case RangeAcquireRangeUnsupported:
		return false
	default:
		return supportsRange
	}
}

// IsPayloadFirst reports whether Range is still unproven and must be verified
// by the first real payload shard.
func (m RangeAcquisitionMode) IsPayloadFirst() bool {
	return m == RangeAcquirePayloadFirstUnknown
}

// UpgradeLegacyRangeMode maps a gob record that predates this field.
// Tasks snapshot → RangeSupported; no snapshot → ProbeAtEnqueue (old sequential).
// Never invents PayloadFirstUnknown for old tasks.
func UpgradeLegacyRangeMode(mode RangeAcquisitionMode, hasTasks bool) RangeAcquisitionMode {
	if mode != RangeAcquireProbeAtEnqueue {
		return mode
	}
	if hasTasks {
		return RangeAcquireRangeSupported
	}
	return RangeAcquireProbeAtEnqueue
}
