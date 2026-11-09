package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAiAdminToolCatalog(t *testing.T) {
	defs := aiAdminToolDefs()
	require.Len(t, defs, 6)
	seen := map[string]bool{}
	for _, def := range defs {
		require.NotEmpty(t, def.Name)
		require.False(t, seen[def.Name], "duplicate tool name %s", def.Name)
		seen[def.Name] = true
		require.Equal(t, "object", def.Parameters["type"])
		tool := def.openAITool()
		require.Equal(t, "function", tool["type"])
		function := tool["function"].(map[string]any)
		require.Equal(t, def.Name, function["name"])
		require.NotEmpty(t, function["description"])
		require.NotNil(t, function["parameters"])
		catalog := def.catalogEntry()
		require.Equal(t, def.Mutating, catalog["mutating"])
	}
	require.True(t, aiAdminToolNames()["update_model_price"])
	require.True(t, aiAdminToolNames()["adjust_user_quota"])
	require.False(t, aiAdminToolNames()["drop_database"])
}

func TestParseAiAdminArguments(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    map[string]any
		wantErr bool
	}{
		{"object", `{"model":"gpt-4o","ratio":2.5}`, map[string]any{"model": "gpt-4o", "ratio": 2.5}, false},
		{"encoded string", `"{\"model\":\"gpt-4o\"}"`, map[string]any{"model": "gpt-4o"}, false},
		{"empty", ``, map[string]any{}, false},
		{"null", `null`, map[string]any{}, false},
		{"empty string", `""`, map[string]any{}, false},
		{"array", `[]`, nil, true},
		{"bad string", `"not json"`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAiAdminArguments(json.RawMessage(tc.raw))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAiAdminArgumentValidation(t *testing.T) {
	_, _, err := aiAdminNumber(map[string]any{"ratio": "abc"}, "ratio")
	require.Error(t, err)
	_, _, err = aiAdminNumber(map[string]any{"ratio": "NaN"}, "ratio")
	require.Error(t, err)
	value, exists, err := aiAdminNumber(map[string]any{"ratio": "2.5"}, "ratio")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, 2.5, value)

	_, exists, err = aiAdminInt(map[string]any{"value": 1.5}, "value")
	require.Error(t, err)
	require.True(t, exists)
	intValue, exists, err := aiAdminInt(map[string]any{"value": 10}, "value")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, 10, intValue)

	require.Error(t, aiAdminValidatePrice(-1, "ratio"))
	require.Error(t, aiAdminValidatePrice(aiAdminMaxRatio+1, "ratio"))
	require.NoError(t, aiAdminValidatePrice(0, "ratio"))
}

func TestSanitizeAiAdminArgs(t *testing.T) {
	original := map[string]any{
		"name": "my channel",
		"key":  "sk-super-secret",
		"html": strings.Repeat("<p>hello</p>", 100),
	}
	safe := sanitizeAiAdminArgs(original)
	require.Equal(t, "my channel", safe["name"])
	require.Equal(t, "***redacted***", safe["key"])
	require.NotContains(t, fmt.Sprint(safe["html"]), strings.Repeat("<p>hello</p>", 100))
	require.Contains(t, fmt.Sprint(safe["html"]), "bytes")
	// The caller's map must not be mutated.
	require.Equal(t, "sk-super-secret", original["key"])
}

// ensureAiAdminOptionMap mirrors the startup initialization that tests skip.
func ensureAiAdminOptionMap(t *testing.T) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	if _, ok := common.OptionMap["HomePageContent"]; !ok {
		common.OptionMap["HomePageContent"] = ""
	}
}

func newAiAdminTestContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/ai-admin/execute", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("username", "root")
	c.Set("role", common.RoleRootUser)
	return c, recorder
}

func TestExecuteAiAdminToolRejectsInvalid(t *testing.T) {
	c, recorder := newAiAdminTestContext(t, `{"name":"drop_database","arguments":{}}`)
	ExecuteAiAdminTool(c)
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.False(t, resp.Success)
	require.Contains(t, resp.Message, "unknown tool")

	c, recorder = newAiAdminTestContext(t, `{"name":"update_model_price","arguments":[]}`)
	ExecuteAiAdminTool(c)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.False(t, resp.Success)
	require.Contains(t, resp.Message, "JSON object")
}

func TestExecuteAiAdminToolHomepageAndAudit(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}))
	ensureAiAdminOptionMap(t)

	common.OptionMapRWMutex.RLock()
	previousHTML := common.OptionMap["HomePageContent"]
	common.OptionMapRWMutex.RUnlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["HomePageContent"] = previousHTML
		common.OptionMapRWMutex.Unlock()
	})

	html := "<h1>AI Admin test homepage</h1>"
	payload, err := json.Marshal(map[string]any{
		"name":      "update_homepage_html",
		"arguments": map[string]any{"html": html},
	})
	require.NoError(t, err)
	c, recorder := newAiAdminTestContext(t, string(payload))
	ExecuteAiAdminTool(c)

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Tool        string         `json:"tool"`
			Summary     string         `json:"summary"`
			Result      map[string]any `json:"result"`
			AuditLogged bool           `json:"audit_logged"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp), recorder.Body.String())
	require.True(t, resp.Success, resp.Message)
	require.True(t, resp.Data.AuditLogged)
	require.Contains(t, resp.Data.Summary, "homepage HTML")
	require.Equal(t, float64(len(html)), resp.Data.Result["length"])

	common.OptionMapRWMutex.RLock()
	stored := common.OptionMap["HomePageContent"]
	common.OptionMapRWMutex.RUnlock()
	require.Equal(t, html, stored)

	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "ai_admin.execute").Take(&audit).Error)
	require.Equal(t, "ai_admin.execute", audit.Action)
	require.True(t, audit.Success)
	require.Equal(t, model.AuditCategoryOperation, audit.Category)
	require.Contains(t, audit.Content, "update_homepage_html")
	require.Contains(t, audit.Content, "homepage HTML")
	require.NotContains(t, audit.Content, html, "full HTML must not be written to the audit log")
	require.NotNil(t, audit.Other.Op)
	// AuditFields retains raw JSON values, so compare their text form.
	toolRaw, ok := audit.Other.Op.Params["tool"].(json.RawMessage)
	require.True(t, ok)
	require.Contains(t, string(toolRaw), "update_homepage_html")
	successRaw, ok := audit.Other.Op.Params["success"].(json.RawMessage)
	require.True(t, ok)
	require.Equal(t, "true", string(successRaw))
}

func TestExecuteAiAdminToolRecordsFailure(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}))
	ensureAiAdminOptionMap(t)

	c, recorder := newAiAdminTestContext(t, `{"name":"update_model_price","arguments":{"model":"gpt-4o","ratio":-1}}`)
	ExecuteAiAdminTool(c)
	var resp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.False(t, resp.Success)

	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "ai_admin.execute").Take(&audit).Error)
	require.False(t, audit.Success)
	require.Contains(t, audit.Content, "error")
}

func TestExecuteAiAdminToolUpdateModelPrice(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}))
	ensureAiAdminOptionMap(t)

	modelName := "ai-admin-test-price-model"
	c, recorder := newAiAdminTestContext(t, `{"name":"update_model_price","arguments":{"model":"`+modelName+`","ratio":2.5}}`)
	ExecuteAiAdminTool(c)
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Result map[string]any `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.True(t, resp.Success, resp.Message)
	require.Equal(t, 2.5, ratio_setting.GetModelRatioCopy()[modelName])
	require.Contains(t, fmt.Sprint(resp.Data.Result["changes"]), "ratio")

	var stored model.Option
	require.NoError(t, db.Where("key = ?", "ModelRatio").Take(&stored).Error)
	require.Contains(t, stored.Value, modelName)
}

func TestExecuteAiAdminToolCreateChannel(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}, &model.Ability{}))
	ensureAiAdminOptionMap(t)

	c, recorder := newAiAdminTestContext(t, `{"name":"create_channel","arguments":{"name":"ai-admin-test-channel","key":"sk-should-be-redacted","models":"gpt-4o,gpt-4o-mini"}}`)
	ExecuteAiAdminTool(c)
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Result map[string]any `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.True(t, resp.Success, resp.Message)
	require.Equal(t, "ai-admin-test-channel", resp.Data.Result["name"])

	var channel model.Channel
	require.NoError(t, db.Where("name = ?", "ai-admin-test-channel").Take(&channel).Error)
	require.Contains(t, channel.Models, "gpt-4o")

	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "ai_admin.execute").Take(&audit).Error)
	require.NotContains(t, audit.Content, "sk-should-be-redacted", "credentials must never enter the audit table")
	require.Contains(t, audit.Content, "redacted")
}

func TestExecuteAiAdminToolAdjustUserQuota(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}))
	ensureAiAdminOptionMap(t)

	user := model.User{Username: "ai-admin-quota-user", Quota: 1000, Role: common.RoleCommonUser}
	require.NoError(t, db.Create(&user).Error)

	payload := fmt.Sprintf(`{"name":"adjust_user_quota","arguments":{"username":"%s","mode":"add","value":500}}`, user.Username)
	c, recorder := newAiAdminTestContext(t, payload)
	ExecuteAiAdminTool(c)
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Result map[string]any `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.True(t, resp.Success, resp.Message)
	require.Equal(t, float64(1000), resp.Data.Result["before"])
	require.Equal(t, float64(1500), resp.Data.Result["after"])

	var stored model.User
	require.NoError(t, db.Where("id = ?", user.Id).Take(&stored).Error)
	require.Equal(t, 1500, stored.Quota)
}
