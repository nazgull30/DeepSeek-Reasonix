package builtinmcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHHMM(t *testing.T) {
	tests := []struct {
		input   string
		hour    int
		minute  int
		wantErr bool
	}{
		{"14:30", 14, 30, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"24:00", 0, 0, true},
		{"12:60", 0, 0, true},
		{"abc", 0, 0, true},
		{"12", 0, 0, true},
		{"-1:00", 0, 0, true},
		{"  08:15  ", 8, 15, false},
	}
	for _, tc := range tests {
		h, m, err := parseHHMM(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseHHMM(%q) should have failed", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("parseHHMM(%q) failed: %v", tc.input, err)
			} else if h != tc.hour || m != tc.minute {
				t.Errorf("parseHHMM(%q) = %d:%d, want %d:%d", tc.input, h, m, tc.hour, tc.minute)
			}
		}
	}
}

func TestLoadLocation(t *testing.T) {
	// Empty → local timezone.
	loc, name, err := loadLocation("")
	if err != nil {
		t.Fatalf("loadLocation(\"\"): %v", err)
	}
	if name == "" || loc == nil {
		t.Fatal("loadLocation(\"\"): empty result")
	}

	// Named timezone.
	loc, name, err = loadLocation("America/New_York")
	if err != nil {
		t.Fatalf("loadLocation(America/New_York): %v", err)
	}
	if name != "America/New_York" {
		t.Fatalf("name = %q, want America/New_York", name)
	}

	// Invalid timezone.
	_, _, err = loadLocation("Not/A/Timezone")
	if err == nil {
		t.Fatal("loadLocation(invalid) should fail")
	}

	// Whitespace handling.
	loc, name, err = loadLocation("  Europe/London  ")
	if err != nil {
		t.Fatalf("loadLocation(Europe/London with spaces): %v", err)
	}
	if name != "Europe/London" {
		t.Fatalf("name = %q, want Europe/London", name)
	}
}

func TestOptionalString(t *testing.T) {
	if got := optionalString(nil, "key"); got != "" {
		t.Fatalf("optionalString(nil) = %q, want \"\"", got)
	}
	if got := optionalString(map[string]any{}, "key"); got != "" {
		t.Fatalf("optionalString(empty) = %q, want \"\"", got)
	}
	if got := optionalString(map[string]any{"key": "value"}, "key"); got != "value" {
		t.Fatalf("optionalString = %q, want value", got)
	}
	// Non-string value.
	if got := optionalString(map[string]any{"key": 42}, "key"); got != "" {
		t.Fatalf("optionalString(non-string) = %q, want \"\"", got)
	}
	// Whitespace trimming.
	if got := optionalString(map[string]any{"key": "  val  "}, "key"); got != "val" {
		t.Fatalf("optionalString(with spaces) = %q, want val", got)
	}
}

func TestRequiredString(t *testing.T) {
	if got := requiredString(nil, "key"); got != "" {
		t.Fatalf("requiredString(nil) = %q, want \"\"", got)
	}
	if got := requiredString(map[string]any{}, "key"); got != "" {
		t.Fatalf("requiredString(empty) = %q, want \"\"", got)
	}
	if got := requiredString(map[string]any{"key": "value"}, "key"); got != "value" {
		t.Fatalf("requiredString = %q, want value", got)
	}
}

func TestMarshalTimeResult(t *testing.T) {
	s, err := marshalTimeResult(map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("marshalTimeResult: %v", err)
	}
	if !strings.Contains(s, "foo") || !strings.Contains(s, "bar") {
		t.Fatalf("marshalTimeResult = %q, want foo/bar", s)
	}
}

func TestTimeTextResult(t *testing.T) {
	r := timeTextResult("hello", false)
	content, ok := r["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["text"] != "hello" {
		t.Fatalf("timeTextResult content = %+v", r["content"])
	}
	if r["isError"] != false {
		t.Fatal("timeTextResult isError should be false")
	}

	r = timeTextResult("error msg", true)
	if r["isError"] != true {
		t.Fatal("timeTextResult isError should be true")
	}
}

func TestCallTimeToolGetCurrentTime(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name": "get_current_time",
		"arguments": map[string]any{
			"timezone": "UTC",
		},
	})
	result, errObj := callTimeTool(params)
	if errObj != nil {
		t.Fatalf("callTimeTool(get_current_time) returned error: %+v", errObj)
	}
	r := result.(map[string]any)
	content := r["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, "UTC") || !strings.Contains(text, "datetime") {
		t.Fatalf("get_current_time UTC result = %q, want UTC/datetime", text)
	}
}

func TestCallTimeToolGetCurrentTimeDefaultTZ(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name":      "get_current_time",
		"arguments": map[string]any{},
	})
	result, errObj := callTimeTool(params)
	if errObj != nil {
		t.Fatalf("callTimeTool(get_current_time, no tz) error: %+v", errObj)
	}
	r := result.(map[string]any)
	content := r["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, "datetime") {
		t.Fatalf("get_current_time default result = %q, want datetime", text)
	}
}

func TestCallTimeToolConvertTime(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name": "convert_time",
		"arguments": map[string]any{
			"source_timezone": "America/New_York",
			"time":            "12:00",
			"target_timezone": "Europe/London",
		},
	})
	result, errObj := callTimeTool(params)
	if errObj != nil {
		t.Fatalf("callTimeTool(convert_time) error: %+v", errObj)
	}
	r := result.(map[string]any)
	content := r["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, "source") || !strings.Contains(text, "target") || !strings.Contains(text, "time_difference") {
		t.Fatalf("convert_time result = %q, want source/target/time_difference", text)
	}
}

func TestCallTimeToolConvertTimeMissingArgs(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name": "convert_time",
		"arguments": map[string]any{
			"source_timezone": "UTC",
			// missing time and target_timezone
		},
	})
	result, errObj := callTimeTool(params)
	// Missing args → result contains error text, not an error object
	if errObj != nil {
		t.Fatalf("callTimeTool(convert_time, missing args) returned error object: %+v", errObj)
	}
	r := result.(map[string]any)
	content := r["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, "required") && !strings.Contains(text, "error") {
		t.Fatalf("convert_time missing args result = %q, want error text", text)
	}
}

func TestCallTimeToolUnknownTool(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name":      "nonexistent_tool",
		"arguments": map[string]any{},
	})
	_, errObj := callTimeTool(params)
	if errObj == nil {
		t.Fatal("callTimeTool(unknown) should return error")
	}
	if errObj.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", errObj.Code)
	}
}

func TestCallTimeToolInvalidParams(t *testing.T) {
	// Pass invalid JSON as params.
	_, errObj := callTimeTool(json.RawMessage(`not json`))
	if errObj == nil {
		t.Fatal("callTimeTool(invalid json) should return error")
	}
}

func TestCallTimeToolInvalidTimezone(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"name": "get_current_time",
		"arguments": map[string]any{
			"timezone": "Moon/Base",
		},
	})
	result, errObj := callTimeTool(params)
	if errObj != nil {
		t.Fatalf("callTimeTool(bad tz) returned error object: %+v", errObj)
	}
	r := result.(map[string]any)
	content := r["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, "unknown") && !strings.Contains(text, "error") {
		t.Fatalf("bad timezone result = %q, want error text", text)
	}
}

func TestTimeToolsStable(t *testing.T) {
	tools := timeTools()
	if len(tools) != 2 {
		t.Fatalf("timeTools() = %d, want 2", len(tools))
	}
	names := make(map[string]bool)
	for _, tt := range tools {
		name, ok := tt["name"].(string)
		if !ok {
			t.Fatal("tool missing name")
		}
		if names[name] {
			t.Fatalf("duplicate tool name: %s", name)
		}
		names[name] = true
		schema, ok := tt["inputSchema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("%s: inputSchema missing or wrong type", name)
		}
	}
	if !names["get_current_time"] || !names["convert_time"] {
		t.Fatal("missing expected tools")
	}
}

func TestHandleTimeLineInit(t *testing.T) {
	var buf bytes.Buffer
	w := newWriter(&buf)
	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	err := handleTimeLine(line, w, "test-version")
	if err != nil {
		t.Fatalf("handleTimeLine(initialize): %v", err)
	}
	w.Flush()
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal initialize response: %v", err)
	}
	if resp.Result.ProtocolVersion != protocolVersion {
		t.Fatalf("protocolVersion = %q, want %q", resp.Result.ProtocolVersion, protocolVersion)
	}
	if resp.Result.ServerInfo.Name != "reasonix-time" || resp.Result.ServerInfo.Version != "test-version" {
		t.Fatalf("serverInfo = %+v", resp.Result.ServerInfo)
	}
}

func TestHandleTimeLineNotify(t *testing.T) {
	var buf bytes.Buffer
	w := newWriter(&buf)
	line := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n")
	err := handleTimeLine(line, w, "test")
	if err != nil {
		t.Fatalf("handleTimeLine(notify): %v", err)
	}
	w.Flush()
	if buf.Len() != 0 {
		t.Fatalf("notification produced output: %s", buf.String())
	}
}

func TestHandleTimeLineUnknownMethod(t *testing.T) {
	var buf bytes.Buffer
	w := newWriter(&buf)
	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"bogus","params":{}}` + "\n")
	err := handleTimeLine(line, w, "test")
	if err != nil {
		t.Fatalf("handleTimeLine(bogus): %v", err)
	}
	w.Flush()
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected error -32601, got %+v", resp.Error)
	}
}

func TestHandleTimeLineBadJSON(t *testing.T) {
	var buf bytes.Buffer
	w := newWriter(&buf)
	line := []byte(`not json` + "\n")
	err := handleTimeLine(line, w, "test")
	if err != nil {
		t.Fatalf("handleTimeLine(bad json): %v", err)
	}
	w.Flush()
	if buf.Len() != 0 {
		t.Fatalf("bad JSON produced output: %s", buf.String())
	}
}

// newWriter creates a bufio.Writer wrapping a bytes.Buffer, so handleTimeLine
// receives the *bufio.Writer it expects instead of a raw io.Writer.
func newWriter(buf *bytes.Buffer) *bufio.Writer {
	return bufio.NewWriter(buf)
}
