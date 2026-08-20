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
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	maxHostImportRequestBytes  = 64 * 1024
	maxHostImportResponseBytes = 2 * 1024 * 1024
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
	AuthProfileRef  string            `json:"auth_profile_ref"`
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
	manifest         Manifest
	packIdentity     VerifiedPackIdentity
	packID           string
	budget           *HostCallBudget
	httpBroker       *HTTPBroker
	authResolver     AuthProfileResolver
	hostPolicy       HostPolicyResolver
	maxRequestBytes  int
	maxResponseBytes int
}

type hostImportRequestMode int

const (
	hostImportModeInvalid hostImportRequestMode = iota
	hostImportModeLegacyRawURL
	hostImportModeAliasRef
)

type hostImportModeFields struct {
	URL             string
	BrokerPolicyRef string
	EndpointRef     string
	Params          map[string]string
}

func newHostImportBridge(pack VerifiedPack, budget *HostCallBudget, config HostImportConfig) *hostImportBridge {
	return &hostImportBridge{
		manifest:         pack.Manifest,
		packIdentity:     pack.Identity,
		packID:           pack.Manifest.PackID,
		budget:           budget,
		httpBroker:       config.HTTPBroker,
		authResolver:     config.AuthResolver,
		hostPolicy:       config.HostPolicyResolver,
		maxRequestBytes:  maxHostImportRequestBytes,
		maxResponseBytes: maxHostImportResponseBytes,
	}
}

func (b *hostImportBridge) executeHTTPFetch(ctx context.Context, requestBytes []byte) []byte {
	if err := b.consumeBudget(); err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "budget_exhausted", Message: RedactSensitive(err.Error())}, b.responseCap())
	}

	var request HostHTTPFetchRequest
	if err := b.decodeRequestStrict(requestBytes, &request); err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
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
	mode, params, err := determineHostImportRequestMode(b.manifest, hostImportModeFields{
		URL:             request.URL,
		BrokerPolicyRef: request.BrokerPolicyRef,
		EndpointRef:     request.EndpointRef,
		Params:          request.Params,
	})
	if err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	if b.httpBroker == nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "not_configured", Message: "http broker is not configured"}, b.responseCap())
	}

	if mode == hostImportModeAliasRef {
		return b.executeHTTPFetchRefMode(ctx, request, params)
	}

	var timeout time.Duration
	if request.TimeoutMillis > 0 {
		timeout = time.Duration(request.TimeoutMillis) * time.Millisecond
	}
	response, err := b.httpBroker.Fetch(ctx, HTTPFetchRequest{
		PackID:           b.packID,
		Manifest:         b.manifest,
		PackIdentity:     b.packIdentity,
		Method:           request.Method,
		URL:              request.URL,
		Headers:          request.Headers,
		AuthProfileID:    AuthProfileID(request.AuthProfileRef),
		Timeout:          timeout,
		MaxResponseBytes: request.MaxResponseBytes,
	})
	if err != nil {
		if request.AuthProfileRef != "" {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "authenticated_fetch_failed", Message: "authenticated fetch failed"}, b.responseCap())
		}

		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "fetch_failed", Message: "fetch failed"}, b.responseCap())
	}

	return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{
		OK:         true,
		StatusCode: response.StatusCode,
		FinalURL:   RedactSensitive(response.FinalURL),
		Headers:    safeHeaderMap(response.Headers),
		BodyBase64: base64.StdEncoding.EncodeToString(response.Body),
	}, b.responseCap())
}

func (b *hostImportBridge) executeHTTPFetchRefMode(ctx context.Context, request HostHTTPFetchRequest, params map[string]string) []byte {
	policy, err := resolveAliasHostPolicy(ctx, b.effectiveHostPolicyResolver(), b.packIdentity, b.manifest)
	if err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "policy_denied", Message: "alias host policy denied request"}, b.responseCap())
	}
	endpoint, ok := resolveHostPolicyEndpoint(policy, request.BrokerPolicyRef, request.EndpointRef)
	if !ok {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy endpoint is not available"}, b.responseCap())
	}
	method, endpointTimeoutMillis, endpointMaxResponseBytes, err := validateHostPolicyEndpointRequest(endpoint, request.Method, AuthProfileID(request.AuthProfileRef), b.manifest, b.httpBroker.policy)
	if err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "policy_denied", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	expandedURL, err := expandHostPolicyEndpointURL(policy, endpoint, params)
	if err != nil {
		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}

	timeoutMillis := minPositiveDurationMillis(request.TimeoutMillis, endpointTimeoutMillis)
	var timeout time.Duration
	if timeoutMillis > 0 {
		timeout = time.Duration(timeoutMillis) * time.Millisecond
	}
	maxResponseBytes := minPositiveInt64(request.MaxResponseBytes, endpointMaxResponseBytes)
	response, err := b.httpBroker.Fetch(ctx, HTTPFetchRequest{
		PackID:           b.packID,
		Manifest:         b.manifest,
		PackIdentity:     b.packIdentity,
		Method:           method,
		URL:              expandedURL,
		Headers:          request.Headers,
		AuthProfileID:    AuthProfileID(request.AuthProfileRef),
		Timeout:          timeout,
		MaxResponseBytes: maxResponseBytes,
	})
	if err != nil {
		if request.AuthProfileRef != "" {
			return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "authenticated_fetch_failed", Message: "authenticated fetch failed"}, b.responseCap())
		}

		return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{OK: false, ErrorCode: "fetch_failed", Message: "fetch failed"}, b.responseCap())
	}

	return encodeHostHTTPFetchResponse(HostHTTPFetchResponse{
		OK:         true,
		StatusCode: response.StatusCode,
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
	profileID := AuthProfileID(request.AuthProfileRef)
	if err := validateAuthProfileID(profileID); err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	mode, params, err := determineHostImportRequestMode(b.manifest, hostImportModeFields{
		URL:             request.URL,
		BrokerPolicyRef: request.BrokerPolicyRef,
		EndpointRef:     request.EndpointRef,
		Params:          request.Params,
	})
	if err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	if mode == hostImportModeAliasRef {
		return b.executeAuthProfileStatusRefMode(ctx, request, params, profileID)
	}
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:             b.packID,
		Manifest:           b.manifest,
		Capability:         CapabilityAuthProfile,
		PackIdentity:       b.packIdentity,
		HostPolicyResolver: b.hostPolicy,
	}, request.URL); err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	if isAliasManifest(b.manifest) {
		_, host, err := parseSafeHTTPURL(request.URL)
		if err != nil {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: RedactSensitive(err.Error())}, b.responseCap())
		}
		policy, err := resolveAliasHostPolicy(ctx, b.hostPolicy, b.packIdentity, b.manifest)
		if err != nil || !policyAuthProfileMatchesHost(policy, profileID, host) {
			return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "auth profile is not allowed by alias host policy"}, b.responseCap())
		}
	}
	if b.authResolver == nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "not_configured", Message: "auth resolver is not configured"}, b.responseCap())
	}

	resolved, err := b.authResolver.ResolveAuthProfile(ctx, b.packID, profileID, request.URL)
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

func (b *hostImportBridge) executeAuthProfileStatusRefMode(ctx context.Context, request HostAuthProfileStatusRequest, params map[string]string, profileID AuthProfileID) []byte {
	if !ManifestHasCapability(b.manifest, CapabilityAuthProfile) {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "pack is not allowed to use auth profiles"}, b.responseCap())
	}
	policy, err := resolveAliasHostPolicy(ctx, b.effectiveHostPolicyResolver(), b.packIdentity, b.manifest)
	if err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "alias host policy denied request"}, b.responseCap())
	}
	if !policyAllowsCapability(policy, CapabilityAuthProfile) {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "alias host policy denied auth profile status"}, b.responseCap())
	}
	endpoint, ok := resolveHostPolicyEndpoint(policy, request.BrokerPolicyRef, request.EndpointRef)
	if !ok {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "host policy endpoint is not available"}, b.responseCap())
	}
	if !hostPolicyEndpointAllowsAuthProfile(endpoint, profileID) {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "auth profile is not allowed by host policy endpoint"}, b.responseCap())
	}
	expandedURL, err := expandHostPolicyEndpointURL(policy, endpoint, params)
	if err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "invalid_request", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	_, host, err := parseSafeHTTPURL(expandedURL)
	if err != nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: RedactSensitive(err.Error())}, b.responseCap())
	}
	if !policyBrokerMatchesHost(policy, host) || !policyAuthProfileMatchesHost(policy, profileID, host) {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "policy_denied", Message: "auth profile is not allowed by alias host policy"}, b.responseCap())
	}
	if b.authResolver == nil {
		return encodeHostAuthProfileStatusResponse(HostAuthProfileStatusResponse{OK: false, ErrorCode: "not_configured", Message: "auth resolver is not configured"}, b.responseCap())
	}

	resolved, err := b.authResolver.ResolveAuthProfile(ctx, b.packID, profileID, expandedURL)
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

func (b *hostImportBridge) effectiveHostPolicyResolver() HostPolicyResolver {
	if b == nil {
		return nil
	}
	if b.hostPolicy != nil {
		return b.hostPolicy
	}
	if b.httpBroker != nil {
		return b.httpBroker.hostPolicyResolver
	}

	return nil
}

func determineHostImportRequestMode(manifest Manifest, fields hostImportModeFields) (hostImportRequestMode, map[string]string, error) {
	hasURL := fields.URL != ""
	hasBrokerRef := fields.BrokerPolicyRef != ""
	hasEndpointRef := fields.EndpointRef != ""
	hasParams := len(fields.Params) > 0
	hasAnyRefField := hasBrokerRef || hasEndpointRef || hasParams
	alias := isAliasManifest(manifest)

	if hasURL && hasAnyRefField {
		return hostImportModeInvalid, nil, errors.New("raw url must not be combined with ref-mode fields")
	}
	if hasURL {
		if alias {
			return hostImportModeInvalid, nil, errors.New("alias manifest host imports must use broker_policy_ref and endpoint_ref")
		}

		return hostImportModeLegacyRawURL, nil, nil
	}
	if hasBrokerRef && hasEndpointRef {
		if !alias {
			return hostImportModeInvalid, nil, errors.New("ref-mode host imports require an alias manifest")
		}
		if err := validateOpaquePolicyRef("broker_policy_ref", fields.BrokerPolicyRef); err != nil {
			return hostImportModeInvalid, nil, err
		}
		if err := validateOpaquePolicyRef("endpoint_ref", fields.EndpointRef); err != nil {
			return hostImportModeInvalid, nil, err
		}
		params, err := validateHostPolicyEndpointParams(fields.Params)
		if err != nil {
			return hostImportModeInvalid, nil, err
		}

		return hostImportModeAliasRef, params, nil
	}

	return hostImportModeInvalid, nil, errors.New("host import request must provide either legacy url or broker_policy_ref with endpoint_ref")
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
