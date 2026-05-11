package extractor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	maxHostImportRequestBytes  = 64 * 1024
	maxHostImportResponseBytes = 2 * 1024 * 1024
	maxHostImportParams        = 16
	maxHostImportParamKeyBytes = 64
	maxHostImportParamValBytes = 1024
)

type hostImportRequestMode int

const (
	hostImportRequestModeInvalid hostImportRequestMode = iota
	hostImportRequestModeRaw
	hostImportRequestModeRef
)

type RunnerConfig struct {
	HTTPBroker         *HTTPBroker
	AuthResolver       AuthProfileResolver
	HostPolicyResolver HostPolicyResolver
}

type HostImportConfig struct {
	HTTPBroker         *HTTPBroker
	AuthResolver       AuthProfileResolver
	HostPolicyResolver HostPolicyResolver
}

type HostHTTPFetchRequest struct {
	Method           string            `json:"method,omitempty"`
	URL              string            `json:"url,omitempty"`
	BrokerPolicyRef  string            `json:"broker_policy_ref,omitempty"`
	EndpointRef      string            `json:"endpoint_ref,omitempty"`
	Params           map[string]string `json:"params,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	AuthProfileRef   string            `json:"auth_profile_ref,omitempty"`
	TimeoutMillis    int               `json:"timeout_millis,omitempty"`
	MaxResponseBytes int64             `json:"max_response_bytes,omitempty"`
}

type HostHTTPFetchResponse struct {
	OK         bool                `json:"ok"`
	StatusCode int                 `json:"status_code,omitempty"`
	FinalURL   string              `json:"final_url,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	BodyBase64 string              `json:"body_base64,omitempty"`
	ErrorCode  string              `json:"error_code,omitempty"`
	Message    string              `json:"message,omitempty"`
}

type HostAuthProfileStatusRequest struct {
	AuthProfileRef  string            `json:"auth_profile_ref,omitempty"`
	URL             string            `json:"url,omitempty"`
	BrokerPolicyRef string            `json:"broker_policy_ref,omitempty"`
	EndpointRef     string            `json:"endpoint_ref,omitempty"`
	Params          map[string]string `json:"params,omitempty"`
}

type HostAuthProfileStatusResponse struct {
	OK              bool           `json:"ok"`
	Available       bool           `json:"available,omitempty"`
	Kind            AuthSecretKind `json:"kind,omitempty"`
	RedactedDisplay string         `json:"redacted_display,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
	Message         string         `json:"message,omitempty"`
}

type hostImportBridge struct {
	manifest           Manifest
	identity           VerifiedPackIdentity
	packID             string
	budget             *HostCallBudget
	httpBroker         *HTTPBroker
	authResolver       AuthProfileResolver
	hostPolicyResolver HostPolicyResolver
	resolvedPolicy     *ResolvedHostPolicy
	maxRequestBytes    int
	maxResponseBytes   int
}

func newHostImportBridge(manifest Manifest, identity VerifiedPackIdentity, budget *HostCallBudget, config HostImportConfig) *hostImportBridge {
	return &hostImportBridge{
		manifest:           manifest,
		identity:           identity,
		packID:             manifest.PackID,
		budget:             budget,
		httpBroker:         config.HTTPBroker,
		authResolver:       config.AuthResolver,
		hostPolicyResolver: config.HostPolicyResolver,
		maxRequestBytes:    maxHostImportRequestBytes,
		maxResponseBytes:   maxHostImportResponseBytes,
	}
}

func (b *hostImportBridge) resolveHostPolicy(ctx context.Context) (ResolvedHostPolicy, error) {
	if b == nil {
		return ResolvedHostPolicy{}, errors.New("host import bridge is not configured")
	}
	if b.resolvedPolicy != nil {
		return cloneResolvedHostPolicy(*b.resolvedPolicy), nil
	}
	policy, err := resolveAliasHostPolicy(ctx, b.hostPolicyResolver, b.identity, b.manifest)
	if err != nil {
		return ResolvedHostPolicy{}, errors.New("host policy denied")
	}
	cloned := cloneResolvedHostPolicy(policy)
	b.resolvedPolicy = &cloned

	return cloneResolvedHostPolicy(cloned), nil
}

func classifyHostImportRequestMode(rawURL string, brokerPolicyRef string, endpointRef string) (hostImportRequestMode, error) {
	hasURL := rawURL != ""
	hasBrokerRef := brokerPolicyRef != ""
	hasEndpointRef := endpointRef != ""
	if hasURL && (hasBrokerRef || hasEndpointRef) {
		return hostImportRequestModeInvalid, errors.New("host import request must not mix url and refs")
	}
	if hasURL {
		return hostImportRequestModeRaw, nil
	}
	if hasBrokerRef && hasEndpointRef {
		return hostImportRequestModeRef, nil
	}
	if hasBrokerRef || hasEndpointRef {
		return hostImportRequestModeInvalid, errors.New("host import ref mode requires broker_policy_ref and endpoint_ref")
	}

	return hostImportRequestModeInvalid, errors.New("host import request requires url or refs")
}

func validateHostImportRequestMode(manifest Manifest, rawURL string, brokerPolicyRef string, endpointRef string) (hostImportRequestMode, error) {
	mode, err := classifyHostImportRequestMode(rawURL, brokerPolicyRef, endpointRef)
	if err != nil {
		return hostImportRequestModeInvalid, err
	}
	if isAliasManifest(manifest) {
		if mode != hostImportRequestModeRef {
			return hostImportRequestModeInvalid, errors.New("alias manifest requires ref-mode host imports")
		}
		return mode, nil
	}
	if mode != hostImportRequestModeRaw {
		return hostImportRequestModeInvalid, errors.New("legacy manifest requires raw url host imports")
	}

	return mode, nil
}

func validateHostImportRefs(brokerPolicyRef string, endpointRef string) error {
	if err := validateOpaquePolicyRef("broker_policy_ref", brokerPolicyRef); err != nil {
		return err
	}
	if err := validateOpaquePolicyRef("endpoint_ref", endpointRef); err != nil {
		return err
	}

	return nil
}

func validateHostImportParams(params map[string]string) error {
	if len(params) > maxHostImportParams {
		return fmt.Errorf("params must not contain more than %d entries", maxHostImportParams)
	}
	for key, value := range params {
		if len(key) < 1 || len(key) > maxHostImportParamKeyBytes {
			return fmt.Errorf("params key length must be between 1 and %d bytes", maxHostImportParamKeyBytes)
		}
		if len(value) > maxHostImportParamValBytes {
			return fmt.Errorf("params value exceeds %d bytes", maxHostImportParamValBytes)
		}
		if err := validateHostImportParamScalar(key, "params key"); err != nil {
			return err
		}
		if err := validateHostImportParamScalar(value, "params value"); err != nil {
			return err
		}
		if isCredentialShapedMetadataKey(key) {
			return errors.New("params key is credential-shaped")
		}
	}

	return nil
}

func validateHostImportParamScalar(value string, field string) error {
	if strings.Contains(value, "://") || strings.Contains(value, `\`) {
		return fmt.Errorf("%s contains unsupported marker", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}

	return nil
}

func (b *hostImportBridge) executeHTTPFetch(ctx context.Context, requestBytes []byte) []byte {
	if err := b.consumeBudget(); err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "budget_exhausted", Message: RedactSensitive(err.Error())}, b.responseCap())
	}

	var request HostHTTPFetchRequest
	if err := b.decodeRequestStrict(requestBytes, &request); err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	mode, err := validateHostImportRequestMode(b.manifest, request.URL, request.BrokerPolicyRef, request.EndpointRef)
	if err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	if mode == hostImportRequestModeRef {
		if err := validateHostImportRefs(request.BrokerPolicyRef, request.EndpointRef); err != nil {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
		if err := validateHostImportParams(request.Params); err != nil {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
	} else if len(request.Params) > 0 {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: "params require ref-mode host import"}, b.responseCap())
	}
	if request.AuthProfileRef != "" {
		if err := validateAuthProfileID(AuthProfileID(request.AuthProfileRef)); err != nil {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
	}
	if request.TimeoutMillis < 0 {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: "timeout_millis must not be negative"}, b.responseCap())
	}
	if request.MaxResponseBytes < 0 {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: "max_response_bytes must not be negative"}, b.responseCap())
	}
	if b.httpBroker == nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "not_configured", Message: "http broker is not configured"}, b.responseCap())
	}

	var timeout time.Duration
	if request.TimeoutMillis > 0 {
		timeout = time.Duration(request.TimeoutMillis) * time.Millisecond
	}
	var response HTTPFetchResponse
	var fetchErr error
	if mode == hostImportRequestModeRef {
		policy, err := b.resolveHostPolicy(ctx)
		if err != nil {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy denied"}, b.responseCap())
		}
		response, fetchErr = b.httpBroker.FetchRef(ctx, HTTPFetchRefRequest{
			PackID:           b.packID,
			Manifest:         b.manifest,
			PackIdentity:     b.identity,
			HostPolicy:       policy,
			BrokerPolicyRef:  request.BrokerPolicyRef,
			EndpointRef:      request.EndpointRef,
			Params:           request.Params,
			Method:           request.Method,
			Headers:          request.Headers,
			AuthProfileID:    AuthProfileID(request.AuthProfileRef),
			Timeout:          timeout,
			MaxResponseBytes: request.MaxResponseBytes,
		})
	} else {
		response, fetchErr = b.httpBroker.Fetch(ctx, HTTPFetchRequest{
			PackID:           b.packID,
			Manifest:         b.manifest,
			Method:           request.Method,
			URL:              request.URL,
			Headers:          request.Headers,
			AuthProfileID:    AuthProfileID(request.AuthProfileRef),
			Timeout:          timeout,
			MaxResponseBytes: request.MaxResponseBytes,
		})
	}
	if fetchErr != nil {
		if request.AuthProfileRef != "" {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "authenticated_fetch_failed", Message: "authenticated fetch failed"}, b.responseCap())
		}

		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "fetch_failed", Message: RedactSensitive(fetchErr.Error())}, b.responseCap())
	}

	return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{
		OK:         true,
		StatusCode: response.StatusCode,
		FinalURL:   response.FinalURL,
		Headers:    safeHeaderMap(response.Headers),
		BodyBase64: base64.StdEncoding.EncodeToString(response.Body),
	}, b.responseCap())
}

func (b *hostImportBridge) executeAuthProfileStatus(ctx context.Context, requestBytes []byte) []byte {
	if err := b.consumeBudget(); err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "budget_exhausted", Message: RedactSensitive(err.Error())}, b.responseCap())
	}

	var request HostAuthProfileStatusRequest
	if err := b.decodeRequestStrict(requestBytes, &request); err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	mode, err := validateHostImportRequestMode(b.manifest, request.URL, request.BrokerPolicyRef, request.EndpointRef)
	if err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	if mode == hostImportRequestModeRef {
		if err := validateHostImportRefs(request.BrokerPolicyRef, request.EndpointRef); err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
		if err := validateHostImportParams(request.Params); err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
	} else if len(request.Params) > 0 {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: "params require ref-mode host import"}, b.responseCap())
	}
	profileID := AuthProfileID(request.AuthProfileRef)
	if err := validateAuthProfileID(profileID); err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	targetURL := request.URL
	if mode == hostImportRequestModeRef {
		policy, err := b.resolveHostPolicy(ctx)
		if err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy denied"}, b.responseCap())
		}
		if err := validateResolvedHostPolicyBinding(b.identity, b.manifest, policy); err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy denied"}, b.responseCap())
		}
		if !manifestHasCapability(b.manifest, CapabilityAuthProfile) || !policyAllowsCapability(policy, CapabilityAuthProfile) {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy denied"}, b.responseCap())
		}
		endpoint, ok := findBrokerEndpoint(policy, request.BrokerPolicyRef, request.EndpointRef)
		if !ok || !endpointAllowsAuthProfile(endpoint, profileID) {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy denied"}, b.responseCap())
		}
		targetURL, err = expandBrokerEndpointURL(policy, endpoint, request.Params)
		if err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
		parsed, _, err := parseSafeHTTPURL(targetURL)
		if err != nil || parsed.Scheme != "https" {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy denied"}, b.responseCap())
		}
	} else {
		if err := ValidateCapabilityURL(CapabilityContext{
			PackID:     b.packID,
			Manifest:   b.manifest,
			Capability: CapabilityAuthProfile,
		}, request.URL); err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
	}
	if b.authResolver == nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "not_configured", Message: "auth resolver is not configured"}, b.responseCap())
	}

	resolved, err := b.authResolver.ResolveAuthProfile(ctx, b.packID, profileID, targetURL)
	if err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "auth_unavailable", Message: "auth profile unavailable"}, b.responseCap())
	}

	return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{
		OK:              true,
		Available:       true,
		Kind:            resolved.Kind,
		RedactedDisplay: RedactSensitive(resolved.RedactedDisplay, authSecretForms(resolved.HeaderName, resolved.HeaderValue)...),
	}, b.responseCap())
}

func (b *hostImportBridge) instantiateHostImports(ctx context.Context, runtime wazero.Runtime) error {
	if b == nil {
		return errors.New("host import bridge is nil")
	}

	_, err := runtime.NewHostModuleBuilder(HostImportModule).
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		b.callHostImport(ctx, mod, stack, b.executeHTTPFetch)
	}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export(HostImportHTTPFetch).
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		b.callHostImport(ctx, mod, stack, b.executeAuthProfileStatus)
	}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export(HostImportAuthProfileStatus).
		Instantiate(ctx)

	return err
}

func (b *hostImportBridge) callHostImport(ctx context.Context, mod api.Module, stack []uint64, execute func(context.Context, []byte) []byte) {
	if len(stack) < 2 {
		return
	}
	requestPtr := api.DecodeU32(stack[0])
	requestLen := api.DecodeU32(stack[1])
	stack[0] = 0

	memory := mod.Memory()
	if memory == nil || requestLen == 0 || int(requestLen) > b.requestCap() {
		return
	}
	requestView, ok := memory.Read(requestPtr, requestLen)
	if !ok {
		return
	}
	responseBytes := execute(ctx, cloneBytes(requestView))
	if len(responseBytes) == 0 || len(responseBytes) > b.responseCap() {
		return
	}
	responsePtr, ok := allocateHostImportResponse(ctx, mod, uint32(len(responseBytes)))
	if !ok || responsePtr == 0 {
		return
	}
	if !memory.Write(responsePtr, responseBytes) {
		return
	}

	stack[0] = packABIResult(responsePtr, uint32(len(responseBytes)))
}

func allocateHostImportResponse(ctx context.Context, mod api.Module, length uint32) (uint32, bool) {
	alloc := mod.ExportedFunction(ABIExportAlloc)
	if alloc == nil || length == 0 {
		return 0, false
	}
	results, err := alloc.Call(ctx, uint64(length))
	if err != nil || len(results) != 1 {
		return 0, false
	}

	return api.DecodeU32(results[0]), true
}

func (b *hostImportBridge) consumeBudget() error {
	if b == nil || b.budget == nil {
		return errors.New("host call budget is not configured")
	}

	return b.budget.Consume()
}

func (b *hostImportBridge) decodeRequestStrict(raw []byte, output any) error {
	if len(raw) == 0 {
		return errors.New("request must be non-empty")
	}
	if len(raw) > b.requestCap() {
		return fmt.Errorf("request exceeds %d byte cap", b.requestCap())
	}

	return decodeStrictJSON(raw, output)
}

func (b *hostImportBridge) requestCap() int {
	if b == nil || b.maxRequestBytes <= 0 {
		return maxHostImportRequestBytes
	}

	return b.maxRequestBytes
}

func (b *hostImportBridge) responseCap() int {
	if b == nil || b.maxResponseBytes <= 0 {
		return maxHostImportResponseBytes
	}

	return b.maxResponseBytes
}

func decodeStrictJSON(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request contains trailing JSON data")
	}

	return nil
}

func encodeHostHTTPFetchResponse(response HostHTTPFetchResponse, capBytes int) []byte {
	return encodeBoundedHostImportResponse(response, capBytes)
}

func encodeHostAuthProfileStatusResponse(response HostAuthProfileStatusResponse, capBytes int) []byte {
	return encodeBoundedHostImportResponse(response, capBytes)
}

func encodeBoundedHostImportResponse(response any, capBytes int) []byte {
	if capBytes <= 0 {
		capBytes = maxHostImportResponseBytes
	}
	bytes, err := json.Marshal(response)
	if err != nil {
		bytes = []byte(`{"ok":false,"error_code":"internal_error","message":"encode host import response"}`)
	}
	if len(bytes) <= capBytes {
		return bytes
	}

	truncated, err := json.Marshal(struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}{
		OK:        false,
		ErrorCode: "response_too_large",
		Message:   "host import response exceeds size cap",
	})
	if err != nil || len(truncated) > capBytes {
		return nil
	}

	return truncated
}

func safeHeaderMap(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	safe := make(map[string][]string, len(headers))
	for name, values := range headers {
		cloned := make([]string, 0, len(values))
		for _, value := range values {
			cloned = append(cloned, RedactSensitive(value))
		}
		if len(cloned) > 0 {
			safe[http.CanonicalHeaderKey(name)] = cloned
		}
	}

	return safe
}
