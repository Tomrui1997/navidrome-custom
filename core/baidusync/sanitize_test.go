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

// 中间层必须原样透传 failedItems。
// 该字段是在助手侧新加的，本层若漏声明会被 encoding/json **静默丢弃**，
// 导致前端既看不到「未完成数量」也无法一键重试（真实踩过的坑，务必防回归）。
func TestDownloadTaskKeepsFailedItems(t *testing.T) {
	raw := []byte(`{
		"running": false, "total": 3, "done": 2, "skipped": 0, "failed": 1,
		"processed": 2, "elapsed": 12, "errors": ["周杰伦 - 晴天"],
		"failedItems": [{"id":"abc","source":"kuwo","name":"晴天","artist":"周杰伦","album":"叶惠美","duration":269}]
	}`)

	var st DownloadTask
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if st.Failed != 1 || st.Processed != 2 || st.Total != 3 {
		t.Errorf("基础字段解析异常: failed=%d processed=%d total=%d", st.Failed, st.Processed, st.Total)
	}
	if len(st.FailedItems) != 1 {
		t.Fatalf("failedItems 被中间层丢弃了（必须在 DownloadTask 上声明该字段）: %d 条", len(st.FailedItems))
	}
	it := st.FailedItems[0]
	if it.ID != "abc" || it.Source != "kuwo" || it.Name != "晴天" || it.Artist != "周杰伦" || it.Duration != 269 {
		t.Errorf("失败项内容不完整: %#v", it)
	}

	// 再序列化回去 —— 前端拿到的就是这份 JSON，字段必须还在
	out, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(out), `"failedItems"`) {
		t.Errorf("序列化结果缺少 failedItems: %s", out)
	}
	if !strings.Contains(string(out), `"coverUrl"`) {
		t.Errorf("DownloadItem 字段名应与助手一致（coverUrl）: %s", out)
	}
}
