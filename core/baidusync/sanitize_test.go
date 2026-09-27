package baidusync

import (
	"encoding/json"
	"strings"
	"testing"
)

// 安全关键：上游登录成功响应会带**完整登录 Cookie**，代理层必须剥离后才能回传前端。
// 一旦泄漏，等于把账号凭据交给了浏览器。
func TestSanitizeLoginPayloadStripsCredentials(t *testing.T) {
	raw := json.RawMessage(`{"code":200,"msg":"success","data":{
		"source":"qq","key":"k1","status":"success","message":"ok",
		"cookie":"uin=123456; qm_keyst=SUPERSECRET",
		"cookies":{"qq":"SUPERSECRET2"},
		"extra":{"code":"0"}}}`)

	got := string(SanitizeLoginPayload(raw))
	if strings.Contains(got, "cookie") {
		t.Fatalf("敏感字段 cookie/cookies 未被剥离: %s", got)
	}
	if strings.Contains(got, "SUPERSECRET") {
		t.Fatalf("凭据内容仍然存在: %s", got)
	}

	var out struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if out.Code != 200 || out.Msg != "success" {
		t.Errorf("code/msg 应保留，得到 %d/%q", out.Code, out.Msg)
	}
	// 前端展示所需字段必须保留，否则登录交互会失效
	for _, k := range []string{"source", "key", "status", "message", "extra"} {
		if _, ok := out.Data[k]; !ok {
			t.Errorf("展示字段 %q 不应被删除", k)
		}
	}
	if out.Data["status"] != "success" {
		t.Errorf("status 应保留，得到 %v", out.Data["status"])
	}
}

// 容错：各种异常输入都不能 panic（该函数在请求路径上）。
func TestSanitizeLoginPayloadTolerant(t *testing.T) {
	inputs := []string{
		`not-json-at-all`,
		``,
		`{}`,
		`{"code":500,"msg":"boom"}`,
		`{"code":200,"msg":"ok","data":null}`,
		`{"code":200,"msg":"ok","data":{"status":"waiting"}}`,
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("输入 %q 触发 panic: %v", in, r)
				}
			}()
			_ = SanitizeLoginPayload(json.RawMessage(in))
		}()
	}
}

// 输出必须是合法 JSON，且二维码字段要保留（前端直接使用 image_url）。
func TestSanitizeLoginPayloadKeepsQRFields(t *testing.T) {
	raw := json.RawMessage(`{"code":200,"msg":"success","data":{"key":"abc","image_url":"data:image/png;base64,iVBORw0KGgo"}}`)
	out := SanitizeLoginPayload(raw)
	if !json.Valid(out) {
		t.Fatalf("输出非法 JSON: %s", string(out))
	}
	if !strings.Contains(string(out), "data:image/png;base64") {
		t.Error("二维码图片字段应保留")
	}
	if !strings.Contains(string(out), `"key":"abc"`) {
		t.Error("登录 key 应保留（轮询需要）")
	}
}
