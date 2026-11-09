package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// AI Admin is an operator-facing assistant: the model only *proposes* tool
// calls, and nothing is applied until the administrator reviews the arguments
// and clicks "Run" in the UI. This file owns the tool catalogue (exposed as
// OpenAI function definitions), the strict server-side executor and the
// operation audit trail. Execution is gated by RootAuth in the router.

const (
	aiAdminMaxHTMLBytes    = 512 * 1024
	aiAdminAuditFieldLimit = 1024
	aiAdminAuditContentMax = 4096
	aiAdminMaxRatio        = 1_000_000
)

const aiAdminSystemPrompt = `You are the AI management assistant of a New API gateway admin panel. You help administrators operate the system by calling the provided tools.

STRICT RULES:
1. Calling a tool only PROPOSES an action. NEVER claim an operation has been executed or has succeeded. The administrator must review the proposed arguments and click "Run" in the UI before anything is applied.
2. For every management request, respond with one or more tool calls from the provided tool list. Do not invent tool names or parameters, and do not describe the change as already done.
3. When you need current values (model prices, homepage HTML, channel list), call the corresponding read tool first.
4. For destructive changes (price changes, new channels, user balance adjustments, overwriting homepage HTML), propose them one at a time and state the exact target and new value in a short sentence before the call.
5. Keep replies short and precise. Never include credentials in your reply text.

FALLBACK: If your model cannot emit native tool calls, output ONLY a fenced JSON code block in exactly this shape:
` + "```json\n" + `{"tool_calls":[{"name":"<tool_name>","arguments":{...}}]}
` + "```" + `
Do not wrap it in any other text.`

type aiAdminToolDef struct {
	Name        string
	Description string
	Mutating    bool
	Parameters  map[string]any
}

func (d aiAdminToolDef) openAITool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        d.Name,
			"description": d.Description,
			"parameters":  d.Parameters,
		},
	}
}

func (d aiAdminToolDef) catalogEntry() map[string]any {
	return map[string]any{
		"name":        d.Name,
		"description": d.Description,
		"mutating":    d.Mutating,
		"parameters":  d.Parameters,
	}
}

func strSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func aiAdminToolDefs() []aiAdminToolDef {
	return []aiAdminToolDef{
		{
			Name:        "get_model_price",
			Description: "Read the current pricing of a model: model ratio (multiplier), per-request fixed price and completion ratio. Call this before changing a price so the administrator can see the old value.",
			Mutating:    false,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"model": strSchema("Exact model name, e.g. gpt-4o"),
				},
				"required": []string{"model"},
			},
		},
		{
			Name:        "update_model_price",
			Description: "Change the price of a model. Provide at least one of ratio (input token multiplier), price (fixed price per request in quota units) or completion_ratio (output token multiplier). Omitted fields are left unchanged.",
			Mutating:    true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"model":            strSchema("Exact model name to update"),
					"ratio":            numSchema("New input token multiplier, e.g. 2.5. Must be >= 0"),
					"price":            numSchema("New fixed price per request. Must be >= 0"),
					"completion_ratio": numSchema("New output token multiplier. Must be >= 0"),
				},
				"required": []string{"model"},
			},
		},
		{
			Name:        "create_channel",
			Description: "Create a new upstream channel. Requires a name, an API key and a comma-separated list of model names the channel serves. The key is never written to audit logs.",
			Mutating:    true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":     strSchema("Human-readable channel name"),
					"key":      strSchema("Upstream API key (required, kept secret)"),
					"models":   strSchema("Comma-separated model names, e.g. gpt-4o,gpt-4o-mini"),
					"type":     map[string]any{"type": "integer", "description": "Channel type id; 1 = OpenAI compatible (default)"},
					"base_url": strSchema("Upstream base URL, only when a custom endpoint is needed"),
					"group":    strSchema("User group allowed to use this channel (default: default)"),
				},
				"required": []string{"name", "key", "models"},
			},
		},
		{
			Name:        "adjust_user_quota",
			Description: "Adjust a user's wallet balance (quota). mode=add increases, subtract decreases, override sets the balance to value. Identify the user by user_id or username.",
			Mutating:    true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"user_id":  map[string]any{"type": "integer", "description": "Target user id"},
					"username": strSchema("Target username (used when user_id is not provided)"),
					"mode":     map[string]any{"type": "string", "enum": []string{"add", "subtract", "override"}, "description": "Adjustment mode"},
					"value":    map[string]any{"type": "integer", "description": "Amount in quota units (required)"},
				},
				"required": []string{"mode", "value"},
			},
		},
		{
			Name:        "get_homepage_html",
			Description: "Read the current custom homepage HTML content.",
			Mutating:    false,
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "update_homepage_html",
			Description: "Replace the custom homepage HTML with the provided content. This overwrites the previous content entirely; pass an empty string to clear it.",
			Mutating:    true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"html": strSchema("Full HTML document to publish on the homepage (max 512 KB)"),
				},
				"required": []string{"html"},
			},
		},
	}
}

func aiAdminToolNames() map[string]bool {
	defs := aiAdminToolDefs()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}
	return names
}

// aiAdminBuildSystemPrompt appends a compact tool catalogue to the base
// prompt. If an upstream model or channel drops the native `tools` array, the
// model can still emit a valid JSON fallback using these exact names.
func aiAdminBuildSystemPrompt(defs []aiAdminToolDef) string {
	var builder strings.Builder
	builder.WriteString(aiAdminSystemPrompt)
	builder.WriteString("\n\nAVAILABLE TOOLS (use these exact names):\n")
	for _, def := range defs {
		properties, _ := def.Parameters["properties"].(map[string]any)
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(&builder, "- %s(%s): %s\n", def.Name, strings.Join(names, ", "), def.Description)
	}
	return builder.String()
}

// GetAiAdminTools returns the function-calling schema, a UI catalogue and the
// system prompt. Keeping the catalogue server-side makes the AI's view and the
// executor's whitelist one single source of truth.
func GetAiAdminTools(c *gin.Context) {
	defs := aiAdminToolDefs()
	tools := make([]map[string]any, 0, len(defs))
	catalog := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, d.openAITool())
		catalog = append(catalog, d.catalogEntry())
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"tools":         tools,
			"catalog":       catalog,
			"system_prompt": aiAdminBuildSystemPrompt(defs),
		},
	})
}

// ExecuteAiAdminTool runs one reviewed tool call. Every invocation — success
// or failure — is written to the operation audit log.
func ExecuteAiAdminTool(c *gin.Context) {
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid request: " + err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if !aiAdminToolNames()[name] {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "unknown tool: " + name})
		return
	}
	args, err := parseAiAdminArguments(req.Arguments)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	result, summary, execErr := executeAiAdminTool(c, name, args)
	recordAiAdminAudit(c, name, summary, args, result, execErr)

	if execErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": execErr.Error(),
			"data":    gin.H{"tool": name, "audit_logged": true},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"tool":         name,
			"summary":      summary,
			"result":       result,
			"audit_logged": true,
		},
	})
}

func parseAiAdminArguments(raw json.RawMessage) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj, nil
	}
	// Native tool calls carry arguments as a JSON-encoded string.
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		if strings.TrimSpace(encoded) == "" {
			return map[string]any{}, nil
		}
		if err := json.Unmarshal([]byte(encoded), &obj); err != nil {
			return nil, errors.New("tool arguments must be a JSON object")
		}
		return obj, nil
	}
	return nil, errors.New("tool arguments must be a JSON object")
}

func executeAiAdminTool(c *gin.Context, name string, args map[string]any) (any, string, error) {
	switch name {
	case "get_model_price":
		return aiAdminGetModelPrice(args)
	case "update_model_price":
		return aiAdminUpdateModelPrice(args)
	case "create_channel":
		return aiAdminCreateChannel(args)
	case "adjust_user_quota":
		return aiAdminAdjustUserQuota(c, args)
	case "get_homepage_html":
		return aiAdminGetHomepageHTML()
	case "update_homepage_html":
		return aiAdminUpdateHomepageHTML(args)
	default:
		return nil, "", errors.New("unknown tool: " + name)
	}
}

// ---------------------------------------------------------------------------
// Argument helpers
// ---------------------------------------------------------------------------

func aiAdminString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func aiAdminRequiredString(args map[string]any, key string) (string, error) {
	value := aiAdminString(args, key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func aiAdminNumber(args map[string]any, key string) (float64, bool, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return 0, false, nil
	}
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case float32:
		value = float64(v)
	case int:
		value = float64(v)
	case int64:
		value = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, true, fmt.Errorf("%s must be a number", key)
		}
		value = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, true, fmt.Errorf("%s must be a number", key)
		}
		value = parsed
	default:
		return 0, true, fmt.Errorf("%s must be a number", key)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, true, fmt.Errorf("%s must be a finite number", key)
	}
	return value, true, nil
}

func aiAdminInt(args map[string]any, key string) (int, bool, error) {
	value, exists, err := aiAdminNumber(args, key)
	if err != nil || !exists {
		return 0, exists, err
	}
	if value != math.Trunc(value) {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	if value > math.MaxInt32 || value < math.MinInt32 {
		return 0, true, fmt.Errorf("%s is out of range", key)
	}
	return int(value), true, nil
}

func aiAdminValidatePrice(value float64, key string) error {
	if value < 0 || value > aiAdminMaxRatio {
		return fmt.Errorf("%s must be between 0 and %d", key, aiAdminMaxRatio)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

func aiAdminGetModelPrice(args map[string]any) (any, string, error) {
	name, err := aiAdminRequiredString(args, "model")
	if err != nil {
		return nil, "", err
	}
	result := gin.H{
		"model":            name,
		"ratio":            ratio_setting.GetModelRatioCopy()[name],
		"price":            ratio_setting.GetModelPriceCopy()[name],
		"completion_ratio": ratio_setting.GetCompletionRatioCopy()[name],
	}
	summary := fmt.Sprintf("queried pricing for model %q (ratio=%v, price=%v, completion=%v)",
		name, result["ratio"], result["price"], result["completion_ratio"])
	return result, summary, nil
}

func aiAdminUpdateModelPrice(args map[string]any) (any, string, error) {
	name, err := aiAdminRequiredString(args, "model")
	if err != nil {
		return nil, "", err
	}
	ratio, hasRatio, err := aiAdminNumber(args, "ratio")
	if err != nil {
		return nil, "", err
	}
	price, hasPrice, err := aiAdminNumber(args, "price")
	if err != nil {
		return nil, "", err
	}
	completion, hasCompletion, err := aiAdminNumber(args, "completion_ratio")
	if err != nil {
		return nil, "", err
	}
	if !hasRatio && !hasPrice && !hasCompletion {
		return nil, "", errors.New("at least one of ratio, price or completion_ratio is required")
	}
	type priceChange struct {
		option string
		values map[string]float64
		value  float64
		before float64
		existed bool
		label  string
	}
	changes := make([]priceChange, 0, 3)
	if hasRatio {
		if err := aiAdminValidatePrice(ratio, "ratio"); err != nil {
			return nil, "", err
		}
		values := ratio_setting.GetModelRatioCopy()
		before, existed := values[name]
		changes = append(changes, priceChange{"ModelRatio", values, ratio, before, existed, "ratio"})
	}
	if hasPrice {
		if err := aiAdminValidatePrice(price, "price"); err != nil {
			return nil, "", err
		}
		values := ratio_setting.GetModelPriceCopy()
		before, existed := values[name]
		changes = append(changes, priceChange{"ModelPrice", values, price, before, existed, "price"})
	}
	if hasCompletion {
		if err := aiAdminValidatePrice(completion, "completion_ratio"); err != nil {
			return nil, "", err
		}
		values := ratio_setting.GetCompletionRatioCopy()
		before, existed := values[name]
		changes = append(changes, priceChange{"CompletionRatio", values, completion, before, existed, "completion_ratio"})
	}
	// Apply sequentially; each option goes through the same validation and
	// transaction path used by the system settings page.
	applied := make([]string, 0, len(changes))
	for _, change := range changes {
		change.values[name] = change.value
		encoded, err := json.Marshal(change.values)
		if err != nil {
			return nil, "", fmt.Errorf("encode %s: %w", change.option, err)
		}
		if err := model.UpdateOption(change.option, string(encoded)); err != nil {
			return nil, "", fmt.Errorf("update %s: %w", change.option, err)
		}
		beforeText := "unset"
		if change.existed {
			beforeText = strconv.FormatFloat(change.before, 'f', -1, 64)
		}
		applied = append(applied, fmt.Sprintf("%s %s -> %s", change.label, beforeText,
			strconv.FormatFloat(change.value, 'f', -1, 64)))
	}
	result := gin.H{"model": name, "changes": applied}
	summary := fmt.Sprintf("updated price of model %q: %s", name, strings.Join(applied, "; "))
	return result, summary, nil
}

func aiAdminCreateChannel(args map[string]any) (any, string, error) {
	name, err := aiAdminRequiredString(args, "name")
	if err != nil {
		return nil, "", err
	}
	key, err := aiAdminRequiredString(args, "key")
	if err != nil {
		return nil, "", err
	}
	models, err := aiAdminRequiredString(args, "models")
	if err != nil {
		return nil, "", err
	}
	channelType, hasType, err := aiAdminInt(args, "type")
	if err != nil {
		return nil, "", err
	}
	if !hasType || channelType <= 0 {
		channelType = constant.ChannelTypeOpenAI
	}
	if channelType > 1000 {
		return nil, "", errors.New("type is out of range")
	}
	group := aiAdminString(args, "group")
	if group == "" {
		group = "default"
	}
	baseURL := aiAdminString(args, "base_url")

	channel := &model.Channel{
		Type:        channelType,
		Name:        name,
		Key:         key,
		Models:      models,
		Group:       group,
		Status:      1,
		CreatedTime: common.GetTimestamp(),
	}
	if baseURL != "" {
		channel.BaseURL = &baseURL
	}
	if err := validateChannel(channel, true); err != nil {
		return nil, "", err
	}
	if err := channel.Insert(); err != nil {
		return nil, "", fmt.Errorf("create channel: %w", err)
	}
	model.InitChannelCache()
	result := gin.H{
		"id": channel.Id, "name": name, "type": channelType,
		"models": models, "group": group, "base_url": baseURL,
	}
	summary := fmt.Sprintf("created channel %q (id=%d, type=%d, models=%s, group=%s)",
		name, channel.Id, channelType, models, group)
	return result, summary, nil
}

func aiAdminAdjustUserQuota(c *gin.Context, args map[string]any) (any, string, error) {
	userID, hasID, err := aiAdminInt(args, "user_id")
	if err != nil {
		return nil, "", err
	}
	username := aiAdminString(args, "username")
	if !hasID || userID <= 0 {
		if username == "" {
			return nil, "", errors.New("user_id or username is required")
		}
		var user model.User
		if err := model.DB.Select("id", "username", "role").Where("username = ?", username).First(&user).Error; err != nil {
			return nil, "", fmt.Errorf("user %q not found", username)
		}
		userID = user.Id
	}
	mode := aiAdminString(args, "mode")
	if mode != "add" && mode != "subtract" && mode != "override" {
		return nil, "", errors.New("mode must be add, subtract or override")
	}
	value, hasValue, err := aiAdminInt(args, "value")
	if err != nil {
		return nil, "", err
	}
	if !hasValue {
		return nil, "", errors.New("value is required")
	}
	adjustment, err := model.AdjustUserQuota(userID, c.GetInt("role"), mode, value)
	if err != nil {
		return nil, "", fmt.Errorf("adjust quota: %w", err)
	}
	result := gin.H{
		"user_id": adjustment.UserID, "username": adjustment.Username,
		"before": adjustment.Before, "after": adjustment.After,
		"mode": mode, "value": value,
	}
	summary := fmt.Sprintf("adjusted quota of user %s (id=%d) %s %d: %d -> %d",
		adjustment.Username, adjustment.UserID, mode, value, adjustment.Before, adjustment.After)
	return result, summary, nil
}

func aiAdminReadHomepageHTML() string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return common.OptionMap["HomePageContent"]
}

func aiAdminGetHomepageHTML() (any, string, error) {
	html := aiAdminReadHomepageHTML()
	result := gin.H{"html": html, "length": len(html)}
	summary := fmt.Sprintf("read homepage HTML (%d bytes)", len(html))
	return result, summary, nil
}

func aiAdminUpdateHomepageHTML(args map[string]any) (any, string, error) {
	htmlRaw, exists := args["html"]
	if !exists {
		return nil, "", errors.New("html is required")
	}
	html, ok := htmlRaw.(string)
	if !ok {
		return nil, "", errors.New("html must be a string")
	}
	if len(html) > aiAdminMaxHTMLBytes {
		return nil, "", fmt.Errorf("html exceeds the %d byte limit", aiAdminMaxHTMLBytes)
	}
	before := aiAdminReadHomepageHTML()
	if err := model.UpdateOption("HomePageContent", html); err != nil {
		return nil, "", fmt.Errorf("update homepage HTML: %w", err)
	}
	result := gin.H{
		"before_length": len(before), "length": len(html),
		"changed": before != html,
	}
	summary := fmt.Sprintf("updated homepage HTML: %d -> %d bytes", len(before), len(html))
	return result, summary, nil
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

func aiAdminTruncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func aiAdminAuditJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return aiAdminTruncateRunes(fmt.Sprintf("%v", value), aiAdminAuditFieldLimit)
	}
	return aiAdminTruncateRunes(string(encoded), aiAdminAuditFieldLimit)
}

// sanitizeAiAdminArgs keeps secrets and bulky payloads out of the audit table.
func sanitizeAiAdminArgs(args map[string]any) map[string]any {
	safe := make(map[string]any, len(args))
	for key, value := range args {
		safe[key] = value
	}
	if _, ok := safe["key"]; ok {
		safe["key"] = "***redacted***"
	}
	if html, ok := safe["html"].(string); ok {
		safe["html"] = fmt.Sprintf("<%d bytes> %s", len(html), aiAdminTruncateRunes(html, 200))
	}
	return safe
}

// recordAiAdminAudit writes a single operation audit entry describing the
// proposed arguments, the resulting change and whether it succeeded. The
// middleware fallback is suppressed via markAuditLogged to avoid duplicates.
func recordAiAdminAudit(c *gin.Context, tool, summary string, args map[string]any, result any, execErr error) {
	if summary == "" {
		summary = "no change"
	}
	success := execErr == nil
	argsJSON := aiAdminAuditJSON(sanitizeAiAdminArgs(args))
	resultJSON := aiAdminAuditJSON(result)
	params := model.AuditFields{
		"tool":    tool,
		"summary": aiAdminTruncateRunes(summary, 512),
		"args":    argsJSON,
		"result":  resultJSON,
		"success": success,
	}
	if execErr != nil {
		params["error"] = aiAdminTruncateRunes(execErr.Error(), 512)
	}
	content := fmt.Sprintf("AI Admin executed %s: %s | args: %s | result: %s",
		tool, summary, argsJSON, resultJSON)
	if execErr != nil {
		content += " | error: " + aiAdminTruncateRunes(execErr.Error(), 512)
	}
	content = aiAdminTruncateRunes(content, aiAdminAuditContentMax)

	auditInfo := &model.AuditRequestInfo{
		Method:  c.Request.Method,
		Route:   c.FullPath(),
		Path:    c.FullPath(),
		Status:  c.Writer.Status(),
		Success: success,
	}
	model.RecordOperationAuditLog(
		c.GetInt("id"), c.GetInt("role"), content, c.ClientIP(),
		"ai_admin.execute", params, auditOperatorInfo(c), auditInfo, c,
	)
	markAuditLogged(c)
}
