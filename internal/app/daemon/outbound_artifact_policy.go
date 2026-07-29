package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
)

const (
	defaultOutboundArtifactPolicyTimeout = 60 * time.Second
	maxOutboundArtifactPolicyTimeout     = 5 * time.Minute
	maxOutboundArtifactPolicyErrorLength = 800
)

type outboundArtifactPolicy struct {
	Command string
	Timeout time.Duration
}

type outboundArtifactPolicyRequest struct {
	SchemaVersion    string `json:"schema_version"`
	GatewayID        string `json:"gateway_id"`
	SurfaceSessionID string `json:"surface_session_id"`
	Kind             string `json:"kind"`
	Path             string `json:"path"`
}

type outboundArtifactPolicyResponse struct {
	Path          string `json:"path"`
	Watermarked   bool   `json:"watermarked"`
	PolicyVersion string `json:"policy_version,omitempty"`
}

func outboundArtifactPoliciesFromFeishuApps(apps []config.FeishuAppConfig) map[string]outboundArtifactPolicy {
	policies := map[string]outboundArtifactPolicy{}
	for _, app := range apps {
		gatewayID := strings.TrimSpace(app.ID)
		if gatewayID == "" || app.OutboundArtifactPolicy == nil {
			continue
		}
		command := strings.TrimSpace(app.OutboundArtifactPolicy.Command)
		timeout := time.Duration(app.OutboundArtifactPolicy.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = defaultOutboundArtifactPolicyTimeout
		}
		if timeout > maxOutboundArtifactPolicyTimeout {
			timeout = maxOutboundArtifactPolicyTimeout
		}
		policies[gatewayID] = outboundArtifactPolicy{
			Command: command,
			Timeout: timeout,
		}
	}
	return policies
}

func (a *App) SetOutboundArtifactPolicies(policies map[string]outboundArtifactPolicy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(policies) == 0 {
		a.outboundArtifactPolicies = nil
		return
	}
	a.outboundArtifactPolicies = make(map[string]outboundArtifactPolicy, len(policies))
	for gatewayID, policy := range policies {
		gatewayID = strings.TrimSpace(gatewayID)
		if gatewayID == "" {
			continue
		}
		if policy.Timeout <= 0 {
			policy.Timeout = defaultOutboundArtifactPolicyTimeout
		}
		a.outboundArtifactPolicies[gatewayID] = policy
	}
}

func (a *App) outboundArtifactPolicyForGateway(gatewayID string) (outboundArtifactPolicy, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	policy, ok := a.outboundArtifactPolicies[strings.TrimSpace(gatewayID)]
	return policy, ok
}

func (a *App) prepareOutboundArtifact(
	ctx context.Context,
	resolved resolvedToolSurfaceContext,
	kind string,
	path string,
) (string, *toolError) {
	policy, configured := a.outboundArtifactPolicyForGateway(resolved.GatewayID)
	if !configured {
		return path, nil
	}
	if strings.TrimSpace(policy.Command) == "" {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy command is not configured",
		}
	}
	commandPath := strings.TrimSpace(policy.Command)
	if !filepath.IsAbs(commandPath) {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy command must be an absolute path",
		}
	}
	info, err := os.Stat(commandPath)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy command is unavailable",
		}
	}

	request := outboundArtifactPolicyRequest{
		SchemaVersion:    "1.0",
		GatewayID:        strings.TrimSpace(resolved.GatewayID),
		SurfaceSessionID: strings.TrimSpace(resolved.SurfaceSessionID),
		Kind:             strings.TrimSpace(kind),
		Path:             path,
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "failed to encode outbound artifact policy request",
		}
	}
	rawRequest = append(rawRequest, '\n')

	timeout := policy.Timeout
	if timeout <= 0 {
		timeout = defaultOutboundArtifactPolicyTimeout
	}
	policyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := execlaunch.CommandContext(policyCtx, commandPath)
	command.Stdin = bytes.NewReader(rawRequest)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > maxOutboundArtifactPolicyErrorLength {
			message = message[:maxOutboundArtifactPolicyErrorLength]
		}
		if errors.Is(policyCtx.Err(), context.DeadlineExceeded) {
			message = "outbound artifact policy timed out"
		} else if message == "" {
			message = "outbound artifact policy rejected the file"
		}
		return "", &toolError{
			Code:    "artifact_policy_rejected",
			Message: message,
		}
	}

	var response outboundArtifactPolicyResponse
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&response); err != nil {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy returned invalid JSON",
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy returned multiple JSON values",
		}
	} else if !errors.Is(err, io.EOF) {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy returned trailing invalid data",
		}
	}
	preparedPath := strings.TrimSpace(response.Path)
	if !response.Watermarked || preparedPath == "" {
		return "", &toolError{
			Code:    "artifact_policy_rejected",
			Message: "outbound artifact policy did not verify a watermarked artifact",
		}
	}
	if !filepath.IsAbs(preparedPath) {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy returned a non-absolute path",
		}
	}
	preparedInfo, err := os.Stat(preparedPath)
	if err != nil || preparedInfo.IsDir() || preparedInfo.Size() <= 0 {
		return "", &toolError{
			Code:    "artifact_policy_failed",
			Message: "outbound artifact policy returned an unavailable file",
		}
	}
	if kind == "image" {
		if apiErr := validateSendImagePath(preparedPath); apiErr != nil {
			return "", &toolError{
				Code:    "artifact_policy_failed",
				Message: fmt.Sprintf("outbound artifact policy returned invalid image: %s", apiErr.Message),
			}
		}
	}
	return preparedPath, nil
}
